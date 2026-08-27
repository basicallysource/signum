package engrave

import (
	"fmt"
	"math"
	"slices"
)

// This file triangulates a polygon with holes by ear clipping: each hole is
// bridged to the outer boundary through a mutually visible vertex pair,
// turning the region into one simple (if self-touching) ring, and ears are
// clipped off that ring one by one.
//
// The textbook way to find a bridge -- shoot a horizontal ray from the
// hole's rightmost vertex -- goes blind here, because glyph contours are
// full of edges lying exactly on the cap line and the baseline, and a ray
// running collinear with an edge sees no crossing at all. The bridge search
// below is instead the closest visible pair: candidate pairs in order of
// length, taking the first whose segment touches no boundary edge anywhere.
// Holes still merge rightmost-first, which is what guarantees a visible
// pair exists: every hole not yet merged lies entirely left of the current
// hole's rightmost vertex, so it cannot obstruct that vertex's view
// rightward.
//
// Everything works on vertex ids with a coordinate lookup, not on raw
// points, because the caller needs the emitted triangles to share ids with
// geometry built elsewhere -- that identity is what makes the final mesh's
// edges pair up exactly.

// triangulateWithHoles triangulates the region inside outer and outside each
// hole. The outer ring must wind counter-clockwise and every hole clockwise;
// emitted triangles wind counter-clockwise.
func triangulateWithHoles(outer []int, holes [][]int, at func(int) Vec2) ([][3]int, error) {
	ring := slices.Clone(outer)
	// Every boundary edge obstructs bridges: the outer ring, every hole
	// whether merged yet or not, and each bridge once built.
	var obstacles [][2]Vec2
	addEdges := func(ids []int) {
		for i := range ids {
			obstacles = append(obstacles, [2]Vec2{at(ids[i]), at(ids[(i+1)%len(ids)])})
		}
	}
	addEdges(outer)
	for _, h := range holes {
		addEdges(h)
	}

	idx := make([]int, len(holes))
	for i := range idx {
		idx[i] = i
	}
	maxX := func(h []int) float64 {
		v := math.Inf(-1)
		for _, id := range h {
			v = math.Max(v, at(id).X)
		}
		return v
	}
	slices.SortStableFunc(idx, func(a, b int) int {
		xa, xb := maxX(holes[a]), maxX(holes[b])
		if xa != xb {
			if xa > xb {
				return -1
			}
			return 1
		}
		return a - b
	})
	for _, hi := range idx {
		var bridge [2]Vec2
		var err error
		ring, bridge, err = bridgeHole(ring, holes[hi], obstacles, at)
		if err != nil {
			return nil, err
		}
		obstacles = append(obstacles, bridge)
	}
	return earClip(ring, at)
}

// bridgeHole splices one hole into the ring through the shortest mutually
// visible vertex pair, and returns the new ring plus the bridge segment for
// the caller's obstacle list.
func bridgeHole(ring, hole []int, obstacles [][2]Vec2, at func(int) Vec2) ([]int, [2]Vec2, error) {
	if len(hole) < 3 {
		return nil, [2]Vec2{}, fmt.Errorf("engrave: degenerate hole")
	}
	type pair struct {
		h, r int
		d2   float64
	}
	pairs := make([]pair, 0, len(hole)*len(ring))
	for h := range hole {
		hp := at(hole[h])
		for r := range ring {
			rp := at(ring[r])
			dx, dy := rp.X-hp.X, rp.Y-hp.Y
			pairs = append(pairs, pair{h, r, dx*dx + dy*dy})
		}
	}
	slices.SortStableFunc(pairs, func(a, b pair) int {
		if a.d2 != b.d2 {
			if a.d2 < b.d2 {
				return -1
			}
			return 1
		}
		if a.h != b.h {
			return a.h - b.h
		}
		return a.r - b.r
	})
	for _, c := range pairs {
		m, p := at(hole[c.h]), at(ring[c.r])
		if m == p {
			continue // a pinch, not a bridge
		}
		// The segment test below cannot tell coincident vertices apart:
		// once earlier bridges have made the ring visit a position more
		// than once, a bridge to that position is sound only through the
		// occurrence whose local interior wedge contains it -- spliced
		// into any other copy it crosses that copy's edges right at the
		// shared point, inside the whisker visible() must exempt, and the
		// ring comes out self-intersecting. Requiring the bridge to head
		// locally into the interior at both endpoints picks the right
		// copy and costs nothing in the ordinary single-copy case.
		if !locallyInside(at(ring[(c.r+len(ring)-1)%len(ring)]), p, at(ring[(c.r+1)%len(ring)]), m) {
			continue
		}
		if !locallyInside(at(hole[(c.h+len(hole)-1)%len(hole)]), m, at(hole[(c.h+1)%len(hole)]), p) {
			continue
		}
		if !visible(m, p, obstacles) {
			continue
		}
		// Splice: ...P, M, hole..., M, P...
		out := make([]int, 0, len(ring)+len(hole)+2)
		out = append(out, ring[:c.r+1]...)
		for k := 0; k <= len(hole); k++ {
			out = append(out, hole[(c.h+k)%len(hole)])
		}
		out = append(out, ring[c.r:]...)
		return out, [2]Vec2{m, p}, nil
	}
	return nil, [2]Vec2{}, fmt.Errorf("engrave: no visible bridge for hole")
}

// locallyInside reports whether a segment leaving v toward q heads strictly
// into the region interior at v, where the boundary runs prev -> v -> next
// with the interior on the left (true along a counter-clockwise outer ring
// and along a clockwise hole alike). At a convex corner the interior wedge
// is the directions left of both incident edges; at a reflex corner, left
// of either. Directions exactly along an edge are boundary, not interior,
// and report false.
func locallyInside(prev, v, next, q Vec2) bool {
	din := v.sub(prev)
	dout := next.sub(v)
	d := q.sub(v)
	li := din.X*d.Y - din.Y*d.X
	lo := dout.X*d.Y - dout.Y*d.X
	if din.X*dout.Y-din.Y*dout.X >= 0 { // convex or straight corner
		return li > 0 && lo > 0
	}
	return li > 0 || lo > 0
}

// visible reports whether the open segment between m and p touches no
// obstacle edge. The segment is shrunk by a whisker at both ends so that
// edges legitimately meeting it at m or p do not count as touches, while a
// collinear overlap running along such an edge still does.
func visible(m, p Vec2, obstacles [][2]Vec2) bool {
	dx, dy := p.X-m.X, p.Y-m.Y
	a := Vec2{m.X + dx*1e-7, m.Y + dy*1e-7}
	b := Vec2{p.X - dx*1e-7, p.Y - dy*1e-7}
	for _, e := range obstacles {
		if segsTouch(a, b, e[0], e[1]) {
			return false
		}
	}
	return true
}

// segsTouch reports whether two closed segments share any point.
func segsTouch(a, b, c, d Vec2) bool {
	const eps = 1e-12
	o1 := cross2(a, b, c)
	o2 := cross2(a, b, d)
	o3 := cross2(c, d, a)
	o4 := cross2(c, d, b)
	if ((o1 > eps && o2 < -eps) || (o1 < -eps && o2 > eps)) &&
		((o3 > eps && o4 < -eps) || (o3 < -eps && o4 > eps)) {
		return true
	}
	on := func(p, q, r Vec2, o float64) bool { // r on segment pq, given collinear-ish o
		return math.Abs(o) <= eps &&
			r.X >= math.Min(p.X, q.X)-eps && r.X <= math.Max(p.X, q.X)+eps &&
			r.Y >= math.Min(p.Y, q.Y)-eps && r.Y <= math.Max(p.Y, q.Y)+eps
	}
	return on(a, b, c, o1) || on(a, b, d, o2) || on(c, d, a, o3) || on(c, d, b, o4)
}

// earClip triangulates one counter-clockwise ring, which may touch itself at
// bridge vertices. Ears must have strictly positive area and contain no
// other ring vertex, on the boundary included -- a vertex sitting exactly on
// a would-be diagonal must block it, or the diagonal would run through
// geometry.
func earClip(ring []int, at func(int) Vec2) ([][3]int, error) {
	n := len(ring)
	if n < 3 {
		return nil, fmt.Errorf("engrave: ring of %d vertices", n)
	}
	next := make([]int, n)
	prev := make([]int, n)
	for i := range n {
		next[i] = (i + 1) % n
		prev[i] = (i + n - 1) % n
	}
	const epsArea = 1e-9

	isEar := func(i int) bool {
		a, b, c := at(ring[prev[i]]), at(ring[i]), at(ring[next[i]])
		if cross2(a, b, c) <= epsArea {
			return false
		}
		// The diagonal a-c must head into the interior at both endpoints.
		// The blocker scan below exempts vertices coincident with a
		// corner (bridge twins), which is sound only while the diagonal
		// stays on this corner's own sheet of the ring: a diagonal
		// leaving through a twin's wedge would cross the twin's edges at
		// the corner itself, where no blocking vertex ever lands inside
		// the triangle to refuse the ear.
		if !locallyInside(at(ring[prev[prev[i]]]), a, b, c) {
			return false
		}
		if !locallyInside(b, c, at(ring[next[next[i]]]), a) {
			return false
		}
		for j := next[next[i]]; j != prev[i]; j = next[j] {
			q := at(ring[j])
			// A ring vertex that merely duplicates a corner (a bridge
			// twin) is the corner, not a blocker.
			if q == a || q == b || q == c {
				continue
			}
			d0 := cross2(a, b, q)
			d1 := cross2(b, c, q)
			d2 := cross2(c, a, q)
			if d0 >= -epsArea && d1 >= -epsArea && d2 >= -epsArea {
				return false
			}
		}
		return true
	}

	var out [][3]int
	remaining := n
	i := 0
	for remaining > 3 {
		found := false
		for scan := 0; scan < remaining; scan++ {
			if isEar(i) {
				out = append(out, [3]int{ring[prev[i]], ring[i], ring[next[i]]})
				next[prev[i]] = next[i]
				prev[next[i]] = prev[i]
				i = prev[i]
				remaining--
				found = true
				break
			}
			i = next[i]
		}
		if !found {
			return nil, fmt.Errorf("engrave: ear clipping stuck with %d vertices left", remaining)
		}
	}
	a, b, c := ring[i], ring[next[i]], ring[next[next[i]]]
	// A last triangle with a repeated point only ever comes from bridge
	// duplicates collapsing; its directed edges cancel in pairs, so dropping
	// it is sound. A distinct-cornered one is real area and must be kept
	// whatever its size, or its boundary edges would go unpaired.
	if pa, pb, pc := at(a), at(b), at(c); pa != pb && pb != pc && pc != pa {
		out = append(out, [3]int{a, b, c})
	}
	return out, nil
}
