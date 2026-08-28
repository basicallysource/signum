package web

import (
	"archive/zip"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/basicallysource/signum/internal/store"
)

// downloadZip streams the selected parts as one archive. Each part
// contributes the file you would actually print: the engraved copy when one
// exists, the source otherwise. The archive is written straight through to
// the response, so a big selection costs memory nothing.
const downloadCap = 500

func (s *Server) downloadZip(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	uids := r.Form["uid"]
	if len(uids) == 0 {
		back := r.FormValue("back")
		if !strings.HasPrefix(back, "/") || strings.HasPrefix(back, "//") {
			back = "/"
		}
		http.Redirect(w, r, back+"?error=nothing+was+selected", http.StatusSeeOther)
		return
	}
	if len(uids) > downloadCap {
		s.fail(w, r, http.StatusBadRequest, "that is too many parts for one archive", nil)
		return
	}

	name := archiveName(r.FormValue("name"))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)

	archive := zip.NewWriter(w)
	used := map[string]bool{}
	for _, uid := range uids {
		part, err := s.Store.PartByUID(r.Context(), uid)
		if err != nil {
			continue // a stale or forged uid is simply not in the archive
		}
		file, ok, err := s.printableFile(r, part.UID)
		if err != nil || !ok {
			continue
		}

		entryName := file.Filename
		if used[entryName] {
			entryName = part.UID + "-" + entryName
		}
		used[entryName] = true

		entry, err := archive.Create(entryName)
		if err != nil {
			break // the response is already committed; stop cleanly
		}
		content, err := s.Blobs.Open(file.SHA256)
		if err != nil {
			s.logger().Error("download: open blob", "part", part.UID, "error", err)
			continue
		}
		_, err = io.Copy(entry, content)
		content.Close()
		if err != nil {
			break
		}
	}
	if err := archive.Close(); err != nil {
		s.logger().Error("download: close archive", "error", err)
	}
}

// printableFile is the file a person means when they say "the part": the
// engraved copy when there is one, else the source.
func (s *Server) printableFile(r *http.Request, uid string) (store.PartFile, bool, error) {
	files, err := s.Store.PartFiles(r.Context(), uid)
	if err != nil {
		return store.PartFile{}, false, err
	}
	var chosen store.PartFile
	for _, file := range files {
		if file.Kind == store.FileEngraved {
			return file, true, nil
		}
		if file.Kind == store.FileSource && chosen.ID == "" {
			chosen = file
		}
	}
	return chosen, chosen.ID != "", nil
}

var unsafeFilename = regexp.MustCompile(`[^A-Za-z0-9._ -]+`)

func archiveName(hint string) string {
	hint = strings.TrimSpace(unsafeFilename.ReplaceAllString(hint, ""))
	if hint == "" {
		hint = "parts"
	}
	return hint + ".zip"
}
