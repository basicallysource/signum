package engrave

import "testing"

func TestDebugRepro(t *testing.T) {
	font := testFont(t)
	mesh := plate()
	placements, err := Placements(mesh, "wlm\nv4", font, Options{})
	if err != nil || len(placements) == 0 {
		t.Fatalf("no placements: %v", err)
	}
	for i, p := range placements {
		_, err := Cut(mesh, "wlm\nv4", p, font)
		t.Logf("face %d (%s): err=%v", i, p.Note, err)
	}
}
