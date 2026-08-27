package web

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"regexp"
	"strings"

	"github.com/basicallysource/signum/internal/store"
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
		Fields:   map[string]bool{},
	}
	for _, name := range r.Form["engrave_field"] {
		lineChoices.Fields[name] = true
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

// engraveChoices is the checkboxes: which aspects go onto the part. Fields
// is checked by field name; the value engraved is the field's value.
type engraveChoices struct {
	UID      bool
	Filename bool
	Fields   map[string]bool
}

// engraveable is what an aspect must look like to be cut: printable ASCII
// (the engraver itself drops anything its font truly lacks), short enough
// to ever fit a face.
var engraveable = regexp.MustCompile(`^[ -~]{1,40}$`)

func (c engraveChoices) aspects(uid, filename string, fields []store.Field) []string {
	var aspects []string
	if c.UID {
		aspects = append(aspects, uid)
	}
	if c.Filename {
		aspects = append(aspects, strings.TrimSuffix(filename, path.Ext(filename)))
	}
	for _, field := range fields {
		if c.Fields[field.Name] && engraveable.MatchString(field.Value) {
			aspects = append(aspects, field.Value)
		}
	}
	// Something un-engraveable that slipped through (a checked URL field, a
	// character the font lacks) is dropped rather than sinking the upload.
	kept := aspects[:0]
	for _, aspect := range aspects {
		if engraveable.MatchString(aspect) {
			kept = append(kept, aspect)
		}
	}
	return kept
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

	aspects := choices.aspects(part.UID, stl.filename, fields)
	if len(aspects) == 0 {
		return part.UID, nil
	}

	// A part whose geometry offers no face is still tracked; it just is not
	// engraved, and the page says so.
	plan, err := s.Engrave.Plan(stl.content, aspects)
	if err != nil || len(plan.Faces) == 0 {
		s.logger().Info("no engraveable face", "part", part.UID, "file", stl.filename, "error", err)
		return part.UID, nil
	}
	if encoded, err := json.Marshal(plan.Faces); err == nil {
		s.Store.SetPlacements(r.Context(), part.UID, string(encoded))
	}

	if err := s.engraveOnto(r, part.UID, stl, aspects, plan, 0); err != nil {
		s.logger().Warn("engrave failed", "part", part.UID, "error", err)
	}
	return part.UID, nil
}

// engraveOnto cuts and stores the engraved copy, replacing any previous one.
func (s *Server) engraveOnto(r *http.Request, uid string, stl incoming,
	aspects []string, plan Plan, face int) error {

	engraved, err := s.Engrave.Cut(stl.content, aspects, face)
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

	info := engraveInfo{Aspects: aspects, Lines: plan.Lines, Face: face, Note: plan.Faces[face].Note}
	encoded, _ := json.Marshal(info)
	return s.Store.SetEngrave(r.Context(), uid, string(encoded))
}

// reEngrave cuts the same aspects into a different face.
func (s *Server) reEngrave(w http.ResponseWriter, r *http.Request) {
	part, err := s.Store.PartByUID(r.Context(), r.PathValue("uid"))
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "nothing has that uid", nil)
		return
	}

	var info engraveInfo
	if part.Engrave == "" || json.Unmarshal([]byte(part.Engrave), &info) != nil || len(info.aspects()) == 0 {
		s.fail(w, r, http.StatusBadRequest, "this part was saved without engraving", nil)
		return
	}

	source, err := s.sourceFile(r, part.UID)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "could not read the source STL", err)
		return
	}

	// Re-planning keeps face indexes honest against the geometry (and
	// refreshes the stored candidates for parts recorded before the frame
	// data existed).
	plan, err := s.Engrave.Plan(source.content, info.aspects())
	if err != nil || len(plan.Faces) == 0 {
		s.fail(w, r, http.StatusBadRequest, "this part has no engraveable faces", err)
		return
	}
	if encoded, err := json.Marshal(plan.Faces); err == nil {
		s.Store.SetPlacements(r.Context(), part.UID, string(encoded))
	}

	face := 0
	if _, err := parseInt(r.FormValue("face"), &face); err != nil || face < 0 || face >= len(plan.Faces) {
		s.fail(w, r, http.StatusBadRequest, "no such face", err)
		return
	}

	if err := s.engraveOnto(r, part.UID, source, info.aspects(), plan, face); err != nil {
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
