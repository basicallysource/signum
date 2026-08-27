package engrave

import (
	"fmt"
	"math"
	"slices"
)

// weldTol is how close two vertices must be, mm, to count as the same point
// when recovering connectivity from triangle soup. Exported STLs repeat a
// shared vertex bit-identically; this tolerance only mops up ASCII files that
// printed the same point two slightly different ways.
const weldTol = 1e-5

// weldedMesh is the mesh with connectivity recovered: every distinct vertex
// once, triangles as index triples. verts keeps the first-seen exact
// coordinates, so welding never moves a point that was already consistent.
type weldedMesh struct {
	verts []Vec3
	tris  [][3]int
}

func weld(m Mesh) weldedMesh {
	w := weldedMesh{tris: make([][3]int, len(m.Triangles))}
	grid := map[[3]int64][]int{}
	key := func(v Vec3) [3]int64 {
		return [3]int64{
			int64(math.Floor(v.X / weldTol)),
			int64(math.Floor(v.Y / weldTol)),
			int64(math.Floor(v.Z / weldTol)),
		}
	}
	add := func(v Vec3) int {
		k := key(v)
		for dx := int64(-1); dx <= 1; dx++ {
			for dy := int64(-1); dy <= 1; dy++ {
				for dz := int64(-1); dz <= 1; dz++ {
					for _, id := range grid[[3]int64{k[0] + dx, k[1] + dy, k[2] + dz}] {
						if v.Sub(w.verts[id]).Len() <= weldTol {
							return id
						}
					}
				}
			}
		}
		w.verts = append(w.verts, v)
		id := len(w.verts) - 1
		grid[k] = append(grid[k], id)
		return id
	}
	for i, t := range m.Triangles {
		w.tris[i] = [3]int{add(t[0]), add(t[1]), add(t[2])}
	}
	return w
}

// facet is a connected run of coplanar triangles: one flat face of the part.
// Two separate flats on the same plane are two facets, so each is a distinct
// place a mark could go.
type facet struct {
	tris   []int // mesh triangle indices, ascending
	normal Vec3  // outward unit normal
	offset float64
	area   float64
	isBed  bool
	label  string
}

// world maps a point on the facet's plane, given in (u, v) frame coordinates,
// back to model space.
func (f *facet) world(u, v Vec3, x, y float64) Vec3 {
	return u.Mul(x).Add(v.Mul(y)).Add(f.normal.Mul(f.offset))
}

// facets finds every planar face, largest first, capped at maxFacets. A face
// is grown by walking triangle adjacency from a seed and accepting neighbours
// whose normals agree within NormalTol and whose vertices sit within PlaneTol
// of the seed's plane -- measuring against the seed, not the previous
// neighbour, so a gently curved surface cannot creep in a fraction of a
// degree at a time.
func facets(m Mesh, w weldedMesh) []facet {
	n := len(m.Triangles)
	normals := make([]Vec3, n)
	areas := make([]float64, n)
	for i, t := range m.Triangles {
		normals[i] = t.Normal()
		areas[i] = t.Area()
	}

	// Adjacency across shared welded edges.
	edgeTris := map[[2]int][]int{}
	for i, t := range w.tris {
		for e := range 3 {
			a, b := t[e], t[(e+1)%3]
			if a > b {
				a, b = b, a
			}
			edgeTris[[2]int{a, b}] = append(edgeTris[[2]int{a, b}], i)
		}
	}

	// Seeds in descending area order so a facet's plane is defined by its
	// most reliable triangle, and so the grouping is deterministic.
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		if areas[a] != areas[b] {
			if areas[a] > areas[b] {
				return -1
			}
			return 1
		}
		return a - b
	})

	minB, _ := m.Bounds()
	bedOffset := -minB.Z // the plane z == min z, as seen along -Z

	visited := make([]bool, n)
	var out []facet
	for _, seed := range order {
		if visited[seed] || areas[seed] < 1e-9 {
			continue
		}
		sn := normals[seed]
		soff := (m.Triangles[seed][0].Dot(sn) + m.Triangles[seed][1].Dot(sn) + m.Triangles[seed][2].Dot(sn)) / 3
		group := []int{seed}
		visited[seed] = true
		for i := 0; i < len(group); i++ {
			t := w.tris[group[i]]
			for e := range 3 {
				a, b := t[e], t[(e+1)%3]
				if a > b {
					a, b = b, a
				}
				for _, nb := range edgeTris[[2]int{a, b}] {
					if visited[nb] || areas[nb] < 1e-9 || normals[nb].Dot(sn) < NormalTol {
						continue
					}
					inPlane := true
					for _, v := range m.Triangles[nb] {
						if math.Abs(v.Dot(sn)-soff) > PlaneTol {
							inPlane = false
							break
						}
					}
					if inPlane {
						visited[nb] = true
						group = append(group, nb)
					}
				}
			}
		}
		slices.Sort(group)

		var f facet
		f.tris = group
		var wsum Vec3
		for _, ti := range group {
			wsum = wsum.Add(normals[ti].Mul(areas[ti]))
			f.area += areas[ti]
		}
		f.normal = wsum.Unit()
		var osum float64
		for _, ti := range group {
			c := m.Triangles[ti][0].Add(m.Triangles[ti][1]).Add(m.Triangles[ti][2]).Mul(1.0 / 3)
			osum += c.Dot(f.normal) * areas[ti]
		}
		f.offset = osum / f.area
		f.isBed = f.normal.Z < -NormalTol && math.Abs(f.offset-bedOffset) < PlaneTol
		f.label = faceLabel(f.normal, f.isBed)
		out = append(out, f)
	}

	slices.SortStableFunc(out, func(a, b facet) int {
		if a.area != b.area {
			if a.area > b.area {
				return -1
			}
			return 1
		}
		return a.tris[0] - b.tris[0]
	})
	if len(out) > maxFacets {
		out = out[:maxFacets]
	}
	return out
}

// faceFrame spans a flat face so that text reads upright from outside: v is
// as close to world up as the face allows -- or +Y on a face too near
// horizontal to have an up -- and u = v x n, so u x v = n.
func faceFrame(n Vec3) (u, v Vec3) {
	up := Vec3{0, 0, 1}
	if math.Abs(n.Z) >= 0.9 {
		up = Vec3{0, 1, 0}
	}
	v = up.Sub(n.Mul(up.Dot(n))).Unit()
	u = v.Cross(n)
	return u, v
}

// faceLabel is a short human name for where a face points. The bed face gets
// its own name because it is the preferred spot and the reason why is worth
// a word: a first-layer pocket prints cleanest and hides once assembled.
func faceLabel(n Vec3, isBed bool) string {
	if isBed {
		return "bed face"
	}
	if n.Z > 0.95 {
		return "top face"
	}
	if n.Z < -0.95 {
		return "bottom face"
	}
	if math.Abs(n.X) > 0.95 {
		return fmt.Sprintf("%cx wall", sign(n.X))
	}
	if math.Abs(n.Y) > 0.95 {
		return fmt.Sprintf("%cy wall", sign(n.Y))
	}
	return "angled face"
}

func sign(v float64) byte {
	if v > 0 {
		return '+'
	}
	return '-'
}
