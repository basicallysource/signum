package engrave

import (
	"fmt"
	"math"
	"slices"
)

// Cut returns the mesh with the text recessed into the placed face as a
// pocket Depth deep, glyph counters left standing as islands, watertight.
//
// There is no 3D boolean here, and none is needed: the pocket only ever
// intrudes into one planar face, so the cut is local re-triangulation. A
// working rectangle is drawn around the text, strictly inside the face; the
// face triangles the rectangle touches are removed and their union is
// re-triangulated with the rectangle as a hole; the rectangle is filled back
// in at surface level with the glyphs as holes; and the glyphs get a floor
// at -Depth and vertical walls joining floor to surface. Because both sides
// of the rectangle boundary use exactly its four corners -- the removed
// region is re-triangulated as one union, never triangle by triangle --
// no T-junctions can appear along it.
//
// Everything new is built in one shared 2D frame and welded through one
// vertex pool, and original mesh vertices pass through untouched, so every
// edge of the result pairs exactly. The text must be the same string the
// placement was made with.
func Cut(m Mesh, text string, p Placement, f *Font) (Mesh, error) {
	if f == nil {
		return Mesh{}, fmt.Errorf("engrave: nil font")
	}
	if len(p.tris) == 0 || p.Depth <= 0 || p.CapHeight <= 0 {
		return Mesh{}, fmt.Errorf("engrave: placement was not produced by Placements")
	}
	for _, ti := range p.tris {
		if ti < 0 || ti >= len(m.Triangles) {
			return Mesh{}, fmt.Errorf("engrave: placement does not belong to this mesh")
		}
	}
	o, err := f.Outlines(text, p.CapHeight)
	if err != nil {
		return Mesh{}, err
	}
	if math.Abs((o.Max.X-o.Min.X)-p.Width) > 1e-6 || math.Abs((o.Max.Y-o.Min.Y)-p.Height) > 1e-6 {
		return Mesh{}, fmt.Errorf("engrave: text does not measure as placed; Cut needs the text Placements saw")
	}

	// One pool of 2D points in the text frame. Original mesh vertices are
	// registered first and keep their exact 3D coordinates, so the seam
	// between new geometry and the rest of the mesh is bit-identical.
	pl := newPool()
	w := weld(m)
	pid := map[int]int{} // welded vertex id -> pool id
	weldIDs := []int{}
	for _, ti := range p.tris {
		for _, vid := range w.tris[ti] {
			if _, ok := pid[vid]; !ok {
				pid[vid] = -1
				weldIDs = append(weldIDs, vid)
			}
		}
	}
	slices.Sort(weldIDs)
	for _, vid := range weldIDs {
		v := w.verts[vid]
		rel := v.Sub(p.Origin)
		pid[vid] = pl.add(Vec2{rel.Dot(p.U), rel.Dot(p.V)}, &v)
	}
	tri2 := make([][3]int, len(p.tris)) // facet triangles as pool ids
	for i, ti := range p.tris {
		for c := range 3 {
			tri2[i][c] = pid[w.tris[ti][c]]
		}
		if tri2[i][0] == tri2[i][1] || tri2[i][1] == tri2[i][2] || tri2[i][2] == tri2[i][0] {
			return Mesh{}, fmt.Errorf("engrave: face has a degenerate triangle at the weld tolerance")
		}
	}

	rect, err := placeRect(pl, tri2, o, p.CapHeight)
	if err != nil {
		return Mesh{}, err
	}

	// The triangles the rectangle touches. The test errs toward inclusion:
	// a triangle wrongly included only grows the re-triangulated region,
	// while one wrongly excluded would leave a hole under the rectangle.
	var cutIdx []int // indices into p.tris / tri2
	for i := range tri2 {
		if triTouchesRect(pl, tri2[i], rect) {
			cutIdx = append(cutIdx, i)
		}
	}
	if len(cutIdx) == 0 {
		return Mesh{}, fmt.Errorf("engrave: rectangle touches no face triangle")
	}

	outer, uHoles, err := unionBoundary(pl, tri2, cutIdx)
	if err != nil {
		return Mesh{}, err
	}
	// The construction depends on the rectangle sitting strictly inside the
	// removed region; verify rather than assume.
	for i := range outer {
		a, b := pl.pts[outer[i]], pl.pts[outer[(i+1)%len(outer)]]
		if segIntersectsRect(a, b, rect[0]-1e-9, rect[1]-1e-9, rect[2]+1e-9, rect[3]+1e-9) {
			return Mesh{}, fmt.Errorf("engrave: rectangle reaches the edge of the removed region")
		}
	}
	for _, c := range [][2]float64{{rect[0], rect[1]}, {rect[2], rect[1]}, {rect[2], rect[3]}, {rect[0], rect[3]}} {
		inside := false
		for _, i := range cutIdx {
			if pointInTri2(Vec2{c[0], c[1]}, [3]Vec2{pl.pts[tri2[i][0]], pl.pts[tri2[i][1]], pl.pts[tri2[i][2]]}) {
				inside = true
				break
			}
		}
		if !inside {
			return Mesh{}, fmt.Errorf("engrave: rectangle corner outside the removed region")
		}
	}

	// Glyph contours as pool ids. These are shared verbatim between the
	// surface fill, the floor, and the walls; that sharing is what makes
	// the pocket seams pair.
	loops := make([][]int, len(o.Loops))
	for i, l := range o.Loops {
		ids := make([]int, 0, len(l.Pts))
		for _, q := range l.Pts {
			id := pl.add(q, nil)
			if len(ids) > 0 && ids[len(ids)-1] == id {
				continue
			}
			ids = append(ids, id)
		}
		for len(ids) > 1 && ids[0] == ids[len(ids)-1] {
			ids = ids[:len(ids)-1]
		}
		if len(ids) < 3 {
			return Mesh{}, fmt.Errorf("engrave: glyph contour collapsed in the pool")
		}
		loops[i] = ids
	}
	children := make([][]int, len(o.Loops))
	for i, l := range o.Loops {
		if l.Parent >= 0 {
			children[l.Parent] = append(children[l.Parent], i)
		}
	}

	rectRing := []int{
		pl.add(Vec2{rect[0], rect[1]}, nil), pl.add(Vec2{rect[2], rect[1]}, nil),
		pl.add(Vec2{rect[2], rect[3]}, nil), pl.add(Vec2{rect[0], rect[3]}, nil),
	}

	at := func(id int) Vec2 { return pl.pts[id] }
	rev := func(ids []int) []int {
		r := slices.Clone(ids)
		slices.Reverse(r)
		return r
	}

	// The annulus: what remains of the removed region at surface level once
	// the rectangle is out of it.
	annulus, err := triangulateWithHoles(outer, append(slices.Clone(uHoles), rev(rectRing)), at)
	if err != nil {
		return Mesh{}, fmt.Errorf("engrave: annulus: %w", err)
	}
	// The rectangle back at surface level, glyphs cut out of it.
	var rectHoles [][]int
	for i, l := range o.Loops {
		if l.Depth == 0 {
			rectHoles = append(rectHoles, rev(loops[i]))
		}
	}
	rectFill, err := triangulateWithHoles(rectRing, rectHoles, at)
	if err != nil {
		return Mesh{}, fmt.Errorf("engrave: rectangle fill: %w", err)
	}
	// Islands: a counter (the hole of an o) stays at surface level, a raised
	// island in the pocket. Depth alternates, so fills alternate too: even
	// depths are pocket floor, odd depths are surface.
	var islands, floors [][3]int
	for i, l := range o.Loops {
		if l.Depth%2 == 1 {
			var holes [][]int
			for _, c := range children[i] {
				holes = append(holes, rev(loops[c]))
			}
			tris, err := triangulateWithHoles(rev(loops[i]), holes, at)
			if err != nil {
				return Mesh{}, fmt.Errorf("engrave: island: %w", err)
			}
			islands = append(islands, tris...)
		} else {
			var holes [][]int
			for _, c := range children[i] {
				holes = append(holes, loops[c])
			}
			tris, err := triangulateWithHoles(loops[i], holes, at)
			if err != nil {
				return Mesh{}, fmt.Errorf("engrave: floor: %w", err)
			}
			floors = append(floors, tris...)
		}
	}

	// Back to 3D. Everything at surface level lies on the placement plane
	// except original vertices, which keep their own exact coordinates.
	at3 := func(id int, z float64) Vec3 {
		if z == 0 && pl.has[id] {
			return pl.exact[id]
		}
		q := pl.pts[id]
		return p.Origin.Add(p.U.Mul(q.X)).Add(p.V.Mul(q.Y)).Add(p.Normal.Mul(z))
	}

	removed := map[int]bool{}
	for _, i := range cutIdx {
		removed[p.tris[i]] = true
	}
	out := Mesh{Triangles: make([]Triangle, 0, len(m.Triangles)+len(annulus)+len(rectFill)+8*len(loops))}
	for ti, t := range m.Triangles {
		if !removed[ti] {
			out.Triangles = append(out.Triangles, t)
		}
	}
	emit := func(tris [][3]int, z float64) {
		for _, t := range tris {
			out.Triangles = append(out.Triangles, Triangle{at3(t[0], z), at3(t[1], z), at3(t[2], z)})
		}
	}
	emit(annulus, 0)
	emit(rectFill, 0)
	emit(islands, 0)
	emit(floors, -p.Depth)
	// Walls: one quad per contour edge, from the surface loop straight down
	// to the floor loop. With even loops counter-clockwise and odd loops
	// clockwise, the same winding puts every wall's outward normal into the
	// pocket air, both on letter outlines and around islands.
	for _, ids := range loops {
		for i := range ids {
			pI, q := ids[i], ids[(i+1)%len(ids)]
			p0, q0 := at3(pI, 0), at3(q, 0)
			p1, q1 := at3(pI, -p.Depth), at3(q, -p.Depth)
			out.Triangles = append(out.Triangles,
				Triangle{p0, q0, q1},
				Triangle{p0, q1, p1},
			)
		}
	}
	return out, nil
}

// placeRect draws the working rectangle around the text: its bounding box
// grown by half the placement margin, then nudged outward until it clears
// every face vertex and every face edge passes no rect corner too closely.
// The clearance matters because a face vertex sitting exactly on the
// rectangle boundary would pinch the re-triangulated region to zero width
// there, which ear clipping cannot survive; a micrometre of daylight costs
// nothing and removes the whole class of failure.
func placeRect(pl *pool, tri2 [][3]int, o *TextOutline, cap float64) ([4]float64, error) {
	rm := margin(cap) / 2
	rect := [4]float64{o.Min.X - rm, o.Min.Y - rm, o.Max.X + rm, o.Max.Y + rm}
	budget := margin(cap) - rm - 0.05 // stay inside the region placement verified
	const clear = 1e-3
	const step = 3e-3

	verts := map[int]bool{}
	var edges [][2]Vec2
	seen := map[[2]int]bool{}
	for _, t := range tri2 {
		for c := range 3 {
			verts[t[c]] = true
			a, b := t[c], t[(c+1)%3]
			if a > b {
				a, b = b, a
			}
			if !seen[[2]int{a, b}] {
				seen[[2]int{a, b}] = true
				edges = append(edges, [2]Vec2{pl.pts[a], pl.pts[b]})
			}
		}
	}
	vpts := make([]Vec2, 0, len(verts))
	for id := range len(pl.pts) {
		if verts[id] {
			vpts = append(vpts, pl.pts[id])
		}
	}

	grown := 0.0
	for pass := 0; pass < 40; pass++ {
		moved := false
		// A vertex too near a rectangle line, within that line's span.
		for _, v := range vpts {
			pad := 0.1
			if v.Y > rect[1]-pad && v.Y < rect[3]+pad {
				if math.Abs(v.X-rect[0]) < clear {
					rect[0] -= step
					moved = true
				}
				if math.Abs(v.X-rect[2]) < clear {
					rect[2] += step
					moved = true
				}
			}
			if v.X > rect[0]-pad && v.X < rect[2]+pad {
				if math.Abs(v.Y-rect[1]) < clear {
					rect[1] -= step
					moved = true
				}
				if math.Abs(v.Y-rect[3]) < clear {
					rect[3] += step
					moved = true
				}
			}
		}
		// A face edge passing too near a rectangle corner.
		corners := [][2]float64{{rect[0], rect[1]}, {rect[2], rect[1]}, {rect[2], rect[3]}, {rect[0], rect[3]}}
		for ci, c := range corners {
			for _, e := range edges {
				if distPointSeg(Vec2{c[0], c[1]}, e[0], e[1]) < clear {
					if ci < 2 {
						rect[1] -= step
					} else {
						rect[3] += step
					}
					moved = true
					break
				}
			}
		}
		if !moved {
			return rect, nil
		}
		grown += step
		if grown > budget {
			return rect, fmt.Errorf("engrave: could not clear the face tessellation around the text")
		}
	}
	return rect, fmt.Errorf("engrave: rectangle placement did not settle")
}

func distPointSeg(p, a, b Vec2) float64 {
	dx, dy := b.X-a.X, b.Y-a.Y
	l2 := dx*dx + dy*dy
	t := 0.0
	if l2 > 0 {
		t = math.Max(0, math.Min(1, ((p.X-a.X)*dx+(p.Y-a.Y)*dy)/l2))
	}
	return math.Hypot(p.X-(a.X+t*dx), p.Y-(a.Y+t*dy))
}

// triTouchesRect reports whether the triangle and the rectangle share any
// area, erring toward yes by a whisker.
func triTouchesRect(pl *pool, t [3]int, rect [4]float64) bool {
	const eps = 1e-7
	a, b, c := pl.pts[t[0]], pl.pts[t[1]], pl.pts[t[2]]
	x0, y0, x1, y1 := rect[0]-eps, rect[1]-eps, rect[2]+eps, rect[3]+eps
	if segIntersectsRect(a, b, x0, y0, x1, y1) ||
		segIntersectsRect(b, c, x0, y0, x1, y1) ||
		segIntersectsRect(c, a, x0, y0, x1, y1) {
		return true
	}
	// The rectangle can also sit wholly inside the triangle.
	return pointInTri2(Vec2{rect[0], rect[1]}, [3]Vec2{a, b, c})
}

// unionBoundary chains the boundary of the removed triangles into rings:
// one counter-clockwise outer ring, plus clockwise rings around any face
// triangles the removed set encloses without containing.
func unionBoundary(pl *pool, tri2 [][3]int, cutIdx []int) (outer []int, holes [][]int, err error) {
	type edge struct{ a, b int }
	count := map[edge]int{}
	var order []edge
	for _, i := range cutIdx {
		t := tri2[i]
		for c := range 3 {
			e := edge{t[c], t[(c+1)%3]}
			if count[e] == 0 {
				order = append(order, e)
			}
			count[e]++
		}
	}
	var boundary []edge
	for _, e := range order {
		if count[e] > 1 {
			return nil, nil, fmt.Errorf("engrave: face triangles overlap")
		}
		if count[edge{e.b, e.a}] == 0 {
			boundary = append(boundary, e)
		}
	}
	outgoing := map[int][]int{} // vertex -> indices into boundary, in order
	for i, e := range boundary {
		outgoing[e.a] = append(outgoing[e.a], i)
	}
	used := make([]bool, len(boundary))
	var rings [][]int
	for start := range boundary {
		if used[start] {
			continue
		}
		ring := []int{boundary[start].a}
		cur := boundary[start]
		used[start] = true
		for cur.b != ring[0] {
			ring = append(ring, cur.b)
			if len(ring) > len(boundary) {
				return nil, nil, fmt.Errorf("engrave: boundary does not close")
			}
			// At a pinch vertex the walk must take the sharpest left turn
			// to stay on its own ring; anywhere else there is exactly one
			// way to go.
			din := pl.pts[cur.b].sub(pl.pts[cur.a])
			next := -1
			bestAngle := math.Inf(-1)
			for _, ei := range outgoing[cur.b] {
				if used[ei] {
					continue
				}
				dout := pl.pts[boundary[ei].b].sub(pl.pts[boundary[ei].a])
				angle := math.Atan2(din.X*dout.Y-din.Y*dout.X, din.X*dout.X+din.Y*dout.Y)
				if angle > bestAngle {
					bestAngle = angle
					next = ei
				}
			}
			if next < 0 {
				return nil, nil, fmt.Errorf("engrave: boundary does not close")
			}
			cur = boundary[next]
			used[next] = true
		}
		rings = append(rings, ring)
	}
	for _, r := range rings {
		pts := make([]Vec2, len(r))
		for i, id := range r {
			pts[i] = pl.pts[id]
		}
		if loopArea(pts) > 0 {
			if outer != nil {
				return nil, nil, fmt.Errorf("engrave: removed region is not connected")
			}
			outer = r
		} else {
			holes = append(holes, r)
		}
	}
	if outer == nil {
		return nil, nil, fmt.Errorf("engrave: removed region has no outer boundary")
	}
	return outer, holes, nil
}

func (a Vec2) sub(b Vec2) Vec2 { return Vec2{a.X - b.X, a.Y - b.Y} }

// pool is the shared 2D vertex pool for a cut. Points within poolTol merge,
// first registration winning, so the same location computed two slightly
// different ways still lands on one id -- which is the whole game for edge
// pairing. An entry registered with exact 3D coordinates (an original mesh
// vertex) hands those bits back out at surface level.
type pool struct {
	pts   []Vec2
	exact []Vec3
	has   []bool
	grid  map[[2]int64][]int
}

const poolTol = 1e-6
const poolCell = 1e-5

func newPool() *pool { return &pool{grid: map[[2]int64][]int{}} }

func (p *pool) add(q Vec2, exact *Vec3) int {
	k := [2]int64{int64(math.Floor(q.X / poolCell)), int64(math.Floor(q.Y / poolCell))}
	for dx := int64(-1); dx <= 1; dx++ {
		for dy := int64(-1); dy <= 1; dy++ {
			for _, id := range p.grid[[2]int64{k[0] + dx, k[1] + dy}] {
				if math.Hypot(q.X-p.pts[id].X, q.Y-p.pts[id].Y) <= poolTol {
					if exact != nil && !p.has[id] {
						p.exact[id] = *exact
						p.has[id] = true
					}
					return id
				}
			}
		}
	}
	p.pts = append(p.pts, q)
	if exact != nil {
		p.exact = append(p.exact, *exact)
		p.has = append(p.has, true)
	} else {
		p.exact = append(p.exact, Vec3{})
		p.has = append(p.has, false)
	}
	id := len(p.pts) - 1
	p.grid[k] = append(p.grid[k], id)
	return id
}
