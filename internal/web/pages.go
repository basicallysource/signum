package web

import (
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net/http"
	"strings"

	"github.com/basicallysource/signum/internal/store"
)

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	projects, err := s.Store.ChildProjects(r.Context(), "")
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "could not list projects", err)
		return
	}
	jobs, err := s.Store.RecentJobs(r.Context(), 15)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "could not list jobs", err)
		return
	}
	s.render(w, r, "home.html", map[string]any{
		"Projects": projects,
		"Jobs":     jobs,
		"Error":    r.URL.Query().Get("error"),
	})
}

// lookup is the uid box: type what is engraved on a part, land on the part.
func (s *Server) lookup(w http.ResponseWriter, r *http.Request) {
	uid := strings.ToLower(strings.TrimSpace(r.FormValue("uid")))
	if _, err := s.Store.PartByUID(r.Context(), uid); err != nil {
		http.Redirect(w, r, "/?error=nothing+has+the+uid+%22"+uid+"%22", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/u/"+uid, http.StatusSeeOther)
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	viewer, _ := viewerFrom(r.Context())
	parent := r.FormValue("parent")
	project, err := s.Store.CreateProject(r.Context(), parent, viewer.Account, r.FormValue("name"))
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "could not create the project", err)
		return
	}
	http.Redirect(w, r, "/p/"+project.ID, http.StatusSeeOther)
}

func (s *Server) projectPage(w http.ResponseWriter, r *http.Request) {
	project, err := s.Store.ProjectByID(r.Context(), r.PathValue("project"))
	if errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "no such project", nil)
		return
	}
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "could not read the project", err)
		return
	}
	path, err := s.Store.ProjectPath(r.Context(), project.ID)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "could not read the project", err)
		return
	}
	children, err := s.Store.ChildProjects(r.Context(), project.ID)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "could not list projects", err)
		return
	}
	parts, err := s.Store.PartsInProject(r.Context(), project.ID)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "could not list parts", err)
		return
	}
	s.render(w, r, "project.html", map[string]any{
		"Project":  project,
		"Path":     path,
		"Children": children,
		"Parts":    parts,
	})
}

// engraveInfo is what parts.engrave holds, when an engraving happened:
// Aspects is what the person chose to engrave, Lines is how the engraver
// packed them onto the part.
type engraveInfo struct {
	Aspects []string `json:"aspects,omitempty"`
	Lines   []string `json:"lines"`
	Face    int      `json:"face"`
	Note    string   `json:"note"`
}

// aspects is what a re-engrave feeds back to the engraver; parts recorded
// before aspects existed stored only the packed lines, which are the same
// thing for a single-line mark.
func (i engraveInfo) aspects() []string {
	if len(i.Aspects) > 0 {
		return i.Aspects
	}
	return i.Lines
}

func (s *Server) partPage(w http.ResponseWriter, r *http.Request) {
	part, err := s.Store.PartByUID(r.Context(), r.PathValue("uid"))
	if errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "nothing has that uid", nil)
		return
	}
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "could not read the part", err)
		return
	}

	path, _ := s.Store.ProjectPath(r.Context(), part.ProjectID)
	fields, err := s.Store.PartFields(r.Context(), part.UID)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "could not read the part", err)
		return
	}
	partFiles, err := s.Store.PartFiles(r.Context(), part.UID)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "could not read the part", err)
		return
	}
	jobs, err := s.Store.JobsForPart(r.Context(), part.UID)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "could not read the part", err)
		return
	}

	var faces []FaceOption
	if part.Placements != "" {
		json.Unmarshal([]byte(part.Placements), &faces)
	}
	var engraved *engraveInfo
	if part.Engrave != "" {
		engraved = &engraveInfo{}
		json.Unmarshal([]byte(part.Engrave), engraved)
	}

	s.render(w, r, "part.html", map[string]any{
		"Part":     part,
		"Path":     path,
		"Fields":   fields,
		"Files":    partFiles,
		"Jobs":     jobs,
		"Faces":    faces,
		"Engraved": engraved,
		"Model":    modelPayload(part.UID, partFiles, faces, engraved),
	})
}

// modelPayload is what the 3D viewer needs: which file to show (the
// engraved copy when there is one, since that is the physical part), and
// the mark's frame to highlight. Parts recorded before frames existed have
// zero vectors and simply get no highlight.
func modelPayload(uid string, files []store.PartFile, faces []FaceOption, engraved *engraveInfo) template.JS {
	var shown store.PartFile
	for _, file := range files {
		if file.Kind == store.FileEngraved {
			shown = file
		}
	}
	if shown.ID == "" {
		for _, file := range files {
			if file.Kind == store.FileSource {
				shown = file
				break
			}
		}
	}
	if shown.ID == "" {
		return ""
	}

	payload := map[string]any{"stl": "/u/" + uid + "/file/" + shown.ID}
	if engraved != nil && engraved.Face >= 0 && engraved.Face < len(faces) {
		if face := faces[engraved.Face]; face.U != [3]float64{} {
			payload["highlight"] = face
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return template.JS(raw)
}

func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	file, err := s.Store.PartFileByID(r.Context(), r.PathValue("uid"), r.PathValue("file"))
	if errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "no such file", nil)
		return
	}
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "could not read the file", err)
		return
	}
	content, err := s.Blobs.Open(file.SHA256)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "the stored bytes are missing", err)
		return
	}
	defer content.Close()

	w.Header().Set("Content-Type", "model/stl")
	w.Header().Set("Content-Disposition", `attachment; filename="`+file.Filename+`"`)
	io.Copy(w, content)
}

func (s *Server) printersPage(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.Store.RecentJobs(r.Context(), 100)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "could not list jobs", err)
		return
	}
	s.render(w, r, "printers.html", map[string]any{"Jobs": jobs})
}
