package engrave

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// Vec3 is a point or direction in model space, millimetres, Z up.
type Vec3 struct{ X, Y, Z float64 }

// Add returns a + b.
func (a Vec3) Add(b Vec3) Vec3 { return Vec3{a.X + b.X, a.Y + b.Y, a.Z + b.Z} }

// Sub returns a - b.
func (a Vec3) Sub(b Vec3) Vec3 { return Vec3{a.X - b.X, a.Y - b.Y, a.Z - b.Z} }

// Mul returns the vector scaled by s.
func (a Vec3) Mul(s float64) Vec3 { return Vec3{a.X * s, a.Y * s, a.Z * s} }

// Dot returns the dot product.
func (a Vec3) Dot(b Vec3) float64 { return a.X*b.X + a.Y*b.Y + a.Z*b.Z }

// Cross returns the cross product.
func (a Vec3) Cross(b Vec3) Vec3 {
	return Vec3{a.Y*b.Z - a.Z*b.Y, a.Z*b.X - a.X*b.Z, a.X*b.Y - a.Y*b.X}
}

// Len returns the Euclidean length.
func (a Vec3) Len() float64 { return math.Sqrt(a.Dot(a)) }

// Unit returns the vector scaled to length one, or the zero vector as is.
func (a Vec3) Unit() Vec3 {
	l := a.Len()
	if l == 0 {
		return a
	}
	return Vec3{a.X / l, a.Y / l, a.Z / l}
}

// Vec2 is a point in a face's local frame, millimetres.
type Vec2 struct{ X, Y float64 }

// Triangle is one STL facet, vertices in counter-clockwise order seen from
// outside the solid.
type Triangle [3]Vec3

// Normal is the outward unit normal implied by the winding. The normal a
// file stores is ignored throughout this package: files lie about it often
// enough that anything needing one recomputes it.
func (t Triangle) Normal() Vec3 {
	return t[1].Sub(t[0]).Cross(t[2].Sub(t[0])).Unit()
}

// Area is the triangle's area.
func (t Triangle) Area() float64 {
	return t[1].Sub(t[0]).Cross(t[2].Sub(t[0])).Len() / 2
}

// Mesh is triangle soup, which is all an STL is. Connectivity, when a step
// needs it, is recovered by welding vertices; it is not carried here.
type Mesh struct {
	Triangles []Triangle
}

// maxTriangles bounds what one file may ask this process to hold. Fifty
// million triangles is a scan of a building, not a part.
const maxTriangles = 50_000_000

// Load reads either encoding of STL. The binary layout is fixed, so the
// reliable test is arithmetic: a binary file's length is exactly its header
// plus fifty bytes a triangle. "solid" at the front proves nothing -- binary
// exporters write it too.
func Load(r io.Reader) (Mesh, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return Mesh{}, err
	}
	if len(data) >= 84 {
		count := binary.LittleEndian.Uint32(data[80:84])
		if count > 0 && count <= maxTriangles && int64(len(data)) == 84+int64(count)*50 {
			return parseBinary(data, count), nil
		}
	}
	if bytes.HasPrefix(bytes.TrimLeft(data, " \t\r\n"), []byte("solid")) {
		return parseASCII(data)
	}
	return Mesh{}, fmt.Errorf("engrave: not an STL file")
}

func parseBinary(data []byte, count uint32) Mesh {
	m := Mesh{Triangles: make([]Triangle, 0, count)}
	off := 84
	for range count {
		off += 12 // the stored normal; recomputed when needed
		var tri Triangle
		for v := range 3 {
			tri[v] = Vec3{
				X: float64(math.Float32frombits(binary.LittleEndian.Uint32(data[off:]))),
				Y: float64(math.Float32frombits(binary.LittleEndian.Uint32(data[off+4:]))),
				Z: float64(math.Float32frombits(binary.LittleEndian.Uint32(data[off+8:]))),
			}
			off += 12
		}
		m.Triangles = append(m.Triangles, tri)
		off += 2 // attribute byte count
	}
	return m
}

func parseASCII(data []byte) (Mesh, error) {
	var m Mesh
	var tri Triangle
	verts := 0
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 4 && fields[0] == "vertex" {
			x, err1 := strconv.ParseFloat(fields[1], 64)
			y, err2 := strconv.ParseFloat(fields[2], 64)
			z, err3 := strconv.ParseFloat(fields[3], 64)
			if err1 != nil || err2 != nil || err3 != nil {
				return Mesh{}, fmt.Errorf("engrave: bad vertex line %q", sc.Text())
			}
			if verts < 3 {
				tri[verts] = Vec3{x, y, z}
			}
			verts++
			continue
		}
		if len(fields) >= 1 && fields[0] == "endfacet" {
			if verts != 3 {
				return Mesh{}, fmt.Errorf("engrave: facet with %d vertices", verts)
			}
			if len(m.Triangles) >= maxTriangles {
				return Mesh{}, fmt.Errorf("engrave: more than %d triangles", maxTriangles)
			}
			m.Triangles = append(m.Triangles, tri)
			verts = 0
		}
	}
	if err := sc.Err(); err != nil {
		return Mesh{}, err
	}
	if len(m.Triangles) == 0 {
		return Mesh{}, fmt.Errorf("engrave: no facets")
	}
	return m, nil
}

// WriteBinary encodes the mesh in the binary layout, normals recomputed from
// the winding. The header is fixed, so identical meshes encode to identical
// bytes.
func (m Mesh) WriteBinary(w io.Writer) error {
	buf := make([]byte, 84, 84+len(m.Triangles)*50)
	copy(buf, "printing-prototype-tracker engrave")
	binary.LittleEndian.PutUint32(buf[80:], uint32(len(m.Triangles)))
	var scratch [50]byte
	for _, t := range m.Triangles {
		n := t.Normal()
		coords := [12]float64{n.X, n.Y, n.Z,
			t[0].X, t[0].Y, t[0].Z, t[1].X, t[1].Y, t[1].Z, t[2].X, t[2].Y, t[2].Z}
		for i, c := range coords {
			binary.LittleEndian.PutUint32(scratch[i*4:], math.Float32bits(float32(c)))
		}
		scratch[48], scratch[49] = 0, 0
		buf = append(buf, scratch[:]...)
	}
	_, err := w.Write(buf)
	return err
}

// Bounds is the axis-aligned box around every vertex.
func (m Mesh) Bounds() (min, max Vec3) {
	min = Vec3{math.Inf(1), math.Inf(1), math.Inf(1)}
	max = Vec3{math.Inf(-1), math.Inf(-1), math.Inf(-1)}
	for _, t := range m.Triangles {
		for _, v := range t {
			min.X, min.Y, min.Z = math.Min(min.X, v.X), math.Min(min.Y, v.Y), math.Min(min.Z, v.Z)
			max.X, max.Y, max.Z = math.Max(max.X, v.X), math.Max(max.Y, v.Y), math.Max(max.Z, v.Z)
		}
	}
	return min, max
}

// Volume is the signed enclosed volume by the divergence theorem: positive
// for a closed mesh wound outward, garbage for an open one -- which makes it
// a cheap sanity probe as well as a measurement.
func (m Mesh) Volume() float64 {
	var v float64
	for _, t := range m.Triangles {
		v += t[0].Dot(t[1].Cross(t[2])) / 6
	}
	return v
}

// SurfaceArea is the sum of every triangle's area.
func (m Mesh) SurfaceArea() float64 {
	var a float64
	for _, t := range m.Triangles {
		a += t.Area()
	}
	return a
}
