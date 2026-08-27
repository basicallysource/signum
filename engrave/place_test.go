package engrave

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestFacetsOfPlate(t *testing.T) {
	m := plate()
	fs := facets(m, weld(m))
	if len(fs) != 6 {
		t.Fatalf("plate resolved to %d facets, want 6", len(fs))
	}
	// Largest first: the two 60x40 faces lead.
	if math.Abs(fs[0].area-2400) > 1e-9 || math.Abs(fs[1].area-2400) > 1e-9 {
		t.Fatalf("largest facets are %v and %v mm2", fs[0].area, fs[1].area)
	}
	beds := 0
	for _, f := range fs {
		if f.isBed {
			beds++
			if f.normal.Z > -NormalTol {
				t.Fatalf("bed facet normal %v", f.normal)
			}
		}
	}
	if beds != 1 {
		t.Fatalf("%d bed facets, want 1", beds)
	}
}

func TestPlacementsOnPlate(t *testing.T) {
	f := testFont(t)
	ps, err := Placements(plate(), "AB12", f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) == 0 {
		t.Fatal("no placements on a 60x40 plate")
	}
	first := ps[0]
	if !strings.Contains(first.Note, "bed face") {
		t.Fatalf("best placement is %q, want the bed face", first.Note)
	}
	if first.CapHeight != CapHeight || first.Depth != PocketDepth {
		t.Fatalf("bed placement took fallbacks: cap %v depth %v", first.CapHeight, first.Depth)
	}
	// The frame must be orthonormal and right-handed onto the normal.
	if d := first.U.Cross(first.V).Sub(first.Normal).Len(); d > 1e-9 {
		t.Fatalf("U x V misses the normal by %v", d)
	}
	if math.Abs(first.U.Dot(first.V)) > 1e-9 {
		t.Fatal("U and V are not perpendicular")
	}
	// The bed is the z = 0 plane, so the text origin must sit on it.
	if math.Abs(first.Origin.Z) > 1e-9 {
		t.Fatalf("bed text origin at z = %v", first.Origin.Z)
	}
	// The second spot should be the equally large top face.
	if len(ps) < 2 || !strings.Contains(ps[1].Note, "top face") {
		t.Fatalf("second placement is %+q, want the top face", ps[1].Note)
	}
	for i := 1; i < len(ps); i++ {
		if ps[i].Note == first.Note {
			t.Fatalf("duplicate placement note %q", first.Note)
		}
	}
}

func TestPlacementsRanking(t *testing.T) {
	f := testFont(t)
	ps, err := Placements(plate(), "A", f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	// A one-letter mark fits everywhere on the plate: all six faces show up,
	// bed first, downward-facing never before sideways or up.
	if len(ps) != 6 {
		t.Fatalf("%d placements, want 6", len(ps))
	}
	if !strings.Contains(ps[0].Note, "bed face") {
		t.Fatalf("first is %q", ps[0].Note)
	}
	for i := 1; i < len(ps); i++ {
		if ps[i].Normal.Z < -0.2 {
			t.Fatalf("placement %d (%q) faces down and is not last", i, ps[i].Note)
		}
	}
}

func TestPlacementsMaxCap(t *testing.T) {
	f := testFont(t)
	ps, err := Placements(plate(), "A", f, Options{MaxPlacements: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 2 {
		t.Fatalf("%d placements, want 2", len(ps))
	}
}

func TestPlacementsThinSheetGoesShallow(t *testing.T) {
	f := testFont(t)
	// 1.2 mm of material: too thin for a 0.6 mm pocket over MinWall, enough
	// for the 0.4 mm fallback over MinWallShallow.
	ps, err := Placements(box(0, 0, 0, 30, 20, 1.2), "AB", f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) == 0 {
		t.Fatal("no placements on the thin sheet")
	}
	for _, p := range ps {
		if p.Depth != PocketDepthShallow {
			t.Fatalf("%q got depth %v on a 1.2 mm sheet", p.Note, p.Depth)
		}
		if !strings.Contains(p.Note, "shallow pocket") {
			t.Fatalf("%q does not say shallow", p.Note)
		}
	}
}

func TestPlacementsNothingFits(t *testing.T) {
	f := testFont(t)
	ps, err := Placements(box(0, 0, 0, 5, 4, 3), "PROTO-1", f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 0 {
		t.Fatalf("%d placements on a part with no room, want none", len(ps))
	}
}

func TestPlacementsDeterministic(t *testing.T) {
	f := testFont(t)
	a, err := Placements(plate(), "AB12", f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Placements(plate(), "AB12", f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatal("two placement runs differ")
	}
}
