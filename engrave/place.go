package engrave

import (
	"fmt"
	"math"
	"slices"
)

// Placement is one spot the text fits: a face, a frame on it, and the sizes
// the wall behind it allows. Placements are ranked, best first.
//
// The frame is everything a consumer needs to work with the mark in 3D:
// Origin is where the text's local (0,0) -- the left end of its baseline --
// sits on the face, the text runs along U with cap height rising along V,
// and U x V is the outward Normal. A placement is only meaningful for the
// mesh and text that produced it; Cut checks what it can, but it cannot
// detect a placement replayed against a different mesh with the same shape.
type Placement struct {
	Origin Vec3
	U, V   Vec3
	Normal Vec3

	// CapHeight is the cap height chosen for this face: CapHeight, or
	// CapHeightSmall when the full size did not fit.
	CapHeight float64
	// Depth is the pocket depth: PocketDepth, or PocketDepthShallow when
	// the wall behind the text is too thin for the full pocket.
	Depth float64
	// Width and Height are the text's bounding box in mm at CapHeight.
	Width, Height float64
	// Area is the face's area in mm2; among faces of the same rank, bigger
	// is better.
	Area float64
	// Note names the face for a person: "bed face", "top face", "+x wall",
	// "angled face", with ", smaller text" and ", shallow pocket" appended
	// when a fallback was taken.
	Note string

	// tris are the face's triangle indices in the source mesh. Unexported:
	// they are an implementation handle for Cut, not part of the contract.
	tris []int
}

// Placements finds every flat face the text fits on, best first. The bed
// face outranks everything, then faces pointing up or sideways, then
// downward overhangs; within a rank, full-size text beats the small
// fallback, faces of at least LargeFace beat smaller ones, and bigger area
// wins. A face joins the list only if the wall behind the text is thick
// enough for a pocket at all. An empty result is an answer, not an error:
// the part is too small to carry the text.
func Placements(m Mesh, text string, f *Font, opts Options) ([]Placement, error) {
	if f == nil {
		return nil, fmt.Errorf("engrave: nil font")
	}
	outlines := map[float64]*TextOutline{}
	for _, cap := range []float64{CapHeight, CapHeightSmall} {
		o, err := f.Outlines(text, cap)
		if err != nil {
			return nil, err
		}
		outlines[cap] = o
	}

	w := weld(m)
	type candidate struct {
		p        Placement
		rank     int // 0 bed, 1 up or sideways, 2 downward overhang
		small    int // 1 when the small cap was the one that fit
		tier     int // 0 for a face of at least LargeFace, 2 below
		facetIdx int
	}
	var found []candidate
	for fi, fc := range facets(m, w) {
		baseU, baseV := faceFrame(fc.normal)
		// The ladder: full size upright, full size sideways, then the small
		// size both ways. The first rung that fits geometrically is the one;
		// the wall check afterwards decides depth or rejects the face.
		type rung struct {
			cap     float64
			rotated bool
		}
		var hit *Placement
		for _, r := range []rung{{CapHeight, false}, {CapHeight, true}, {CapHeightSmall, false}, {CapHeightSmall, true}} {
			u, v := baseU, baseV
			if r.rotated {
				// Rotating the text +90 degrees in the face is the same as
				// rotating the frame: the baseline runs up the old V and the
				// caps grow along -U, and U x V still equals the normal.
				u, v = baseV, baseU.Mul(-1)
			}
			o := outlines[r.cap]
			at, ok := placeOnFacet(m, w, &fc, u, v, o, margin(r.cap))
			if !ok {
				continue
			}
			origin := u.Mul(at.X).Add(v.Mul(at.Y)).Add(fc.normal.Mul(fc.offset))
			hit = &Placement{
				Origin: origin, U: u, V: v, Normal: fc.normal,
				CapHeight: r.cap,
				Width:     o.Max.X - o.Min.X, Height: o.Max.Y - o.Min.Y,
				Area: fc.area,
				tris: fc.tris,
			}
			break
		}
		if hit == nil {
			continue
		}

		// The pocket: three layers when the wall allows, two on a thin
		// sheet, nothing at all into a wall that cannot spare two. The hair
		// of tolerance is because a 1.00 mm sheet ray-casts to 0.9999.
		wall := wallThickness(m, hit, outlines[hit.CapHeight])
		switch {
		case wall >= MinWall-0.01:
			hit.Depth = PocketDepth
		case wall >= MinWallShallow-0.01:
			hit.Depth = PocketDepthShallow
		default:
			continue
		}

		hit.Note = fc.label
		if hit.CapHeight < CapHeight {
			hit.Note += ", smaller text"
		}
		if hit.Depth < PocketDepth {
			hit.Note += ", shallow pocket"
		}

		rank := 2
		if fc.isBed {
			rank = 0
		} else if fc.normal.Z >= -0.2 {
			rank = 1
		}
		tier := 2
		if fc.area >= LargeFace {
			tier = 0
		}
		small := 0
		if hit.CapHeight < CapHeight {
			small = 1
		}
		found = append(found, candidate{p: *hit, rank: rank, small: small, tier: tier, facetIdx: fi})
	}

	slices.SortStableFunc(found, func(a, b candidate) int {
		if a.rank != b.rank {
			return a.rank - b.rank
		}
		if a.small != b.small {
			return a.small - b.small
		}
		if a.tier != b.tier {
			return a.tier - b.tier
		}
		if a.p.Area != b.p.Area {
			if a.p.Area > b.p.Area {
				return -1
			}
			return 1
		}
		return a.facetIdx - b.facetIdx
	})

	out := make([]Placement, 0, len(found))
	for _, c := range found {
		out = append(out, c.p)
		if opts.MaxPlacements > 0 && len(out) == opts.MaxPlacements {
			break
		}
	}
	return out, nil
}

// placeOnFacet looks for room for the text on one face, in the given text
// frame, and returns the offset of the text's (0,0) in (u, v) coordinates.
//
// The fit test is exact, not sampled: a rectangle lies inside the face's
// triangle union exactly when its centre is inside some triangle and no
// boundary edge of the union crosses it. Corners are tried before the
// middle -- a stamp should sit out of the way -- and the order is fixed so
// the same part always stamps in the same place. A face that is not roughly
// convex can defeat every corner with clear room left, so a fixed raster
// sweeps across it before giving up.
func placeOnFacet(m Mesh, w weldedMesh, fc *facet, u, v Vec3, o *TextOutline, margin float64) (Vec2, bool) {
	// Project the face into this frame. Positions index the welded vertex
	// list so boundary edges can be recognised by id.
	pts := map[int]Vec2{}
	for _, ti := range fc.tris {
		for _, vid := range w.tris[ti] {
			if _, ok := pts[vid]; !ok {
				p := w.verts[vid]
				pts[vid] = Vec2{p.Dot(u), p.Dot(v)}
			}
		}
	}
	// Boundary edges: an undirected welded edge used exactly once within the
	// face.
	edgeCount := map[[2]int]int{}
	for _, ti := range fc.tris {
		t := w.tris[ti]
		for e := range 3 {
			a, b := t[e], t[(e+1)%3]
			if a > b {
				a, b = b, a
			}
			edgeCount[[2]int{a, b}]++
		}
	}
	var boundary [][2]Vec2
	for _, ti := range fc.tris {
		t := w.tris[ti]
		for e := range 3 {
			a, b := t[e], t[(e+1)%3]
			ka, kb := a, b
			if ka > kb {
				ka, kb = kb, ka
			}
			if edgeCount[[2]int{ka, kb}] == 1 {
				boundary = append(boundary, [2]Vec2{pts[a], pts[b]})
			}
		}
	}
	// Triangles in 2D, for the centre-inside test.
	tris2 := make([][3]Vec2, 0, len(fc.tris))
	bbMin, bbMax := Vec2{math.Inf(1), math.Inf(1)}, Vec2{math.Inf(-1), math.Inf(-1)}
	for _, ti := range fc.tris {
		t := w.tris[ti]
		tri := [3]Vec2{pts[t[0]], pts[t[1]], pts[t[2]]}
		tris2 = append(tris2, tri)
		for _, p := range tri {
			bbMin.X, bbMin.Y = math.Min(bbMin.X, p.X), math.Min(bbMin.Y, p.Y)
			bbMax.X, bbMax.Y = math.Max(bbMax.X, p.X), math.Max(bbMax.Y, p.Y)
		}
	}

	tw, th := o.Max.X-o.Min.X, o.Max.Y-o.Min.Y
	// The room the text's lower-left corner may occupy, once the margin and
	// the text's own size are set aside.
	rx0, ry0 := bbMin.X+margin, bbMin.Y+margin
	rx1, ry1 := bbMax.X-margin-tw, bbMax.Y-margin-th
	if rx1 < rx0 || ry1 < ry0 {
		return Vec2{}, false
	}

	fits := func(x, y float64) bool {
		// The text box grown by the margin must clear the face outright.
		gx0, gy0 := x-margin, y-margin
		gx1, gy1 := x+tw+margin, y+th+margin
		cx, cy := (gx0+gx1)/2, (gy0+gy1)/2
		center := false
		for _, tri := range tris2 {
			if pointInTri2(Vec2{cx, cy}, tri) {
				center = true
				break
			}
		}
		if !center {
			return false
		}
		for _, e := range boundary {
			if segIntersectsRect(e[0], e[1], gx0, gy0, gx1, gy1) {
				return false
			}
		}
		return true
	}

	try := func(x, y float64) (Vec2, bool) {
		if fits(x, y) {
			return Vec2{x - o.Min.X, y - o.Min.Y}, true
		}
		return Vec2{}, false
	}
	corners := [][2]float64{
		{rx0, ry0}, {rx1, ry0}, {rx0, ry1}, {rx1, ry1},
		{(rx0 + rx1) / 2, (ry0 + ry1) / 2},
	}
	for _, c := range corners {
		if at, ok := try(c[0], c[1]); ok {
			return at, true
		}
	}
	// The raster step is 0.5 mm, coarsened on a face so large that half a
	// millimetre would mean an unbounded search; determinism only needs the
	// step to be a pure function of the face.
	step := 0.5
	if span := math.Max(rx1-rx0, ry1-ry0); span > 100 {
		step = span / 200
	}
	for j := 0; j <= int((ry1-ry0)/step); j++ {
		for i := 0; i <= int((rx1-rx0)/step); i++ {
			if at, ok := try(rx0+float64(i)*step, ry0+float64(j)*step); ok {
				return at, true
			}
		}
	}
	return Vec2{}, false
}

func pointInTri2(p Vec2, t [3]Vec2) bool {
	const eps = 1e-9
	d0 := cross2(t[0], t[1], p)
	d1 := cross2(t[1], t[2], p)
	d2 := cross2(t[2], t[0], p)
	return (d0 >= -eps && d1 >= -eps && d2 >= -eps) || (d0 <= eps && d1 <= eps && d2 <= eps)
}

// cross2 is the z of (b-a) x (p-a): positive when p is left of a->b.
func cross2(a, b, p Vec2) float64 {
	return (b.X-a.X)*(p.Y-a.Y) - (b.Y-a.Y)*(p.X-a.X)
}

// segIntersectsRect reports whether the segment meets the closed rectangle,
// by Liang-Barsky clipping.
func segIntersectsRect(a, b Vec2, x0, y0, x1, y1 float64) bool {
	dx, dy := b.X-a.X, b.Y-a.Y
	t0, t1 := 0.0, 1.0
	clip := func(p, q float64) bool {
		if p == 0 {
			return q >= 0
		}
		r := q / p
		if p < 0 {
			if r > t1 {
				return false
			}
			if r > t0 {
				t0 = r
			}
		} else {
			if r < t0 {
				return false
			}
			if r < t1 {
				t1 = r
			}
		}
		return true
	}
	return clip(-dx, a.X-x0) && clip(dx, x1-a.X) &&
		clip(-dy, a.Y-y0) && clip(dy, y1-a.Y) && t0 <= t1
}

// wallThickness is the material behind the text box: the nearest surface
// behind each of its corners and its centre, looking straight in. A pocket
// into a wall thinner than the minimum leaves too little to print.
func wallThickness(m Mesh, p *Placement, o *TextOutline) float64 {
	// Start each ray a nose inside the face so the face itself is not the
	// first hit, and add that nose back to the distance.
	const inset = 0.05
	pts := [][2]float64{
		{(o.Min.X + o.Max.X) / 2, (o.Min.Y + o.Max.Y) / 2},
		{o.Min.X, o.Min.Y}, {o.Max.X, o.Min.Y}, {o.Min.X, o.Max.Y}, {o.Max.X, o.Max.Y},
	}
	dir := p.Normal.Mul(-1)
	thickness := math.Inf(1)
	for _, q := range pts {
		surface := p.Origin.Add(p.U.Mul(q[0])).Add(p.V.Mul(q[1]))
		origin := surface.Add(dir.Mul(inset))
		t, hit := nearestHit(m, origin, dir)
		if !hit {
			return 0 // a ray escaped: this is not the inside of a wall
		}
		thickness = math.Min(thickness, t+inset)
	}
	return thickness
}

// nearestHit is a straightforward Moller-Trumbore sweep over every triangle.
// Five rays a face over meshes this size does not earn an acceleration
// structure.
func nearestHit(m Mesh, origin, dir Vec3) (float64, bool) {
	const eps = 1e-9
	best := math.Inf(1)
	hit := false
	for _, tr := range m.Triangles {
		e1 := tr[1].Sub(tr[0])
		e2 := tr[2].Sub(tr[0])
		p := dir.Cross(e2)
		det := e1.Dot(p)
		if math.Abs(det) < eps {
			continue
		}
		inv := 1 / det
		s := origin.Sub(tr[0])
		u := s.Dot(p) * inv
		if u < -1e-9 || u > 1+1e-9 {
			continue
		}
		q := s.Cross(e1)
		v := dir.Dot(q) * inv
		if v < -1e-9 || u+v > 1+1e-9 {
			continue
		}
		t := e2.Dot(q) * inv
		if t > 1e-6 && t < best {
			best = t
			hit = true
		}
	}
	return best, hit
}
