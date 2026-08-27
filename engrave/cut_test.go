package engrave

import (
	"bytes"
	"math"
	"strings"
	"testing"
)

// cutFixture runs the full pipeline on a mesh and returns input, output and
// the placement used.
func cutFixture(t *testing.T, m Mesh, text string, pick func([]Placement) int) (Mesh, Placement) {
	t.Helper()
	f := testFont(t)
	ps, err := Placements(m, text, f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) == 0 {
		t.Fatal("nothing fits; fixture is wrong")
	}
	i := 0
	if pick != nil {
		i = pick(ps)
	}
	out, err := Cut(m, text, ps[i], f)
	if err != nil {
		t.Fatal(err)
	}
	return out, ps[i]
}

func TestCutPlateIsWatertight(t *testing.T) {
	m := plate()
	out, p := cutFixture(t, m, "PT-01", nil)
	checkManifold(t, out)

	// The pocket is a straight prism of the glyph polygons, so the volume
	// must drop by almost exactly ink area times depth; the band covers
	// float32-free construction error, nothing else.
	f := testFont(t)
	o, err := f.Outlines("PT-01", p.CapHeight)
	if err != nil {
		t.Fatal(err)
	}
	want := o.Area() * p.Depth
	got := m.Volume() - out.Volume()
	if math.Abs(got-want) > want*0.01 {
		t.Fatalf("volume dropped by %v, want about %v", got, want)
	}
	if len(out.Triangles) <= len(m.Triangles) {
		t.Fatalf("cut did not add geometry: %d triangles", len(out.Triangles))
	}
	if len(out.Triangles) > 20000 {
		t.Fatalf("cut exploded to %d triangles", len(out.Triangles))
	}
}

func TestCutCountersBecomeIslands(t *testing.T) {
	// o, a, the dotted zero and a period: every nesting case the charset
	// has, including material standing inside a pocket.
	m := plate()
	out, p := cutFixture(t, m, "o0a.", nil)
	checkManifold(t, out)
	f := testFont(t)
	o, err := f.Outlines("o0a.", p.CapHeight)
	if err != nil {
		t.Fatal(err)
	}
	want := o.Area() * p.Depth
	got := m.Volume() - out.Volume()
	if math.Abs(got-want) > want*0.01 {
		t.Fatalf("volume dropped by %v, want about %v", got, want)
	}
}

func TestCutOnAWall(t *testing.T) {
	// A vertical face exercises the frame logic the bed face cannot: text
	// standing up in world space, pocket cut horizontally.
	m := plate()
	out, p := cutFixture(t, m, "A1", func(ps []Placement) int {
		for i, p := range ps {
			if strings.Contains(p.Note, "wall") {
				return i
			}
		}
		return 0
	})
	if !strings.Contains(p.Note, "wall") {
		t.Skip("no wall took the text")
	}
	checkManifold(t, out)
	if m.Volume() <= out.Volume() {
		t.Fatal("wall cut removed nothing")
	}
}

func TestCutThinSheetShallow(t *testing.T) {
	m := box(0, 0, 0, 30, 20, 1.2)
	out, p := cutFixture(t, m, "AB", nil)
	if p.Depth != PocketDepthShallow {
		t.Fatalf("depth %v on the thin sheet", p.Depth)
	}
	checkManifold(t, out)
	got := m.Volume() - out.Volume()
	f := testFont(t)
	o, err := f.Outlines("AB", p.CapHeight)
	if err != nil {
		t.Fatal(err)
	}
	want := o.Area() * p.Depth
	if math.Abs(got-want) > want*0.01 {
		t.Fatalf("volume dropped by %v, want about %v", got, want)
	}
}

func TestCutTessellatedFace(t *testing.T) {
	// A face made of many small triangles is the case that broke the first
	// bridge-search implementation: glyph edges lie exactly on the cap line
	// and baseline, and the rectangle lands differently once interior
	// vertices force it to nudge. Both the fixture and the cut must hold.
	m := griddedPlate()
	checkManifold(t, m)
	out, p := cutFixture(t, m, "GRID-42", func(ps []Placement) int {
		for i, p := range ps {
			if strings.Contains(p.Note, "top face") {
				return i
			}
		}
		return 0
	})
	if !strings.Contains(p.Note, "top face") {
		t.Fatalf("tessellated top not offered; got %q", p.Note)
	}
	checkManifold(t, out)
	f := testFont(t)
	o, err := f.Outlines("GRID-42", p.CapHeight)
	if err != nil {
		t.Fatal(err)
	}
	want := o.Area() * p.Depth
	got := m.Volume() - out.Volume()
	if math.Abs(got-want) > want*0.01 {
		t.Fatalf("volume dropped by %v, want about %v", got, want)
	}
}

func TestCutFaceWithHole(t *testing.T) {
	// A face whose outline has a hole in it: placement must steer the text
	// around the opening and the cut must survive a facet whose boundary is
	// two loops.
	m := holedPlate()
	checkManifold(t, m)
	out, p := cutFixture(t, m, "0123456789", nil)
	checkManifold(t, out)
	f := testFont(t)
	o, err := f.Outlines("0123456789", p.CapHeight)
	if err != nil {
		t.Fatal(err)
	}
	want := o.Area() * p.Depth
	got := m.Volume() - out.Volume()
	if math.Abs(got-want) > want*0.01 {
		t.Fatalf("volume dropped by %v, want about %v", got, want)
	}
}

func TestCutDeterministic(t *testing.T) {
	f := testFont(t)
	m := plate()
	render := func() []byte {
		ps, err := Placements(m, "PT-01", f, Options{})
		if err != nil {
			t.Fatal(err)
		}
		out, err := Cut(m, "PT-01", ps[0], f)
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := out.WriteBinary(&buf); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	if !bytes.Equal(render(), render()) {
		t.Fatal("the same cut produced different bytes")
	}
}

func TestCutRejectsWrongText(t *testing.T) {
	f := testFont(t)
	m := plate()
	ps, err := Placements(m, "PT-01", f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Cut(m, "PT-02X", ps[0], f); err == nil {
		t.Fatal("cut accepted text the placement never saw")
	}
	if _, err := Cut(m, "PT-01", Placement{}, f); err == nil {
		t.Fatal("cut accepted a zero placement")
	}
}
