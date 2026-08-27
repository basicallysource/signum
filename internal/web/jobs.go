package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"

	"github.com/basicallysource/signum/internal/store"
)

// A print event is worth a page of its own: one plate through one printer,
// the parts that were on it, and the slicer's whole testimony -- with the
// settings a person actually changed made impossible to miss.

// commonSettings are the ones people actually reach for, surfaced above the
// alphabetical rest.
var commonSettings = []string{
	"sparse_infill_density",
	"sparse_infill_pattern",
	"wall_loops",
	"layer_height",
	"filament_type",
	"nozzle_temperature",
	"hot_plate_temp",
	"textured_plate_temp",
	"enable_support",
	"support_type",
	"brim_type",
	"top_shell_layers",
	"bottom_shell_layers",
	"line_width",
	"default_print_speed",
}

type settingRow struct {
	Key     string
	Value   string
	Changed bool
}

// sliceDoc is the slicer document as the job page reads it.
type sliceDoc struct {
	Plate    int               `json:"plate"`
	Settings map[string]string `json:"settings"`
	Changed  []string          `json:"changed"`
}

func (s *Server) jobPage(w http.ResponseWriter, r *http.Request) {
	job, err := s.Store.JobByID(r.Context(), r.PathValue("job"))
	if errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "no such print", nil)
		return
	}
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "could not read the print", err)
		return
	}

	objects, err := s.Store.ObjectsForJob(r.Context(), job.ID)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "could not read the print", err)
		return
	}

	var doc sliceDoc
	var rows []settingRow
	if job.Slice != "" && json.Unmarshal([]byte(job.Slice), &doc) == nil {
		rows = settingRows(doc)
	}

	s.render(w, r, "job.html", map[string]any{
		"Job":      job,
		"Objects":  objects,
		"Plate":    doc.Plate,
		"Settings": rows,
		"Changed":  len(doc.Changed),
	})
}

// settingRows orders the slicer settings for reading: the handful people
// actually tune first, then everything else alphabetically, with the keys
// the slicer says stray from the presets marked wherever they fall.
func settingRows(doc sliceDoc) []settingRow {
	changed := make(map[string]bool, len(doc.Changed))
	for _, key := range doc.Changed {
		changed[key] = true
	}

	rows := make([]settingRow, 0, len(doc.Settings))
	used := map[string]bool{}
	for _, key := range commonSettings {
		if value, ok := doc.Settings[key]; ok {
			rows = append(rows, settingRow{Key: key, Value: value, Changed: changed[key]})
			used[key] = true
		}
	}

	rest := make([]string, 0, len(doc.Settings))
	for key := range doc.Settings {
		if !used[key] {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	for _, key := range rest {
		rows = append(rows, settingRow{Key: key, Value: doc.Settings[key], Changed: changed[key]})
	}
	return rows
}
