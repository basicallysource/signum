package engrave

// Test fixtures are generated in code -- no binary files live in this
// repository -- and the font is fetched by its pinned hash into the user
// cache; tests that need it skip when the network cannot provide it.

import (
	"sync"
	"testing"
)

// box returns a closed axis-aligned box, wound outward, with every shared
// corner bit-identical: the mesh a well-behaved exporter would produce.
func box(x0, y0, z0, x1, y1, z1 float64) Mesh {
	c := [8]Vec3{
		{x0, y0, z0}, {x1, y0, z0}, {x1, y1, z0}, {x0, y1, z0},
		{x0, y0, z1}, {x1, y0, z1}, {x1, y1, z1}, {x0, y1, z1},
	}
	quads := [6][4]int{
		{0, 3, 2, 1}, // bottom, -z
		{4, 5, 6, 7}, // top, +z
		{0, 1, 5, 4}, // -y
		{2, 3, 7, 6}, // +y
		{3, 0, 4, 7}, // -x
		{1, 2, 6, 5}, // +x
	}
	var m Mesh
	for _, q := range quads {
		m.Triangles = append(m.Triangles,
			Triangle{c[q[0]], c[q[1]], c[q[2]]},
			Triangle{c[q[0]], c[q[2]], c[q[3]]},
		)
	}
	return m
}

// plate is the standard test part: a 60 x 40 x 8 slab sitting on z = 0.
func plate() Mesh { return box(0, 0, 0, 60, 40, 8) }

// griddedPlate is the plate with its top face tessellated as a 10 x 8 grid
// instead of two triangles, and the walls fanned so their top edges
// subdivide at the same pitch. A face full of interior vertices and edges is
// what exercises the cut's union boundary and rectangle nudging; the
// two-triangle fixtures never do.
func griddedPlate() Mesh {
	nx, ny := 10, 8
	var tris []Triangle
	v := func(x, y, z float64) Vec3 { return Vec3{x, y, z} }
	tris = append(tris,
		Triangle{v(0, 0, 0), v(0, 40, 0), v(60, 40, 0)},
		Triangle{v(0, 0, 0), v(60, 40, 0), v(60, 0, 0)})
	for j := range ny {
		for i := range nx {
			x0, x1 := 60*float64(i)/float64(nx), 60*float64(i+1)/float64(nx)
			y0, y1 := 40*float64(j)/float64(ny), 40*float64(j+1)/float64(ny)
			tris = append(tris,
				Triangle{v(x0, y0, 8), v(x1, y0, 8), v(x1, y1, 8)},
				Triangle{v(x0, y0, 8), v(x1, y1, 8), v(x0, y1, 8)})
		}
	}
	wall := func(b0, b1 Vec3, tops []Vec3) {
		tris = append(tris, Triangle{b0, b1, tops[len(tops)-1]})
		for k := len(tops) - 1; k > 0; k-- {
			tris = append(tris, Triangle{b0, tops[k], tops[k-1]})
		}
	}
	var south, north, west, east []Vec3
	for i := 0; i <= nx; i++ {
		x := 60 * float64(i) / float64(nx)
		south = append(south, v(x, 0, 8))
		north = append([]Vec3{v(x, 40, 8)}, north...)
	}
	for j := 0; j <= ny; j++ {
		y := 40 * float64(j) / float64(ny)
		east = append(east, v(60, y, 8))
		west = append([]Vec3{v(0, y, 8)}, west...)
	}
	wall(v(0, 0, 0), v(60, 0, 0), south)
	wall(v(60, 40, 0), v(0, 40, 0), north)
	wall(v(60, 0, 0), v(60, 40, 0), east)
	wall(v(0, 40, 0), v(0, 0, 0), west)
	return Mesh{Triangles: tris}
}

// holedPlate is the plate with a 12 x 12 opening through its middle: every
// horizontal face is a ring of patches around the opening, so placement has
// to steer around a hole in the face outline and the cut sees a facet whose
// boundary is two loops.
func holedPlate() Mesh {
	hx0, hy0, hx1, hy1 := 24.0, 14.0, 36.0, 26.0
	var tris []Triangle
	v := func(x, y, z float64) Vec3 { return Vec3{x, y, z} }
	quad := func(a, b, c, d Vec3) {
		tris = append(tris, Triangle{a, b, c}, Triangle{a, c, d})
	}
	xs := []float64{0, hx0, hx1, 60}
	ring := func(z float64, up bool) {
		patch := func(x0, y0, x1, y1 float64) {
			if up {
				quad(v(x0, y0, z), v(x1, y0, z), v(x1, y1, z), v(x0, y1, z))
			} else {
				quad(v(x0, y0, z), v(x0, y1, z), v(x1, y1, z), v(x1, y0, z))
			}
		}
		for i := range 3 {
			patch(xs[i], 0, xs[i+1], hy0)
			patch(xs[i], hy1, xs[i+1], 40)
		}
		patch(0, hy0, hx0, hy1)
		patch(hx1, hy0, 60, hy1)
	}
	ring(8, true)
	ring(0, false)
	wall := func(x0, y0, x1, y1 float64, out bool) {
		if out {
			quad(v(x0, y0, 0), v(x1, y1, 0), v(x1, y1, 8), v(x0, y0, 8))
		} else {
			quad(v(x0, y0, 0), v(x0, y0, 8), v(x1, y1, 8), v(x1, y1, 0))
		}
	}
	ys := []float64{0, hy0, hy1, 40}
	for i := range 3 {
		wall(xs[i], 0, xs[i+1], 0, true)
		wall(xs[3-i], 40, xs[2-i], 40, true)
		wall(60, ys[i], 60, ys[i+1], true)
		wall(0, ys[3-i], 0, ys[2-i], true)
	}
	wall(hx0, hy0, hx1, hy0, false)
	wall(hx1, hy0, hx1, hy1, false)
	wall(hx1, hy1, hx0, hy1, false)
	wall(hx0, hy1, hx0, hy0, false)
	return Mesh{Triangles: tris}
}

// checkManifold fails the test unless the mesh is watertight and 2-manifold:
// every undirected edge shared by exactly two triangles with opposite
// orientation, and no triangle degenerate. Vertices are matched by exact
// bits, which is the standard this package's own output must meet.
func checkManifold(t *testing.T, m Mesh) {
	t.Helper()
	ids := map[Vec3]int{}
	id := func(v Vec3) int {
		if i, ok := ids[v]; ok {
			return i
		}
		i := len(ids)
		ids[v] = i
		return i
	}
	edges := map[[2]int]int{}
	for ti, tr := range m.Triangles {
		a, b, c := id(tr[0]), id(tr[1]), id(tr[2])
		if a == b || b == c || c == a {
			t.Fatalf("triangle %d is degenerate", ti)
		}
		edges[[2]int{a, b}]++
		edges[[2]int{b, c}]++
		edges[[2]int{c, a}]++
	}
	bad := 0
	for e, n := range edges {
		if n != 1 || edges[[2]int{e[1], e[0]}] != 1 {
			bad++
		}
	}
	if bad > 0 {
		t.Fatalf("%d directed edges are unpaired or doubled: mesh is not watertight", bad)
	}
}

var (
	fontOnce sync.Once
	fontVal  *Font
	fontErr  error
)

// testFont fetches and parses the pinned font once per run, skipping tests
// that need it when the network cannot deliver it.
func testFont(t *testing.T) *Font {
	t.Helper()
	fontOnce.Do(func() {
		path, err := FetchFont("")
		if err != nil {
			fontErr = err
			return
		}
		fontVal, fontErr = LoadFont(path)
	})
	if fontErr != nil {
		t.Skipf("pinned font unavailable: %v", fontErr)
	}
	return fontVal
}
