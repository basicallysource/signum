package web

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/basicallysource/signum/internal/blob"
	"github.com/basicallysource/signum/internal/store"
)

// fakeIdentity is the identity service as this server meets it: authorize
// for a browser already signed in there, the exchange with its PKCE check,
// whoami, and sign-out.
type fakeIdentity struct {
	*httptest.Server

	mu        sync.Mutex
	app       string            // this server's origin, the one allowed callback
	challenge string            // from the last authorize
	tokens    map[string]string // live token -> audience
	down      bool
	signouts  []string
}

func newFakeIdentity(t *testing.T) *fakeIdentity {
	t.Helper()
	identity := &fakeIdentity{tokens: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /authorize", identity.authorize)
	mux.HandleFunc("POST /v1/exchange", identity.exchange)
	mux.HandleFunc("GET /v1/whoami", identity.whoami)
	mux.HandleFunc("POST /v1/signout", identity.signout)
	identity.Server = httptest.NewServer(mux)
	t.Cleanup(identity.Close)
	return identity
}

func (f *fakeIdentity) mint(token, audience string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens[token] = audience
}

func (f *fakeIdentity) setDown(down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = down
}

func (f *fakeIdentity) authorize(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	query := r.URL.Query()
	if query.Get("redirect_uri") != f.app+"/auth/callback" || query.Get("code_challenge") == "" {
		jsonAnswer(w, http.StatusBadRequest, map[string]string{"error": "bad authorize"})
		return
	}
	f.challenge = query.Get("code_challenge")
	back := url.Values{"code": {"good-code"}, "state": {query.Get("state")}}
	http.Redirect(w, r, query.Get("redirect_uri")+"?"+back.Encode(), http.StatusSeeOther)
}

func (f *fakeIdentity) exchange(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var body struct {
		Code         string `json:"code"`
		RedirectURI  string `json:"redirect_uri"`
		CodeVerifier string `json:"code_verifier"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	sum := sha256.Sum256([]byte(body.CodeVerifier))
	if body.Code != "good-code" || body.RedirectURI != f.app+"/auth/callback" ||
		base64.RawURLEncoding.EncodeToString(sum[:]) != f.challenge {
		jsonAnswer(w, http.StatusForbidden, map[string]string{"error": "bad exchange"})
		return
	}
	f.tokens["bsid_site"] = f.app
	jsonAnswer(w, http.StatusCreated, map[string]string{"token": "bsid_site", "account": "acct1", "handle": "octocat"})
}

func (f *fakeIdentity) whoami(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		jsonAnswer(w, http.StatusBadGateway, map[string]string{"error": "down"})
		return
	}
	audience, ok := f.tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	if !ok {
		jsonAnswer(w, http.StatusUnauthorized, map[string]string{"error": "no"})
		return
	}
	jsonAnswer(w, http.StatusOK, map[string]any{
		"account": "acct1", "handle": "octocat", "created_at": time.Now(),
		"identities": []any{}, "groups": []string{},
		"token": map[string]any{"id": "t1", "name": "token", "audience": audience, "created_at": time.Now()},
	})
}

func (f *fakeIdentity) signout(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.signouts = append(f.signouts, r.Header.Get("Authorization"))
	delete(f.tokens, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	w.WriteHeader(http.StatusNoContent)
}

func jsonAnswer(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

// newSignInServer is a server behind the fake identity. Its handler needs
// its own origin, so the listener comes first.
func newSignInServer(t *testing.T, identity *fakeIdentity) (*httptest.Server, *Server) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	front := http.NewServeMux()
	ts := httptest.NewServer(front)
	t.Cleanup(ts.Close)
	server := &Server{
		Store:      db,
		Blobs:      blob.Dir{Root: t.TempDir()},
		Engrave:    fakeEngraver{},
		Identity:   identity.URL,
		BaseURL:    ts.URL,
		SessionKey: make([]byte, 32),
	}
	handler, err := server.Handler()
	if err != nil {
		t.Fatal(err)
	}
	front.Handle("/", handler)
	identity.mu.Lock()
	identity.app = ts.URL
	identity.mu.Unlock()
	return ts, server
}

// browser keeps cookies and never follows a redirect by itself.
func browser(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar, CheckRedirect: noRedirect.CheckRedirect}
}

func send(t *testing.T, client *http.Client, method, target string, header map[string]string, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Accept", "text/html")
	for name, value := range header {
		req.Header.Set(name, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestPagesSignInThroughIdentity(t *testing.T) {
	identity := newFakeIdentity(t)
	ts, server := newSignInServer(t, identity)
	client := browser(t)

	// Signed out, a page goes to sign in, and sign-in goes to identity with
	// a PKCE challenge and this server's callback.
	resp := send(t, client, http.MethodGet, ts.URL+"/printers", nil, "")
	signin := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(signin, "/auth/signin?") {
		t.Fatalf("signed-out page answered %d -> %q", resp.StatusCode, signin)
	}
	resp = send(t, client, http.MethodGet, ts.URL+signin, nil, "")
	authorize := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(authorize, identity.URL+"/authorize?") {
		t.Fatalf("sign-in answered %d -> %q", resp.StatusCode, authorize)
	}

	// Identity knows the person and sends the browser straight back, which
	// lands on the page it asked for, signed in, with the token sealed.
	resp = send(t, client, http.MethodGet, authorize, nil, "")
	callback := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(callback, ts.URL+"/auth/callback?") {
		t.Fatalf("authorize answered %d -> %q", resp.StatusCode, callback)
	}
	resp = send(t, client, http.MethodGet, callback, nil, "")
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/printers" {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("callback answered %d -> %q\n%s", resp.StatusCode, resp.Header.Get("Location"), raw)
	}
	var session *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "session" && c.Value != "" {
			session = c
		}
	}
	if session == nil || !session.HttpOnly || strings.Contains(session.Value, "bsid_") {
		t.Fatalf("no sealed HttpOnly session cookie: %+v", session)
	}

	// Signed in, the page knows who is looking and offers a way out.
	resp = send(t, client, http.MethodGet, ts.URL+"/printers", nil, "")
	page, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(page), "octocat") ||
		!strings.Contains(string(page), `action="/auth/signout"`) {
		t.Fatalf("signed-in page answered %d:\n%s", resp.StatusCode, page)
	}

	// Work is stamped with the account.
	resp = send(t, client, http.MethodPost, ts.URL+"/projects",
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"},
		url.Values{"name": {"sorter"}}.Encode())
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create project answered %d", resp.StatusCode)
	}
	projects, err := server.Store.ChildProjects(t.Context(), "")
	if err != nil || len(projects) != 1 || projects[0].Owner != "acct1" {
		t.Fatalf("project not stamped with the account: %+v %v", projects, err)
	}

	// The machine API wants its own bearer; the session cookie is not one.
	resp = send(t, client, http.MethodPost, ts.URL+"/api/jobs", nil,
		`{"printer":"p","external_id":"1","filename":"x.stl","status":"printing","started_at":"2026-01-02T03:04:05Z"}`)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("jobs api with only a session cookie answered %d", resp.StatusCode)
	}

	// Signing out ends the sign-in at identity, and the session with it.
	resp = send(t, client, http.MethodPost, ts.URL+"/auth/signout", map[string]string{"Origin": ts.URL}, "")
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		t.Fatalf("sign-out answered %d -> %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	identity.mu.Lock()
	signouts := identity.signouts
	identity.mu.Unlock()
	if len(signouts) != 1 || signouts[0] != "Bearer bsid_site" {
		t.Fatalf("identity was asked to sign out %v", signouts)
	}
	if resp := send(t, client, http.MethodGet, ts.URL+"/printers", nil, ""); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("a page after sign-out answered %d", resp.StatusCode)
	}

	// The old cookie, replayed, is dead too.
	replay := browser(t)
	origin, _ := url.Parse(ts.URL)
	replay.Jar.SetCookies(origin, []*http.Cookie{session})
	resp = send(t, replay, http.MethodGet, ts.URL+"/printers", nil, "")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/auth/signin?") {
		t.Fatalf("a replayed session answered %d -> %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

// The jobs API accepts what it always has: an account token, which is what a
// watcher holds, or a token handed off to this server; never one handed off
// to another application.
func TestJobsAPITokens(t *testing.T) {
	identity := newFakeIdentity(t)
	ts, _ := newSignInServer(t, identity)
	identity.mint("bsid_account", "")
	identity.mint("bsid_site", ts.URL)
	identity.mint("bsid_other", "https://other.example")

	post := func(authorization string) int {
		t.Helper()
		header := map[string]string{"Accept": "application/json"}
		if authorization != "" {
			header["Authorization"] = authorization
		}
		resp := send(t, http.DefaultClient, http.MethodPost, ts.URL+"/api/jobs", header,
			`{"printer":"p","external_id":"1","filename":"x.stl","status":"printing","started_at":"2026-01-02T03:04:05Z"}`)
		return resp.StatusCode
	}
	for authorization, want := range map[string]int{
		"":                     http.StatusUnauthorized,
		"Bearer ":              http.StatusUnauthorized,
		"bsid_account":         http.StatusUnauthorized,
		"Basic bsid_account":   http.StatusUnauthorized,
		"Bearer bsid_unknown":  http.StatusUnauthorized,
		"Bearer bsid_other":    http.StatusUnauthorized,
		"Bearer bsid_account":  http.StatusNoContent,
		"Bearer  bsid_account": http.StatusNoContent,
		"Bearer bsid_site":     http.StatusNoContent,
	} {
		if got := post(authorization); got != want {
			t.Errorf("Authorization %q answered %d, want %d", authorization, got, want)
		}
	}

	// Identity failing says nothing about a token: 503, and the watcher
	// tries again on its next tick.
	identity.mint("bsid_watcher", "")
	identity.setDown(true)
	if got := post("Bearer bsid_watcher"); got != http.StatusServiceUnavailable {
		t.Fatalf("with identity down the jobs api answered %d", got)
	}
	identity.setDown(false)
	if got := post("Bearer bsid_watcher"); got != http.StatusNoContent {
		t.Fatalf("with identity back the jobs api answered %d", got)
	}
}

func TestSignInNeedsASessionKey(t *testing.T) {
	for name, key := range map[string][]byte{"none": nil, "short": make([]byte, 16)} {
		server := &Server{Identity: "https://identity.example", BaseURL: "https://signum.example", SessionKey: key}
		if _, err := server.Handler(); err == nil {
			t.Errorf("%s: a server with an identity service started without a session key", name)
		}
	}
}

func TestOpenServerHasNoSignIn(t *testing.T) {
	ts, _ := newTestServer(t)
	if page := get(t, ts, "/"); strings.Contains(page, "/auth/signout") {
		t.Fatalf("an open server offers sign-out:\n%s", page)
	}
	resp, err := noRedirect.Get(ts.URL + "/auth/signin")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("an open server answered /auth/signin with %d", resp.StatusCode)
	}
}
