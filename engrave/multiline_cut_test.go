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

// Stacked lines put glyph contours above and below one another, so hole
// bridges chain hole-to-hole and several bridges can land on one vertex.
// That is the regime where bridging through the wrong coincident copy of a
// ring vertex used to self-cross the merged ring and jam ear clipping, in a
// way that depended on the exact layout, not on any one glyph. Cut a wide
// deterministic matrix of stacked texts on every fixture and every offered
// face; each cut must succeed and stay manifold.
func TestCutStackedLinesTorture(t *testing.T) {
	font := testFont(t)

	meshes := []struct {
		name string
		mesh Mesh
	}{
		{"plate", plate()},
		{"gridded plate", griddedPlate()},
		{"holed plate", holedPlate()},
		{"narrow box", box(0, 0, 0, 30, 12, 40)},
	}

	// The texts that first exposed the bug, then every two-line stack of
	// uid-shaped aspect strings: short and long, wide and narrow glyphs,
	// digits, and the letters from the original failures.
	texts := []string{"wlm\nv4", "wlmgha\nv4 left", "wlmgha\nv4"}
	lines := []string{
		"wlm", "v4", "wlmgha", "left", "x7k2p9",
		"g8q", "mmm", "iji", "w4w", "a1b2",
		"zz9", "k5", "70", "ox", "qjy", "vvv",
	}
	for _, a := range lines {
		for _, b := range lines {
			texts = append(texts, a+"\n"+b)
		}
	}

	cuts := 0
	for _, mc := range meshes {
		t.Run(mc.name, func(t *testing.T) {
			for _, text := range texts {
				placements, err := Placements(mc.mesh, text, font, Options{})
				if err != nil {
					t.Fatalf("placements for %q: %v", text, err)
				}
				for i, p := range placements {
					cut, err := Cut(mc.mesh, text, p, font)
					if err != nil {
						t.Fatalf("cut %q on face %d (%s): %v", text, i, p.Note, err)
					}
					func() {
						defer func() {
							if t.Failed() {
								t.Logf("while checking %q on face %d (%s)", text, i, p.Note)
							}
						}()
						checkManifold(t, cut)
					}()
					cuts++
				}
			}
		})
	}
	if cuts == 0 {
		t.Fatal("no cuts were exercised; fixtures are wrong")
	}
	t.Logf("exercised %d cuts across %d texts", cuts, len(texts))
}
