package web

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/basicallysource/identity/api"
	"github.com/basicallysource/signum/internal/printwatch"
)

// recordJob is what a watch agent posts to. With an identity service
// configured it sits behind a tokenGate; without one (the desktop app, a
// server behind its own front door) the endpoint is open.
func (s *Server) recordJob(w http.ResponseWriter, r *http.Request) {
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

// tokenGate is the machine API's bearer check. It is separate from the
// identity client package that gates the pages on purpose: a watcher holds
// an account token, minted at identity for it and carrying no audience, and
// the client package accepts only tokens handed off to this service, which
// is right for a browser session and would lock every watcher out here. So
// this asks identity directly and accepts a token minted for this service or
// for no service at all. A token handed off to another application is
// refused: that application holds it, and must not be able to act here with
// it.
type tokenGate struct {
	identity *api.ClientWithResponses
	audience string
	logger   *slog.Logger

	mu sync.Mutex
	// verified remembers accepted tokens, by hash, for a minute: a watcher
	// coming back reports a burst of jobs, and revocation within a minute is
	// still revocation. Refusals are never remembered.
	verified map[[sha256.Size]byte]time.Time
}

func newTokenGate(identityURL, audience string, logger *slog.Logger) (*tokenGate, error) {
	identity, err := api.NewClientWithResponses(identityURL,
		api.WithHTTPClient(&http.Client{Timeout: 10 * time.Second}))
	if err != nil {
		return nil, err
	}
	return &tokenGate{
		identity: identity,
		audience: audience,
		logger:   logger,
		verified: map[[sha256.Size]byte]time.Time{},
	}, nil
}

// require answers 401 to a missing or refused token and 503 when identity
// cannot be asked, which says nothing about the token.
func (g *tokenGate) require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		token = strings.TrimSpace(token)
		accepted := false
		if ok && token != "" {
			var err error
			if accepted, err = g.accepts(r.Context(), token); err != nil {
				g.logger.Warn("identity whoami for the jobs API", "error", err)
				apiError(w, http.StatusServiceUnavailable, "the identity service is unavailable; try again shortly")
				return
			}
		}
		if !accepted {
			apiError(w, http.StatusUnauthorized, "a live identity token is required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// accepts reports whether identity vouches for the token here. An error
// means identity could not be asked.
func (g *tokenGate) accepts(ctx context.Context, token string) (bool, error) {
	key := sha256.Sum256([]byte(token))
	g.mu.Lock()
	until, ok := g.verified[key]
	g.mu.Unlock()
	if ok && time.Now().Before(until) {
		return true, nil
	}

	resp, err := g.identity.WhoamiWithResponse(ctx, func(_ context.Context, req *http.Request) error {
		req.Header.Set("Authorization", "Bearer "+token)
		return nil
	})
	if err != nil {
		return false, err
	}
	if resp.StatusCode() == http.StatusUnauthorized {
		return false, nil
	}
	if resp.JSON200 == nil {
		return false, fmt.Errorf("web: the identity service answered %d", resp.StatusCode())
	}
	if audience := resp.JSON200.Token.Audience; audience != "" && audience != g.audience {
		return false, nil
	}

	g.mu.Lock()
	if len(g.verified) > 1024 {
		clear(g.verified)
	}
	g.verified[key] = time.Now().Add(time.Minute)
	g.mu.Unlock()
	return true, nil
}

func apiError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}
