package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// With an identity service configured, every page requires being signed in:
// the browser is sent to identity's /authorize, comes back with a one-time
// code, and the code is exchanged server-side for a token this app keeps in
// an HttpOnly cookie. Without one -- the desktop app, a server behind its
// own front door -- there are no sessions and no accounts at all.

const (
	sessionCookie = "signum_session"
	stateCookie   = "signum_state"
	destCookie    = "signum_dest"
	// sessionLife matches the token's own 90-day horizon loosely; the
	// cookie dying first just means signing in again.
	sessionLife = 60 * 24 * time.Hour
)

// Viewer is who is looking at a page.
type Viewer struct {
	Account string
	Handle  string
}

type viewerKey struct{}

func withViewer(ctx context.Context, v Viewer) context.Context {
	return context.WithValue(ctx, viewerKey{}, v)
}

// viewerFrom is the signed-in person, when there is one.
func viewerFrom(ctx context.Context) (Viewer, bool) {
	v, ok := ctx.Value(viewerKey{}).(Viewer)
	return v, ok
}

// requireSession gates the pages. The machine API keeps its own bearer
// check, static assets and the callback must work signed out, and healthz
// answers to anything.
func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/healthz" || path == "/auth/callback" ||
			strings.HasPrefix(path, "/static/") || strings.HasPrefix(path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}

		if cookie, err := r.Cookie(sessionCookie); err == nil {
			if viewer, err := s.verifyToken(r.Context(), cookie.Value); err == nil {
				next.ServeHTTP(w, r.WithContext(withViewer(r.Context(), viewer)))
				return
			}
			// A dead session -- expired, revoked at identity -- is cleared
			// so the sign-in below starts clean.
			http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1})
		}
		s.startSignIn(w, r)
	})
}

// startSignIn sends the browser to identity and pins the round trip with a
// state cookie.
func (s *Server) startSignIn(w http.ResponseWriter, r *http.Request) {
	state, err := randomHex(16)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "could not start sign-in", err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookie,
		Value:    state,
		Path:     "/auth",
		MaxAge:   600,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   strings.HasPrefix(s.BaseURL, "https://"),
	})

	// Remember where the person was going, so signing in lands them there
	// and not back at the front door.
	if dest := r.URL.RequestURI(); r.Method == http.MethodGet &&
		strings.HasPrefix(dest, "/") && !strings.HasPrefix(dest, "//") && len(dest) < 512 {
		http.SetCookie(w, &http.Cookie{
			Name:     destCookie,
			Value:    url.QueryEscape(dest),
			Path:     "/auth",
			MaxAge:   600,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			Secure:   strings.HasPrefix(s.BaseURL, "https://"),
		})
	}

	query := url.Values{
		"redirect_uri": {s.callbackURL()},
		"state":        {state},
	}
	http.Redirect(w, r, s.Identity+"/authorize?"+query.Encode(), http.StatusSeeOther)
}

// authCallback receives the browser back from identity and trades the code
// for this app's own token.
func (s *Server) authCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	cookie, err := r.Cookie(stateCookie)
	if code == "" || state == "" || err != nil || cookie.Value != state {
		s.fail(w, r, http.StatusBadRequest, "this sign-in did not start here; go back and try again", nil)
		return
	}

	token, err := s.exchange(r.Context(), code)
	if err != nil {
		s.fail(w, r, http.StatusBadGateway, "could not complete the sign-in", err)
		return
	}

	http.SetCookie(w, &http.Cookie{Name: stateCookie, Path: "/auth", MaxAge: -1})
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(sessionLife / time.Second),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   strings.HasPrefix(s.BaseURL, "https://"),
	})

	dest := "/"
	if cookie, err := r.Cookie(destCookie); err == nil {
		if unescaped, err := url.QueryUnescape(cookie.Value); err == nil &&
			strings.HasPrefix(unescaped, "/") && !strings.HasPrefix(unescaped, "//") {
			dest = unescaped
		}
		http.SetCookie(w, &http.Cookie{Name: destCookie, Path: "/auth", MaxAge: -1})
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// exchange spends the one-time code at the identity service.
func (s *Server) exchange(ctx context.Context, code string) (string, error) {
	body, _ := json.Marshal(map[string]string{
		"code":         code,
		"redirect_uri": s.callbackURL(),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.Identity+"/v1/exchange", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("web: reach the identity service: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("web: the identity service answered %d to the exchange", resp.StatusCode)
	}

	var minted struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&minted); err != nil || minted.Token == "" {
		return "", fmt.Errorf("web: unreadable exchange answer")
	}
	return minted.Token, nil
}

func (s *Server) callbackURL() string {
	return strings.TrimSuffix(s.BaseURL, "/") + "/auth/callback"
}

func randomHex(bytes int) (string, error) {
	raw := make([]byte, bytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}
