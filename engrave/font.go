package engrave

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// The pinned font: Source Code Pro Bold, chosen by measurement over every
// monospace font to hand. At the standard cap height its dotted zero is a
// 0.79 mm pocket inside a 0.77 mm ring of standing plastic, both comfortably
// above a 0.4 mm nozzle, so 0 and O print differently; its 1, I and l are
// distinct too. OFL-licensed. The file is never committed here -- binaries
// stay out of git -- it is fetched from this URL and verified against this
// hash, which is also what pins every generated mesh to one set of outlines.
const (
	FontURL    = "https://assets.basically.website/sorter-parts/source-code-pro-bold-b2095e0d657e.ttf"
	FontSHA256 = "b2095e0d657e6d28dc32444a9dacabab0c9241d0bf39d96371756cc9bdbc3a5f"
)

// flattenTol is the largest chord error, mm, allowed when a glyph's curves
// are turned into polygon edges. 0.015 mm is invisible next to a 0.4 mm
// nozzle and keeps contours to a few dozen points each.
const flattenTol = 0.015

// FetchFont downloads the pinned font into cacheDir if it is not already
// there, verifies it against FontSHA256 either way, and returns its path. An
// empty cacheDir means the user cache directory, under
// basically-tracker/fonts. A cached file that fails the hash is refetched,
// so a torn download heals itself on the next call.
func FetchFont(cacheDir string) (string, error) {
	if cacheDir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("engrave: no cache dir: %w", err)
		}
		cacheDir = filepath.Join(base, "basically-tracker", "fonts")
	}
	dest := filepath.Join(cacheDir, filepath.Base(FontURL))
	if data, err := os.ReadFile(dest); err == nil && sha(data) == FontSHA256 {
		return dest, nil
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 60 * time.Second}
	req, err := http.NewRequest("GET", FontURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "printing-prototype-tracker/1 (+https://github.com/basicallysource/printing-prototype-tracker)")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("engrave: fetching font: %s", resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if got := sha(data); got != FontSHA256 {
		return "", fmt.Errorf("engrave: font at %s hashed to %s, expected %s", FontURL, got, FontSHA256)
	}
	// Write-then-rename so a concurrent reader never sees a torn file.
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dest); err != nil {
		return "", err
	}
	return dest, nil
}

func sha(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Font is a parsed font ready to produce text outlines in millimetres. Its
// methods are safe for concurrent use.
type Font struct {
	mu   sync.Mutex
	sf   *sfnt.Font
	buf  sfnt.Buffer
	upem fixed.Int26_6
	// capUnits is the measured height of the H glyph in font units. Scaling
	// by a measured cap rather than the em means "3.5 mm text" is a promise
	// about the printed letterform, not about typographic bookkeeping.
	capUnits float64
}

// LoadFont reads and parses a font file, typically the path FetchFont
// returned.
func LoadFont(path string) (*Font, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseFont(data)
}

// ParseFont parses font bytes. The slice is retained.
func ParseFont(data []byte) (*Font, error) {
	sf, err := sfnt.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("engrave: parsing font: %w", err)
	}
	f := &Font{sf: sf, upem: fixed.I(int(sf.UnitsPerEm()))}
	// Measure the cap height once, from the flattened H itself.
	loops, _, err := f.glyphLoops('H')
	if err != nil {
		return nil, fmt.Errorf("engrave: measuring cap height: %w", err)
	}
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, l := range loops {
		for _, p := range l {
			lo, hi = math.Min(lo, p.Y), math.Max(hi, p.Y)
		}
	}
	if !(hi > lo) {
		return nil, fmt.Errorf("engrave: font has no usable H glyph")
	}
	f.capUnits = hi - lo
	return f, nil
}

// Loop is one closed contour of a text outline, in millimetres, baseline at
// y = 0. The final point connects back to the first. Contours are
// canonically wound: a loop at even depth runs counter-clockwise and bounds
// pocket, a loop at odd depth runs clockwise and bounds standing material
// inside it (the counter of an o or an a, which the cut leaves as a raised
// island in the pocket).
type Loop struct {
	Pts []Vec2
	// Depth is the nesting depth: 0 for an outermost contour, 1 for a
	// counter inside one, and so on.
	Depth int
	// Parent indexes the enclosing loop in TextOutline.Loops, -1 at depth 0.
	Parent int
}

// TextOutline is a line of text as closed polygons, laid out on one
// baseline starting at x = 0.
type TextOutline struct {
	Loops    []Loop
	Min, Max Vec2 // bounding box over every contour point
}

// Area is the net enclosed area, mm2: outer contours count positive, their
// counters negative. This is exactly the footprint a pocket of this text
// removes, which makes it the cross-check for a cut's volume change.
func (o *TextOutline) Area() float64 {
	var a float64
	for _, l := range o.Loops {
		a += loopArea(l.Pts)
	}
	return a
}

// Outlines lays out text at the given cap height and returns its contours.
// Every rune must have a glyph in the font; space contributes only advance.
// Layout is plain advances -- the pinned font is a monospace, so there is no
// kerning to apply.
func (f *Font) Outlines(text string, capHeight float64) (*TextOutline, error) {
	if text == "" {
		return nil, fmt.Errorf("engrave: empty text")
	}
	if capHeight <= 0 {
		return nil, fmt.Errorf("engrave: cap height %v", capHeight)
	}
	scale := capHeight / f.capUnits
	out := &TextOutline{Min: Vec2{math.Inf(1), math.Inf(1)}, Max: Vec2{math.Inf(-1), math.Inf(-1)}}
	penX := 0.0
	for _, r := range text {
		loops, advance, err := f.glyphLoops(r)
		if err != nil {
			return nil, err
		}
		base := len(out.Loops)
		for _, raw := range loops {
			pts := make([]Vec2, 0, len(raw))
			for _, p := range raw {
				pts = append(pts, Vec2{penX + p.X*scale, p.Y * scale})
			}
			pts = dedupeLoop(pts)
			if len(pts) < 3 || math.Abs(loopArea(pts)) < 1e-6 {
				continue // a degenerate contour marks nothing
			}
			out.Loops = append(out.Loops, Loop{Pts: pts, Parent: -1})
		}
		resolveNesting(out.Loops[base:], base)
		for _, l := range out.Loops[base:] {
			for _, p := range l.Pts {
				out.Min.X, out.Min.Y = math.Min(out.Min.X, p.X), math.Min(out.Min.Y, p.Y)
				out.Max.X, out.Max.Y = math.Max(out.Max.X, p.X), math.Max(out.Max.Y, p.Y)
			}
		}
		penX += advance * scale
	}
	if len(out.Loops) == 0 {
		return nil, fmt.Errorf("engrave: text %q has no visible outline", text)
	}
	return out, nil
}

// glyphLoops returns one rune's raw contours in font units, y up, plus its
// advance in font units.
func (f *Font) glyphLoops(r rune) ([][]Vec2, float64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	gi, err := f.sf.GlyphIndex(&f.buf, r)
	if err != nil {
		return nil, 0, fmt.Errorf("engrave: glyph for %q: %w", r, err)
	}
	if gi == 0 {
		return nil, 0, fmt.Errorf("engrave: font has no glyph for %q", r)
	}
	segs, err := f.sf.LoadGlyph(&f.buf, gi, f.upem, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("engrave: outline for %q: %w", r, err)
	}
	adv, err := f.sf.GlyphAdvance(&f.buf, gi, f.upem, font.HintingNone)
	if err != nil {
		return nil, 0, fmt.Errorf("engrave: advance for %q: %w", r, err)
	}
	// Loading at ppem == unitsPerEm makes the fixed-point coordinates font
	// units directly; sfnt hands them out y-down, flipped here once.
	pt := func(p fixed.Point26_6) Vec2 {
		return Vec2{float64(p.X) / 64, -float64(p.Y) / 64}
	}
	var loops [][]Vec2
	var cur []Vec2
	var pos Vec2
	// The flattening tolerance is fixed in millimetres; glyphs are flattened
	// in font units, so convert through the smallest cap height the package
	// will ever render at. One tolerance for every size keeps a glyph's
	// polygonisation identical wherever it appears.
	tol := flattenTol * f.capUnits / CapHeightSmall
	if tol <= 0 {
		// Only while ParseFont is still measuring the H: a bounding box
		// does not need fine flattening.
		tol = 2
	}
	for _, s := range segs {
		switch s.Op {
		case sfnt.SegmentOpMoveTo:
			if len(cur) > 0 {
				loops = append(loops, cur)
			}
			pos = pt(s.Args[0])
			cur = []Vec2{pos}
		case sfnt.SegmentOpLineTo:
			pos = pt(s.Args[0])
			cur = append(cur, pos)
		case sfnt.SegmentOpQuadTo:
			cur = flattenQuad(cur, pos, pt(s.Args[0]), pt(s.Args[1]), tol, 0)
			pos = pt(s.Args[1])
		case sfnt.SegmentOpCubeTo:
			cur = flattenCube(cur, pos, pt(s.Args[0]), pt(s.Args[1]), pt(s.Args[2]), tol, 0)
			pos = pt(s.Args[2])
		}
	}
	if len(cur) > 0 {
		loops = append(loops, cur)
	}
	return loops, float64(adv) / 64, nil
}

// flattenQuad appends a quadratic Bezier to dst as line segments, excluding
// the start point, splitting until the control point sits within tol of the
// chord's midpoint region.
func flattenQuad(dst []Vec2, p0, c, p1 Vec2, tol float64, depth int) []Vec2 {
	// The curve's maximum distance from its chord is at most half the
	// control point's distance from the chord midpoint.
	mx, my := (p0.X+p1.X)/2, (p0.Y+p1.Y)/2
	if depth >= 16 || math.Hypot(c.X-mx, c.Y-my)/2 <= tol {
		return append(dst, p1)
	}
	c0 := Vec2{(p0.X + c.X) / 2, (p0.Y + c.Y) / 2}
	c1 := Vec2{(c.X + p1.X) / 2, (c.Y + p1.Y) / 2}
	mid := Vec2{(c0.X + c1.X) / 2, (c0.Y + c1.Y) / 2}
	dst = flattenQuad(dst, p0, c0, mid, tol, depth+1)
	return flattenQuad(dst, mid, c1, p1, tol, depth+1)
}

// flattenCube is the cubic counterpart of flattenQuad.
func flattenCube(dst []Vec2, p0, c0, c1, p1 Vec2, tol float64, depth int) []Vec2 {
	d0 := math.Hypot(c0.X-(2*p0.X+p1.X)/3, c0.Y-(2*p0.Y+p1.Y)/3)
	d1 := math.Hypot(c1.X-(p0.X+2*p1.X)/3, c1.Y-(p0.Y+2*p1.Y)/3)
	if depth >= 16 || (d0+d1)*3/4 <= tol {
		return append(dst, p1)
	}
	q0 := mid2(p0, c0)
	q1 := mid2(c0, c1)
	q2 := mid2(c1, p1)
	r0 := mid2(q0, q1)
	r1 := mid2(q1, q2)
	m := mid2(r0, r1)
	dst = flattenCube(dst, p0, q0, r0, m, tol, depth+1)
	return flattenCube(dst, m, r1, q2, p1, tol, depth+1)
}

func mid2(a, b Vec2) Vec2 { return Vec2{(a.X + b.X) / 2, (a.Y + b.Y) / 2} }

// dedupeLoop drops repeated points, including a closing point that repeats
// the first, so every remaining edge has real length.
func dedupeLoop(pts []Vec2) []Vec2 {
	const minEdge = 1e-4
	out := pts[:0:0]
	for _, p := range pts {
		if len(out) > 0 && math.Hypot(p.X-out[len(out)-1].X, p.Y-out[len(out)-1].Y) < minEdge {
			continue
		}
		out = append(out, p)
	}
	for len(out) > 1 && math.Hypot(out[0].X-out[len(out)-1].X, out[0].Y-out[len(out)-1].Y) < minEdge {
		out = out[:len(out)-1]
	}
	return out
}

// loopArea is the signed area: positive for counter-clockwise.
func loopArea(pts []Vec2) float64 {
	var a float64
	for i, p := range pts {
		q := pts[(i+1)%len(pts)]
		a += p.X*q.Y - q.X*p.Y
	}
	return a / 2
}

// pointInLoop is the even-odd crossing test.
func pointInLoop(p Vec2, pts []Vec2) bool {
	in := false
	for i, a := range pts {
		b := pts[(i+1)%len(pts)]
		if (a.Y > p.Y) != (b.Y > p.Y) &&
			p.X < a.X+(p.Y-a.Y)/(b.Y-a.Y)*(b.X-a.X) {
			in = !in
		}
	}
	return in
}

// resolveNesting fills in Depth and Parent for one glyph's contours and
// rewinds each to the canonical direction. Fonts disagree about winding --
// TrueType and CFF wind opposite ways -- so direction is derived from
// geometry alone: a contour's depth is how many of its siblings contain it,
// and its parent is the smallest of those.
func resolveNesting(loops []Loop, base int) {
	type info struct {
		depth  int
		parent int
		area   float64
	}
	infos := make([]info, len(loops))
	for i := range loops {
		infos[i] = info{parent: -1, area: math.Abs(loopArea(loops[i].Pts))}
	}
	for i := range loops {
		p := loops[i].Pts[0]
		for j := range loops {
			if i == j || !pointInLoop(p, loops[j].Pts) {
				continue
			}
			infos[i].depth++
			if infos[i].parent < 0 || infos[j].area < infos[infos[i].parent].area {
				infos[i].parent = j
			}
		}
	}
	for i := range loops {
		loops[i].Depth = infos[i].depth
		if infos[i].parent >= 0 {
			loops[i].Parent = base + infos[i].parent
		}
		ccw := loopArea(loops[i].Pts) > 0
		if want := infos[i].depth%2 == 0; ccw != want {
			slices.Reverse(loops[i].Pts)
		}
	}
}
