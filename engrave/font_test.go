package engrave

import (
	"math"
	"reflect"
	"testing"
)

// charset is what the package promises to render.
const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-._"

func TestFetchFontVerifiesHash(t *testing.T) {
	path, err := FetchFont("")
	if err != nil {
		t.Skipf("pinned font unavailable: %v", err)
	}
	// A second call must be a cache hit that still verifies.
	again, err := FetchFont("")
	if err != nil {
		t.Fatal(err)
	}
	if again != path {
		t.Fatalf("cache moved: %s then %s", path, again)
	}
}

func TestOutlinesCharset(t *testing.T) {
	f := testFont(t)
	for _, r := range charset {
		o, err := f.Outlines(string(r), CapHeight)
		if err != nil {
			t.Fatalf("%q: %v", r, err)
		}
		for i, l := range o.Loops {
			if len(l.Pts) < 3 {
				t.Fatalf("%q loop %d has %d points", r, i, len(l.Pts))
			}
			area := loopArea(l.Pts)
			if math.Abs(area) < 1e-6 {
				t.Fatalf("%q loop %d is degenerate", r, i)
			}
			// Canonical winding: pocket loops counter-clockwise, islands
			// clockwise.
			if (l.Depth%2 == 0) != (area > 0) {
				t.Fatalf("%q loop %d wound against its depth %d", r, i, l.Depth)
			}
			if (l.Depth == 0) != (l.Parent == -1) {
				t.Fatalf("%q loop %d depth %d but parent %d", r, i, l.Depth, l.Parent)
			}
			for j, p := range l.Pts {
				q := l.Pts[(j+1)%len(l.Pts)]
				if math.Hypot(q.X-p.X, q.Y-p.Y) < 1e-5 {
					t.Fatalf("%q loop %d has a zero-length edge", r, i)
				}
			}
		}
		if o.Area() <= 0 {
			t.Fatalf("%q has non-positive ink area %v", r, o.Area())
		}
	}
}

func TestOutlineCapHeightIsExact(t *testing.T) {
	f := testFont(t)
	o, err := f.Outlines("H", CapHeight)
	if err != nil {
		t.Fatal(err)
	}
	if h := o.Max.Y - o.Min.Y; math.Abs(h-CapHeight) > 1e-9 {
		t.Fatalf("H stands %v mm, want %v", h, CapHeight)
	}
}

func TestOutlineCounters(t *testing.T) {
	f := testFont(t)
	// o is one ring: an outer contour and a counter inside it, which the
	// cut will leave standing as an island.
	o, err := f.Outlines("o", CapHeight)
	if err != nil {
		t.Fatal(err)
	}
	depths := map[int]int{}
	for _, l := range o.Loops {
		depths[l.Depth]++
	}
	if depths[0] != 1 || depths[1] != 1 {
		t.Fatalf("o resolved to depths %v, want one outer and one counter", depths)
	}
	// The dotted zero is the reason this font was pinned: its counter and
	// dot must both survive outline extraction.
	z, err := f.Outlines("0", CapHeight)
	if err != nil {
		t.Fatal(err)
	}
	if len(z.Loops) < 3 {
		t.Fatalf("dotted zero came out as %d loops", len(z.Loops))
	}
}

func TestOutlinesDeterministic(t *testing.T) {
	f := testFont(t)
	a, err := f.Outlines("Ab-0.9_z", CapHeight)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.Outlines("Ab-0.9_z", CapHeight)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatal("two outline runs of the same text differ")
	}
}

func TestOutlinesRejectsMissingGlyph(t *testing.T) {
	f := testFont(t)
	if _, err := f.Outlines("a☃b", CapHeight); err == nil {
		t.Fatal("snowman rendered without error")
	}
}

func TestOutlinesStacksLines(t *testing.T) {
	f := testFont(t)

	one, err := f.Outlines("abc", CapHeight)
	if err != nil {
		t.Fatal(err)
	}
	two, err := f.Outlines("abc\nde", CapHeight)
	if err != nil {
		t.Fatal(err)
	}

	// The block is about as wide as its widest line (bearings differ a hair
	// between lines) and taller by one pitch.
	if two.Max.X-two.Min.X > one.Max.X-one.Min.X+0.5 {
		t.Fatalf("second line widened the block: %v vs %v", two.Max.X-two.Min.X, one.Max.X-one.Min.X)
	}
	wantTaller := CapHeight * linePitch
	oneH := one.Max.Y - one.Min.Y
	twoH := two.Max.Y - two.Min.Y
	if twoH < oneH+wantTaller*0.8 {
		t.Fatalf("two lines are not stacked: height %v vs %v", twoH, oneH)
	}

	// Determinism holds across lines too.
	again, _ := f.Outlines("abc\nde", CapHeight)
	if len(again.Loops) != len(two.Loops) {
		t.Fatalf("multiline layout is not deterministic")
	}
}
