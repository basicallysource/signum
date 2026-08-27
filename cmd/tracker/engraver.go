package main

import (
	"bytes"
	"fmt"
	"strings"
	"sync"

	"github.com/basicallysource/printing-prototype-tracker/engrave"
	"github.com/basicallysource/printing-prototype-tracker/internal/web"
)

// engraver adapts the engrave package to the web layer's seam. The font is
// fetched by pinned hash on first use and kept for the life of the process;
// until something actually engraves, no network is touched, so the app works
// offline for everything else.
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

// oneLine joins the chosen lines into the single line the engraver cuts.
// Stacked multi-line marks are future work in the engrave package.
func oneLine(lines []string) string { return strings.Join(lines, " ") }

// placementCap keeps Faces and Cut looking at the same ranked list, so a
// face index from one is valid in the other.
const placementCap = 8

func (e *engraver) placements(stl []byte, lines []string) ([]engrave.Placement, engrave.Mesh, *engrave.Font, error) {
	font, err := e.load()
	if err != nil {
		return nil, engrave.Mesh{}, nil, err
	}
	mesh, err := engrave.Load(bytes.NewReader(stl))
	if err != nil {
		return nil, engrave.Mesh{}, nil, err
	}
	placements, err := engrave.Placements(mesh, oneLine(lines), font,
		engrave.Options{MaxPlacements: placementCap})
	if err != nil {
		return nil, engrave.Mesh{}, nil, err
	}
	return placements, mesh, font, nil
}

func (e *engraver) Faces(stl []byte, lines []string) ([]web.FaceOption, error) {
	placements, _, _, err := e.placements(stl, lines)
	if err != nil {
		return nil, err
	}
	faces := make([]web.FaceOption, 0, len(placements))
	for _, placement := range placements {
		faces = append(faces, web.FaceOption{
			Note:  placement.Note,
			Cap:   placement.CapHeight,
			Depth: placement.Depth,
		})
	}
	return faces, nil
}

func (e *engraver) Cut(stl []byte, lines []string, face int) ([]byte, error) {
	placements, mesh, font, err := e.placements(stl, lines)
	if err != nil {
		return nil, err
	}
	if face < 0 || face >= len(placements) {
		return nil, fmt.Errorf("engraver: face %d of %d", face, len(placements))
	}
	cut, err := engrave.Cut(mesh, oneLine(lines), placements[face], font)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := cut.WriteBinary(&out); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
