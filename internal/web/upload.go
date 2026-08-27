package web

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"strings"

	"github.com/basicallysource/printing-prototype-tracker/internal/store"
)

// uploadLimit bounds one upload request. STLs are big; this is still far
// beyond any real prototype batch.
const uploadLimit = 512 << 20

func (s *Server) uploadPage(w http.ResponseWriter, r *http.Request) {
	project, err := s.Store.ProjectByID(r.Context(), r.PathValue("project"))
	if errors.Is(err, store.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "no such project", nil)
		return
	}
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "could not read the project", err)
		return
	}
	viewer, _ := viewerFrom(r.Context())
	suggestions, _ := s.Store.FieldNamesUsedBy(r.Context(), viewer.Account)
	s.render(w, r, "upload.html", map[string]any{
		"Project":     project,
		"Suggestions": suggestions,
	})
}

// incoming is one STL pulled out of the request, wherever it was nested.
type incoming struct {
	filename string
	content  []byte
}

// upload takes STLs (loose, several, or zipped), one described batch, and
// makes one part per STL: stored source, minted uid, ranked faces, and --
// unless engraving was unchecked entirely -- the engraved copy.
func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	project, err := s.Store.ProjectByID(r.Context(), r.PathValue("project"))
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "no such project", nil)
		return
	}

	if err := r.ParseMultipartForm(64 << 20); err != nil {
		s.fail(w, r, http.StatusBadRequest, "could not read the upload", err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, uploadLimit)

	var stls []incoming
	if r.MultipartForm != nil {
		for _, header := range r.MultipartForm.File["files"] {
			file, err := header.Open()
			if err != nil {
				s.fail(w, r, http.StatusBadRequest, "could not read an uploaded file", err)
				return
			}
			content, err := io.ReadAll(io.LimitReader(file, uploadLimit))
			file.Close()
			if err != nil {
				s.fail(w, r, http.StatusBadRequest, "could not read an uploaded file", err)
				return
			}
			unpacked, err := unpack(header.Filename, content)
			if err != nil {
				s.fail(w, r, http.StatusBadRequest, "could not unpack "+header.Filename, err)
				return
			}
			stls = append(stls, unpacked...)
		}
	}
	if len(stls) == 0 {
		s.fail(w, r, http.StatusBadRequest, "the upload held no STL files", nil)
		return
	}

	fields := collectFields(r)
	lineChoices := engraveChoices{
		UID:      r.FormValue("engrave_uid") != "",
		Filename: r.FormValue("engrave_filename") != "",
		Version:  strings.TrimSpace(r.FormValue("engrave_version_text")),
	}

	var firstUID string
	for _, stl := range stls {
		uid, err := s.createPart(r, project, stl, fields, lineChoices)
		if err != nil {
			s.fail(w, r, http.StatusInternalServerError, "could not save "+stl.filename, err)
			return
		}
		if firstUID == "" {
			firstUID = uid
		}
	}

	if len(stls) == 1 {
		http.Redirect(w, r, "/u/"+firstUID, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/p/"+project.ID, http.StatusSeeOther)
}

// engraveChoices is the checkboxes: which lines go into the part.
type engraveChoices struct {
	UID      bool
	Filename bool
	Version  string
}

func (c engraveChoices) lines(uid, filename string) []string {
	var lines []string
	if c.UID {
		lines = append(lines, uid)
	}
	if c.Filename {
		lines = append(lines, strings.TrimSuffix(filename, path.Ext(filename)))
	}
	if c.Version != "" {
		lines = append(lines, c.Version)
	}
	return lines
}

// createPart stores one STL as a part: blob, row, faces, engraving.
func (s *Server) createPart(r *http.Request, project store.Project, stl incoming,
	fields []store.Field, choices engraveChoices) (string, error) {

	sum, size, err := s.Blobs.Put(bytes.NewReader(stl.content))
	if err != nil {
		return "", err
	}

	viewer, _ := viewerFrom(r.Context())
	name := strings.TrimSuffix(stl.filename, path.Ext(stl.filename))
	part, err := s.Store.CreatePart(r.Context(),
		store.Part{ProjectID: project.ID, Name: name, CreatedBy: viewer.Account},
		[]store.PartFile{{Kind: store.FileSource, Filename: stl.filename, SHA256: sum, Size: size}},
		fields)
	if err != nil {
		return "", err
	}

	lines := choices.lines(part.UID, stl.filename)
	if len(lines) == 0 {
		return part.UID, nil
	}

	// A part whose geometry offers no face is still tracked; it just is not
	// engraved, and the page says so.
	faces, err := s.Engrave.Faces(stl.content, lines)
	if err != nil || len(faces) == 0 {
		s.logger().Info("no engraveable face", "part", part.UID, "file", stl.filename, "error", err)
		return part.UID, nil
	}
	if encoded, err := json.Marshal(faces); err == nil {
		s.Store.SetPlacements(r.Context(), part.UID, string(encoded))
	}

	if err := s.engraveOnto(r, part.UID, stl, lines, faces, 0); err != nil {
		s.logger().Warn("engrave failed", "part", part.UID, "error", err)
	}
	return part.UID, nil
}

// engraveOnto cuts and stores the engraved copy, replacing any previous one.
func (s *Server) engraveOnto(r *http.Request, uid string, stl incoming,
	lines []string, faces []FaceOption, face int) error {

	engraved, err := s.Engrave.Cut(stl.content, lines, face)
	if err != nil {
		return err
	}
	sum, size, err := s.Blobs.Put(bytes.NewReader(engraved))
	if err != nil {
		return err
	}
	if err := s.Store.DeleteFilesOfKind(r.Context(), uid, store.FileEngraved); err != nil {
		return err
	}

	base := strings.TrimSuffix(stl.filename, path.Ext(stl.filename))
	if err := s.Store.AddPartFile(r.Context(), store.PartFile{
		PartUID:  uid,
		Kind:     store.FileEngraved,
		Filename: base + "-" + uid + ".stl",
		SHA256:   sum,
		Size:     size,
	}); err != nil {
		return err
	}

	info := engraveInfo{Lines: lines, Face: face, Note: faces[face].Note}
	encoded, _ := json.Marshal(info)
	return s.Store.SetEngrave(r.Context(), uid, string(encoded))
}

// reEngrave cuts the same lines into a different face.
func (s *Server) reEngrave(w http.ResponseWriter, r *http.Request) {
	part, err := s.Store.PartByUID(r.Context(), r.PathValue("uid"))
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "nothing has that uid", nil)
		return
	}

	var faces []FaceOption
	if json.Unmarshal([]byte(part.Placements), &faces) != nil || len(faces) == 0 {
		s.fail(w, r, http.StatusBadRequest, "this part has no engraveable faces", nil)
		return
	}
	face := 0
	if _, err := parseInt(r.FormValue("face"), &face); err != nil || face < 0 || face >= len(faces) {
		s.fail(w, r, http.StatusBadRequest, "no such face", err)
		return
	}

	var info engraveInfo
	if part.Engrave == "" || json.Unmarshal([]byte(part.Engrave), &info) != nil || len(info.Lines) == 0 {
		s.fail(w, r, http.StatusBadRequest, "this part was saved without engraving", nil)
		return
	}

	source, err := s.sourceFile(r, part.UID)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "could not read the source STL", err)
		return
	}

	if err := s.engraveOnto(r, part.UID, source, info.Lines, faces, face); err != nil {
		s.fail(w, r, http.StatusInternalServerError, "could not engrave that face", err)
		return
	}
	http.Redirect(w, r, "/u/"+part.UID, http.StatusSeeOther)
}

// sourceFile reads a part's original STL back out of the blob store.
func (s *Server) sourceFile(r *http.Request, uid string) (incoming, error) {
	partFiles, err := s.Store.PartFiles(r.Context(), uid)
	if err != nil {
		return incoming{}, err
	}
	for _, file := range partFiles {
		if file.Kind != store.FileSource {
			continue
		}
		content, err := s.Blobs.Open(file.SHA256)
		if err != nil {
			return incoming{}, err
		}
		defer content.Close()
		raw, err := io.ReadAll(content)
		if err != nil {
			return incoming{}, err
		}
		return incoming{filename: file.Filename, content: raw}, nil
	}
	return incoming{}, errors.New("web: the part has no source file")
}

// unpack turns one uploaded file into STLs: an .stl is itself, a .zip is
// searched, anything else is refused by omission.
func unpack(filename string, content []byte) ([]incoming, error) {
	switch strings.ToLower(path.Ext(filename)) {
	case ".stl":
		return []incoming{{filename: path.Base(filename), content: content}}, nil
	case ".zip":
		reader, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
		if err != nil {
			return nil, err
		}
		var stls []incoming
		for _, entry := range reader.File {
			if strings.ToLower(path.Ext(entry.Name)) != ".stl" ||
				strings.HasPrefix(path.Base(entry.Name), ".") {
				continue
			}
			file, err := entry.Open()
			if err != nil {
				return nil, err
			}
			raw, err := io.ReadAll(io.LimitReader(file, uploadLimit))
			file.Close()
			if err != nil {
				return nil, err
			}
			stls = append(stls, incoming{filename: path.Base(entry.Name), content: raw})
		}
		return stls, nil
	}
	return nil, nil
}

// collectFields gathers the built-in and custom fields, in written order.
func collectFields(r *http.Request) []store.Field {
	var fields []store.Field
	add := func(name, value string) {
		if value = strings.TrimSpace(value); value != "" {
			fields = append(fields, store.Field{Name: name, Value: value})
		}
	}
	add("version", r.FormValue("version"))
	add("variant", r.FormValue("variant"))
	add("onshape part studio", r.FormValue("onshape_studio"))
	add("onshape version", r.FormValue("onshape_version"))
	add("notes", r.FormValue("notes"))

	names := r.Form["field_name"]
	values := r.Form["field_value"]
	for i := range names {
		if i < len(values) {
			add(strings.TrimSpace(names[i]), values[i])
		}
	}
	return fields
}

func parseInt(s string, into *int) (bool, error) {
	if s == "" {
		return false, nil
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return false, errors.New("web: not a number")
		}
		n = n*10 + int(r-'0')
	}
	*into = n
	return true, nil
}
