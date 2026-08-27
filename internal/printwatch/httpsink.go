package printwatch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// HTTPSink reports jobs to a signum server. The token is an identity
// service bearer token; the server decides what it may do.
type HTTPSink struct {
	// URL is the server's base, no trailing slash.
	URL   string
	Token string
	HTTP  *http.Client
}

// Record posts one job event.
func (s *HTTPSink) Record(ctx context.Context, job Job) error {
	body, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("printwatch: encode job: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL+"/api/jobs", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.Token != "" {
		req.Header.Set("Authorization", "Bearer "+s.Token)
	}

	client := s.HTTP
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("printwatch: reach the server: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("printwatch: the server answered %d", resp.StatusCode)
	}
	return nil
}
