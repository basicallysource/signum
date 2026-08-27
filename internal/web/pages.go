package web

import (
	"encoding/json"
	"errors"
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

// engraveInfo is what parts.engrave holds, when an engraving happened.
type engraveInfo struct {
	Lines []string `json:"lines"`
	Face  int      `json:"face"`
	Note  string   `json:"note"`
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
	})
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
