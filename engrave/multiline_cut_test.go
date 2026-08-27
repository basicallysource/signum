package engrave

import "testing"

// A stacked mark must cut exactly as well as a single line does.
func TestCutStackedLines(t *testing.T) {
	font := testFont(t)

	cases := []struct {
		name string
		mesh Mesh
		text string
	}{
		{"plate one line", plate(), "x7k2p9"},
		{"plate two lines", plate(), "x7k2p9\nv4"},
		{"plate three lines", plate(), "x7k2p9\nv4\nleft"},
		{"narrow two lines", box(0, 0, 0, 30, 12, 40), "x7k2p9\nv4 left"},
	}
	for _, c := range cases {
		placements, err := Placements(c.mesh, c.text, font, Options{})
		if err != nil || len(placements) == 0 {
			t.Fatalf("%s: no placements (%v)", c.name, err)
		}
		for i, p := range placements {
			cut, err := Cut(c.mesh, c.text, p, font)
			if err != nil {
				t.Fatalf("%s: cut on face %d (%s) failed: %v", c.name, i, p.Note, err)
			}
			checkManifold(t, cut)
		}
	}
}
