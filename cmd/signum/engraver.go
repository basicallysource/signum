package main

import (
	"bytes"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/basicallysource/signum/engrave"
	"github.com/basicallysource/signum/internal/web"
)

// engraver adapts the engrave package to the web layer's seam, and owns the
// packing: the chosen aspects (uid, filename, field values) are grouped into
// up to three stacked lines, and the grouping is picked by what actually
// places best on this mesh -- a long single line that only fits sideways in
// small text loses to two lines at full size on the bed face.
//
// The font is fetched by pinned hash on first use and kept for the life of
// the process; until something actually engraves, no network is touched.
type engraver struct {
	cacheDir string

	once sync.Once
	font *engrave.Font
	err  error
}

func newEngraver(cacheDir string) *engraver {
	return &engraver{cacheDir: cacheDir}
}

func (e *engraver) load() (*engrave.Font, error) {
	e.once.Do(func() {
		path, err := engrave.FetchFont(e.cacheDir)
		if err != nil {
			e.err = err
			return
		}
		e.font, e.err = engrave.LoadFont(path)
	})
	return e.font, e.err
}

// placementCap keeps Plan and Cut looking at the same ranked list, so a
// face index from one is valid in the other.
const placementCap = 8

// maxLines bounds the stack; past three lines a mark reads as a label, not
// a mark.
const maxLines = 3

func (e *engraver) Plan(stl []byte, aspects []string) (web.Plan, error) {
	text, placements, err := e.plan(stl, aspects)
	if err != nil {
		return web.Plan{}, err
	}
	font, _ := e.load()

	plan := web.Plan{Lines: strings.Split(text, "\n")}
	for _, placement := range placements {
		plan.Faces = append(plan.Faces, faceOption(placement, text, font))
	}
	return plan, nil
}

func (e *engraver) Cut(stl []byte, aspects []string, face int) ([]byte, error) {
	text, placements, err := e.plan(stl, aspects)
	if err != nil {
		return nil, err
	}
	if face < 0 || face >= len(placements) {
		return nil, fmt.Errorf("engraver: face %d of %d", face, len(placements))
	}
	font, _ := e.load()
	mesh, err := engrave.Load(bytes.NewReader(stl))
	if err != nil {
		return nil, err
	}
	cut, err := engrave.Cut(mesh, text, placements[face], font)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := cut.WriteBinary(&out); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// plan picks the best packing deterministically: candidates in
// fewest-lines-first order, judged by the best face each one gets. Bigger
// text wins; then a bigger face; a tie keeps the fewer lines.
func (e *engraver) plan(stl []byte, aspects []string) (string, []engrave.Placement, error) {
	font, err := e.load()
	if err != nil {
		return "", nil, err
	}
	mesh, err := engrave.Load(bytes.NewReader(stl))
	if err != nil {
		return "", nil, err
	}

	// An aspect the font cannot render is dropped rather than sinking the
	// whole mark with it.
	renderable := aspects[:0:0]
	for _, aspect := range aspects {
		if _, err := font.Outlines(aspect, engrave.CapHeight); err == nil {
			renderable = append(renderable, aspect)
		}
	}
	aspects = renderable

	type candidate struct {
		text       string
		placements []engrave.Placement
	}
	var candidates []candidate
	for _, text := range packings(aspects, func(s string) float64 {
		outline, err := font.Outlines(s, engrave.CapHeight)
		if err != nil {
			return math.Inf(1)
		}
		return outline.Max.X - outline.Min.X
	}) {
		placements, err := engrave.Placements(mesh, text, font,
			engrave.Options{MaxPlacements: placementCap})
		if err != nil || len(placements) == 0 {
			continue
		}
		candidates = append(candidates, candidate{text, placements})
	}
	// Best text first, best area second, fewer lines breaking ties (the
	// packing order is fewest-lines-first and the sort is stable).
	sort.SliceStable(candidates, func(a, b int) bool {
		pa, pb := candidates[a].placements[0], candidates[b].placements[0]
		if pa.CapHeight != pb.CapHeight {
			return pa.CapHeight > pb.CapHeight
		}
		return pa.Area > pb.Area
	})

	// A layout is only offered if its cut actually completes: rare glyph
	// arrangements can defeat the re-triangulation, and the next-best
	// layout beats an error every time.
	for _, c := range candidates {
		if _, err := engrave.Cut(mesh, c.text, c.placements[0], font); err == nil {
			return c.text, c.placements, nil
		}
	}
	return "", nil, fmt.Errorf("engraver: no face takes the text")
}

// packings turns aspects into candidate texts: for each line count up to
// maxLines, the aspects in order, split into the contiguous groups that
// minimize the widest line. Order and grouping are deterministic, so Plan
// and Cut always agree.
func packings(aspects []string, width func(string) float64) []string {
	if len(aspects) == 0 {
		return nil
	}
	lineOf := func(group []string) string { return strings.Join(group, " ") }

	seen := map[string]bool{}
	var texts []string
	for lines := 1; lines <= min(maxLines, len(aspects)); lines++ {
		grouping := balance(aspects, lines, func(group []string) float64 {
			return width(lineOf(group))
		})
		var parts []string
		for _, group := range grouping {
			parts = append(parts, lineOf(group))
		}
		text := strings.Join(parts, "\n")
		if !seen[text] {
			seen[text] = true
			texts = append(texts, text)
		}
	}
	return texts
}

// balance splits items into k contiguous groups minimizing the widest
// group. The counts are tiny, so it just tries every split.
func balance(items []string, k int, width func([]string) float64) [][]string {
	if k <= 1 || len(items) <= k {
		if k >= len(items) {
			groups := make([][]string, 0, len(items))
			for _, item := range items {
				groups = append(groups, []string{item})
			}
			return groups
		}
		return [][]string{items}
	}

	bestWidest := math.Inf(1)
	var best [][]string
	// First group takes 1..len-k+1 items; recurse for the rest.
	for take := 1; take <= len(items)-(k-1); take++ {
		head := items[:take]
		rest := balance(items[take:], k-1, width)
		widest := width(head)
		for _, group := range rest {
			widest = math.Max(widest, width(group))
		}
		if widest < bestWidest {
			bestWidest = widest
			best = append([][]string{head}, rest...)
		}
	}
	return best
}

// faceOption carries a placement to the web layer, with the text block's
// frame for the viewer's highlight.
func faceOption(p engrave.Placement, text string, font *engrave.Font) web.FaceOption {
	option := web.FaceOption{
		Note:   p.Note,
		Cap:    p.CapHeight,
		Depth:  p.Depth,
		U:      vec(p.U),
		V:      vec(p.V),
		Normal: vec(p.Normal),
		Width:  p.Width,
		Height: p.Height,
	}
	center := p.Origin
	if font != nil {
		if outline, err := font.Outlines(text, p.CapHeight); err == nil {
			cx := (outline.Min.X + outline.Max.X) / 2
			cy := (outline.Min.Y + outline.Max.Y) / 2
			center = p.Origin.Add(p.U.Mul(cx)).Add(p.V.Mul(cy))
		}
	}
	option.Center = vec(center)
	return option
}

func vec(v engrave.Vec3) [3]float64 { return [3]float64{v.X, v.Y, v.Z} }
