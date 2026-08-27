// Package printwatch turns printers on the local network into a stream of
// job events: this file started printing, it finished, it failed, these were
// the parameters.
//
// The same package is the whole story everywhere it runs. The desktop app
// and the headless agent on a Raspberry Pi are both a Watcher: drivers poll
// printers, and a Sink either writes the local database or posts to a server
// with a token. Two thin faces, one implementation — that is what keeps them
// from drifting apart.
package printwatch

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"
)

// Job is one print as a driver observed it. The zero values mean "not known
// yet": a job appears with a start and no end, and is updated as it runs.
type Job struct {
	// Printer is the configured name of the machine.
	Printer string `json:"printer"`
	// ExternalID is the driver's own id for the job, so updates can be
	// matched across polls. Unique per printer, not globally.
	ExternalID string `json:"external_id"`
	// Filename is what the printer was told to print, as it reports it.
	Filename string `json:"filename"`
	// SHA256 of the printed file when the driver can get the bytes; empty
	// otherwise. This is what makes a match to a tracked part exact.
	SHA256 string `json:"sha256,omitempty"`
	// Status is one of printing, succeeded, failed, canceled.
	Status    string    `json:"status"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitzero"`
	// Params is whatever the printer knows about how it printed: filament,
	// nozzle and bed temperatures, layer height, slicer metadata. Flat
	// string map on purpose; every printer reports differently and this is
	// a record, not a schema.
	Params map[string]string `json:"params,omitempty"`
	// Slice is the slicer document a driver recovered for this job -- the
	// full settings and the objects on the plate -- when its protocol can
	// get one. Opaque JSON here; the server reads it.
	Slice json.RawMessage `json:"slice,omitempty"`
}

// Statuses a Job can carry.
const (
	StatusPrinting  = "printing"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusCanceled  = "canceled"
)

// Done reports whether the job has reached an outcome.
func (j Job) Done() bool { return j.Status != StatusPrinting }

// Driver speaks one printer protocol. Poll returns the jobs as the printer
// currently tells them: the active one and whatever recent history the
// protocol exposes. Implementations live in this package's driver
// subdirectory, one file per protocol.
type Driver interface {
	// Name identifies the printer this driver watches, for Job.Printer.
	Name() string
	Poll(ctx context.Context) ([]Job, error)
}

// Sink is where observed jobs go.
type Sink interface {
	Record(ctx context.Context, job Job) error
}

// Watcher polls every driver on an interval and hands changed jobs to the
// sink. It keeps the last seen state per job so the sink only hears news.
type Watcher struct {
	Drivers  []Driver
	Sink     Sink
	Interval time.Duration
	Logger   *slog.Logger

	seen map[string]snapshot
}

// snapshot is the comparable part of a Job: when nothing here moved, the
// sink has already heard everything worth saying. SliceLen stands in for
// the slice document, whose arrival is the change worth reporting.
type snapshot struct {
	Filename  string
	SHA256    string
	Status    string
	StartedAt time.Time
	EndedAt   time.Time
	SliceLen  int
}

func snapshotOf(j Job) snapshot {
	return snapshot{
		Filename:  j.Filename,
		SHA256:    j.SHA256,
		Status:    j.Status,
		StartedAt: j.StartedAt,
		EndedAt:   j.EndedAt,
		SliceLen:  len(j.Slice),
	}
}

// Run polls until the context ends. Errors are logged and retried on the
// next tick; a flaky printer must never stop the watch.
func (w *Watcher) Run(ctx context.Context) error {
	interval := w.Interval
	if interval <= 0 {
		interval = 10 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		w.sweep(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (w *Watcher) sweep(ctx context.Context) {
	if w.seen == nil {
		w.seen = make(map[string]snapshot)
	}
	for _, driver := range w.Drivers {
		jobs, err := driver.Poll(ctx)
		if err != nil {
			w.logger().Warn("poll failed", "printer", driver.Name(), "error", err)
			continue
		}
		for _, job := range jobs {
			job.Printer = driver.Name()
			key := job.Printer + "\x00" + job.ExternalID
			if previous, ok := w.seen[key]; ok && previous == snapshotOf(job) {
				continue
			}
			if err := w.Sink.Record(ctx, job); err != nil {
				// Not recorded, so not seen: it will be retried next tick.
				w.logger().Warn("record failed", "printer", job.Printer, "job", job.ExternalID, "error", err)
				continue
			}
			w.seen[key] = snapshotOf(job)
			if job.Done() {
				w.logger().Info("job finished", "printer", job.Printer, "file", job.Filename, "status", job.Status)
			}
		}
	}
}

func (w *Watcher) logger() *slog.Logger {
	if w.Logger != nil {
		return w.Logger
	}
	return slog.Default()
}
