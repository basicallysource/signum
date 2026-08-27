package printwatch

import (
	"context"
	"testing"
	"time"
)

type scripted struct {
	name string
	jobs []Job
}

func (s *scripted) Name() string                            { return s.name }
func (s *scripted) Poll(ctx context.Context) ([]Job, error) { return s.jobs, nil }

type collector struct {
	recorded []Job
}

func (c *collector) Record(ctx context.Context, job Job) error {
	c.recorded = append(c.recorded, job)
	return nil
}

func TestWatcherOnlyReportsNews(t *testing.T) {
	started := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	printer := &scripted{name: "left", jobs: []Job{{
		ExternalID: "1", Filename: "bracket-x7k2p9.stl",
		Status: StatusPrinting, StartedAt: started,
	}}}
	sink := &collector{}
	watcher := &Watcher{Drivers: []Driver{printer}, Sink: sink}

	ctx := context.Background()

	// The same state twice is one report.
	watcher.sweep(ctx)
	watcher.sweep(ctx)
	if len(sink.recorded) != 1 {
		t.Fatalf("expected 1 report, got %d", len(sink.recorded))
	}
	if sink.recorded[0].Printer != "left" {
		t.Fatalf("watcher did not stamp the printer name: %+v", sink.recorded[0])
	}

	// The job finishing is news, once.
	printer.jobs[0].Status = StatusSucceeded
	printer.jobs[0].EndedAt = started.Add(time.Hour)
	watcher.sweep(ctx)
	watcher.sweep(ctx)
	if len(sink.recorded) != 2 {
		t.Fatalf("expected 2 reports, got %d", len(sink.recorded))
	}
	if sink.recorded[1].Status != StatusSucceeded {
		t.Fatalf("second report is %+v", sink.recorded[1])
	}
}

func TestWatcherRetriesFailedRecords(t *testing.T) {
	printer := &scripted{name: "p", jobs: []Job{{
		ExternalID: "1", Filename: "a.stl", Status: StatusPrinting,
		StartedAt: time.Now().UTC(),
	}}}
	sink := &failFirst{}
	watcher := &Watcher{Drivers: []Driver{printer}, Sink: sink}

	watcher.sweep(context.Background())
	watcher.sweep(context.Background())
	if sink.successes != 1 {
		t.Fatalf("expected the failed record to be retried once, got %d successes", sink.successes)
	}
}

type failFirst struct {
	calls     int
	successes int
}

func (f *failFirst) Record(ctx context.Context, job Job) error {
	f.calls++
	if f.calls == 1 {
		return context.DeadlineExceeded
	}
	f.successes++
	return nil
}
