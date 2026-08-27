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

	"github.com/basicallysource/printing-prototype-tracker/internal/printwatch"
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

// verifyBearer asks the identity service who a token is. Answers are cached
// for a minute: a printing bed reports many events, and revocation within a
// minute is still revocation.
func (s *Server) verifyBearer(r *http.Request) (string, error) {
	bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || strings.TrimSpace(bearer) == "" {
		return "", fmt.Errorf("web: no bearer token")
	}
	bearer = strings.TrimSpace(bearer)

	s.verified.mu.Lock()
	cached, ok := s.verified.entries[bearer]
	s.verified.mu.Unlock()
	if ok && time.Now().Before(cached.until) {
		return cached.account, nil
	}

	account, err := whoami(r.Context(), s.Identity, bearer)
	if err != nil {
		return "", err
	}

	s.verified.mu.Lock()
	if s.verified.entries == nil {
		s.verified.entries = make(map[string]verdict)
	}
	if len(s.verified.entries) > 1024 {
		clear(s.verified.entries)
	}
	s.verified.entries[bearer] = verdict{account: account, until: time.Now().Add(time.Minute)}
	s.verified.mu.Unlock()
	return account, nil
}

type verdict struct {
	account string
	until   time.Time
}

// verifiedTokens is the cache; it lives on the Server.
type verifiedTokens struct {
	mu      sync.Mutex
	entries map[string]verdict
}

// whoami is the identity service integration, in its entirety.
func whoami(ctx context.Context, base, bearer string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimSuffix(base, "/")+"/v1/whoami", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("web: reach the identity service: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("web: the identity service answered %d", resp.StatusCode)
	}

	var body struct {
		Account string `json:"account"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil || body.Account == "" {
		return "", fmt.Errorf("web: unreadable identity answer")
	}
	return body.Account, nil
}
