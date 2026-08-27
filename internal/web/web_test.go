package web

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/basicallysource/signum/internal/blob"
	"github.com/basicallysource/signum/internal/store"
)

// fakeEngraver stamps by appending, which keeps these tests about the web
// layer: the geometry has its own tests in the engrave package.
type fakeEngraver struct{}

func (fakeEngraver) Plan(stl []byte, aspects []string) (Plan, error) {
	return Plan{
		Faces: []FaceOption{
			{Note: "bed face", Cap: 3.5, Depth: 0.6},
			{Note: "top face", Cap: 3.5, Depth: 0.6},
		},
		Lines: aspects,
	}, nil
}

func (fakeEngraver) Cut(stl []byte, aspects []string, face int) ([]byte, error) {
	return append(append([]byte{}, stl...), []byte("\n; engraved "+strings.Join(aspects, " "))...), nil
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
	form.WriteField("engrave_field", "version")
	form.WriteField("engrave_field", "infill")
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
	if !strings.Contains(engraved, "; engraved "+uid+" v3 40%") {
		t.Fatalf("engraved file lacks the chosen aspects:\n%s", engraved)
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

// fakeIdentity is the identity service boiled down to exchange + whoami.
func fakeIdentity(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/exchange", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Code        string `json:"code"`
			RedirectURI string `json:"redirect_uri"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.Code != "good-code" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"token":"bsid_aa_bb","account":"acct1","handle":"spencer"}`)
	})
	mux.HandleFunc("GET /v1/whoami", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer bsid_aa_bb" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		io.WriteString(w, `{"account":"acct1","handle":"spencer"}`)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func TestPagesRequireSignIn(t *testing.T) {
	identity := fakeIdentity(t)

	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	server := &Server{
		Store:    db,
		Blobs:    blob.Dir{Root: t.TempDir()},
		Engrave:  fakeEngraver{},
		Identity: identity.URL,
	}
	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)
	server.BaseURL = ts.URL

	// Signed out, every page is a redirect to identity's authorize.
	resp, err := noRedirect.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	location := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(location, identity.URL+"/authorize?") {
		t.Fatalf("signed-out home answered %d -> %q", resp.StatusCode, location)
	}
	if !strings.Contains(location, url.QueryEscape(ts.URL+"/auth/callback")) {
		t.Fatalf("authorize redirect lacks the callback: %q", location)
	}
	var state string
	for _, c := range resp.Cookies() {
		if c.Name == stateCookie {
			state = c.Value
		}
	}
	if state == "" {
		t.Fatal("no state cookie was set")
	}

	// A forged state is refused.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/auth/callback?code=good-code&state=forged", nil)
	req.AddCookie(&http.Cookie{Name: stateCookie, Value: state})
	forged, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	forged.Body.Close()
	if forged.StatusCode != http.StatusBadRequest {
		t.Fatalf("forged state answered %d", forged.StatusCode)
	}

	// The real callback trades the code for a session cookie.
	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/auth/callback?code=good-code&state="+state, nil)
	req.AddCookie(&http.Cookie{Name: stateCookie, Value: state})
	back, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	back.Body.Close()
	var session *http.Cookie
	for _, c := range back.Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			session = c
		}
	}
	if back.StatusCode != http.StatusSeeOther || session == nil {
		t.Fatalf("callback answered %d with no session", back.StatusCode)
	}

	// Signed in, the page renders and knows who is looking.
	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/", nil)
	req.AddCookie(session)
	home, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(home.Body)
	home.Body.Close()
	if home.StatusCode != http.StatusOK || !strings.Contains(string(page), "spencer") {
		t.Fatalf("signed-in home answered %d:\n%s", home.StatusCode, page)
	}

	// Work is stamped with the account.
	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/projects",
		strings.NewReader(url.Values{"name": {"sorter"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(session)
	made, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	made.Body.Close()
	if made.StatusCode != http.StatusSeeOther {
		t.Fatalf("create project answered %d", made.StatusCode)
	}
	projects, err := server.Store.ChildProjects(context.Background(), "")
	if err != nil || len(projects) != 1 || projects[0].Owner != "acct1" {
		t.Fatalf("project not stamped with the account: %+v %v", projects, err)
	}

	// The machine API still wants its own bearer, not a cookie.
	jobs, err := http.Post(ts.URL+"/api/jobs", "application/json",
		strings.NewReader(`{"printer":"p","external_id":"1","filename":"x.stl","status":"printing","started_at":"2026-01-02T03:04:05Z"}`))
	if err != nil {
		t.Fatal(err)
	}
	jobs.Body.Close()
	if jobs.StatusCode != http.StatusUnauthorized {
		t.Fatalf("tokenless jobs api answered %d", jobs.StatusCode)
	}
}
