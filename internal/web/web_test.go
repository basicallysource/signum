package web

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/basicallysource/printing-prototype-tracker/internal/blob"
	"github.com/basicallysource/printing-prototype-tracker/internal/store"
)

// fakeEngraver stamps by appending, which keeps these tests about the web
// layer: the geometry has its own tests in the engrave package.
type fakeEngraver struct{}

func (fakeEngraver) Faces(stl []byte, lines []string) ([]FaceOption, error) {
	return []FaceOption{
		{Note: "bed face", Cap: 3.5, Depth: 0.6},
		{Note: "top face", Cap: 3.5, Depth: 0.6},
	}, nil
}

func (fakeEngraver) Cut(stl []byte, lines []string, face int) ([]byte, error) {
	return append(append([]byte{}, stl...), []byte("\n; engraved "+strings.Join(lines, " "))...), nil
}

func newTestServer(t *testing.T) (*httptest.Server, *Server) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	server := &Server{
		Store:   db,
		Blobs:   blob.Dir{Root: t.TempDir()},
		Engrave: fakeEngraver{},
	}
	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)
	return ts, server
}

// noRedirect keeps 303s visible so tests can follow them deliberately.
var noRedirect = &http.Client{
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

func createProject(t *testing.T, ts *httptest.Server, name string) string {
	t.Helper()
	resp, err := noRedirect.PostForm(ts.URL+"/projects", url.Values{"name": {name}})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	location := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(location, "/p/") {
		t.Fatalf("create project answered %d %q", resp.StatusCode, location)
	}
	return strings.TrimPrefix(location, "/p/")
}

func TestUploadEngraveLookup(t *testing.T) {
	ts, server := newTestServer(t)
	projectID := createProject(t, ts, "sorter")

	// Upload one STL with fields and the uid checkbox on.
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, _ := form.CreateFormFile("files", "bracket.stl")
	io.WriteString(part, "solid bracket\nendsolid bracket\n")
	form.WriteField("version", "v3")
	form.WriteField("engrave_uid", "on")
	form.WriteField("field_name", "infill")
	form.WriteField("field_value", "40%")
	form.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/p/"+projectID+"/upload", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	location := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(location, "/u/") {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("upload answered %d %q\n%s", resp.StatusCode, location, raw)
	}
	uid := strings.TrimPrefix(location, "/u/")
	if !regexp.MustCompile(`^[a-z0-9]{6}$`).MatchString(uid) {
		t.Fatalf("redirected to %q, not a uid", location)
	}

	// The part page shows the fields, both files, and the engraving.
	page := get(t, ts, "/u/"+uid)
	for _, want := range []string{"bracket", "v3", "infill", "bed face",
		"bracket-" + uid + ".stl", uid} {
		if !strings.Contains(page, want) {
			t.Fatalf("part page lacks %q:\n%s", want, page)
		}
	}

	// The engraved download carries the fake engraver's mark.
	files, err := server.Store.PartFiles(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	var engravedID string
	for _, file := range files {
		if file.Kind == store.FileEngraved {
			engravedID = file.ID
		}
	}
	if engravedID == "" {
		t.Fatalf("no engraved file was stored: %+v", files)
	}
	engraved := get(t, ts, "/u/"+uid+"/file/"+engravedID)
	if !strings.Contains(engraved, "; engraved "+uid) {
		t.Fatalf("engraved file lacks the mark:\n%s", engraved)
	}

	// Moving the engraving to another face keeps exactly one engraved file.
	resp, err = noRedirect.PostForm(ts.URL+"/u/"+uid+"/engrave", url.Values{"face": {"1"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("re-engrave answered %d", resp.StatusCode)
	}
	page = get(t, ts, "/u/"+uid)
	if !strings.Contains(page, "top face") {
		t.Fatalf("re-engraved face not shown:\n%s", page)
	}

	// The lookup box lands on the part, forgivingly.
	resp, err = noRedirect.PostForm(ts.URL+"/lookup", url.Values{"uid": {"  " + strings.ToUpper(uid) + " "}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("Location"); got != "/u/"+uid {
		t.Fatalf("lookup went to %q", got)
	}
}

func TestUploadZip(t *testing.T) {
	ts, _ := newTestServer(t)
	projectID := createProject(t, ts, "zipped")

	var zipBody bytes.Buffer
	archive := newZip(t, &zipBody, map[string]string{
		"a.stl":      "solid a\nendsolid a\n",
		"sub/b.stl":  "solid b\nendsolid b\n",
		"notes.txt":  "not a model",
		"._junk.stl": "resource fork noise",
	})
	_ = archive

	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, _ := form.CreateFormFile("files", "batch.zip")
	part.Write(zipBody.Bytes())
	form.WriteField("engrave_uid", "on")
	form.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/p/"+projectID+"/upload", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// Two STLs means back to the project, which lists both parts -- and
	// only two: the txt and the resource-fork noise stayed out.
	if got := resp.Header.Get("Location"); got != "/p/"+projectID {
		t.Fatalf("zip upload went to %q", got)
	}
	page := get(t, ts, "/p/"+projectID)
	if got := strings.Count(page, `href="/u/`); got != 2 {
		t.Fatalf("expected 2 part links, found %d:\n%s", got, page)
	}
}

func TestJobsAPI(t *testing.T) {
	ts, _ := newTestServer(t)

	resp, err := http.Post(ts.URL+"/api/jobs", "application/json",
		strings.NewReader(`{"printer":"left","external_id":"1","filename":"x.stl",
		 "status":"printing","started_at":"2026-01-02T03:04:05Z"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("jobs api answered %d", resp.StatusCode)
	}

	page := get(t, ts, "/printers")
	if !strings.Contains(page, "x.stl") || !strings.Contains(page, "printing") {
		t.Fatalf("printers page lacks the job:\n%s", page)
	}
}

func newZip(t *testing.T, into *bytes.Buffer, entries map[string]string) *bytes.Buffer {
	t.Helper()
	writer := zip.NewWriter(into)
	for name, content := range entries {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(entry, content)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return into
}

func get(t *testing.T, ts *httptest.Server, path string) string {
	t.Helper()
	resp, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s answered %d:\n%s", path, resp.StatusCode, raw)
	}
	return string(raw)
}
