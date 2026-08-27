package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/basicallysource/signum/internal/printwatch"
)

// recordJob is what a watch agent posts to. With an identity service
// configured, the agent's bearer token must verify there; without one (the
// desktop app, a server behind its own front door) the endpoint is open.
func (s *Server) recordJob(w http.ResponseWriter, r *http.Request) {
	if s.Identity != "" {
		if _, err := s.verifyBearer(r); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "a live identity token is required"})
			return
		}
	}

	var job printwatch.Job
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&job); err != nil {
		http.Error(w, "send one printwatch job as JSON", http.StatusBadRequest)
		return
	}
	if job.Printer == "" || job.Filename == "" || job.Status == "" {
		http.Error(w, "a job needs printer, filename, and status", http.StatusBadRequest)
		return
	}

	if err := s.Store.RecordJob(r.Context(), job); err != nil {
		s.logger().Error("record job", "error", err)
		http.Error(w, "could not record the job", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// verifyBearer resolves an Authorization header the way verifyToken resolves
// any token.
func (s *Server) verifyBearer(r *http.Request) (Viewer, error) {
	bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || strings.TrimSpace(bearer) == "" {
		return Viewer{}, fmt.Errorf("web: no bearer token")
	}
	return s.verifyToken(r.Context(), strings.TrimSpace(bearer))
}

// verifyToken asks the identity service who a token is. Answers are cached
// for a minute: a printing bed reports many events and a browser loads many
// pages, and revocation within a minute is still revocation.
func (s *Server) verifyToken(ctx context.Context, bearer string) (Viewer, error) {
	s.verified.mu.Lock()
	cached, ok := s.verified.entries[bearer]
	s.verified.mu.Unlock()
	if ok && time.Now().Before(cached.until) {
		return cached.viewer, nil
	}

	viewer, err := whoami(ctx, s.Identity, bearer)
	if err != nil {
		return Viewer{}, err
	}

	s.verified.mu.Lock()
	if s.verified.entries == nil {
		s.verified.entries = make(map[string]verdict)
	}
	if len(s.verified.entries) > 1024 {
		clear(s.verified.entries)
	}
	s.verified.entries[bearer] = verdict{viewer: viewer, until: time.Now().Add(time.Minute)}
	s.verified.mu.Unlock()
	return viewer, nil
}

type verdict struct {
	viewer Viewer
	until  time.Time
}

// verifiedTokens is the cache; it lives on the Server.
type verifiedTokens struct {
	mu      sync.Mutex
	entries map[string]verdict
}

// whoami is the identity service integration, in its entirety.
func whoami(ctx context.Context, base, bearer string) (Viewer, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimSuffix(base, "/")+"/v1/whoami", nil)
	if err != nil {
		return Viewer{}, err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return Viewer{}, fmt.Errorf("web: reach the identity service: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Viewer{}, fmt.Errorf("web: the identity service answered %d", resp.StatusCode)
	}

	var body struct {
		Account string `json:"account"`
		Handle  string `json:"handle"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil || body.Account == "" {
		return Viewer{}, fmt.Errorf("web: unreadable identity answer")
	}
	return Viewer{Account: body.Account, Handle: body.Handle}, nil
}
