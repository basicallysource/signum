// Package driver holds one file per printer protocol. Real protocols
// (Moonraker, PrusaLink, OctoPrint, Bambu) each become a Driver here; until
// they land, Mock is how the rest of the system is developed and tested.
package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/basicallysource/signum/internal/printwatch"
)

// Mock reads its jobs from a JSON file on every poll: an array of
// printwatch.Job objects. Editing the file is the simulated printer changing
// state, which makes the whole pipeline demonstrable with a text editor.
type Mock struct {
	PrinterName string
	Path        string
}

// Name identifies the simulated printer.
func (m *Mock) Name() string { return m.PrinterName }

// Poll reads the file. A missing file is a printer with nothing to say.
func (m *Mock) Poll(ctx context.Context) ([]printwatch.Job, error) {
	body, err := os.ReadFile(m.Path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("driver: read %s: %w", m.Path, err)
	}
	var jobs []printwatch.Job
	if err := json.Unmarshal(body, &jobs); err != nil {
		return nil, fmt.Errorf("driver: parse %s: %w", m.Path, err)
	}
	return jobs, nil
}
