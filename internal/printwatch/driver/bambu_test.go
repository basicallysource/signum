package driver

import (
	"testing"
	"time"

	"github.com/basicallysource/signum/internal/printwatch"
)

func at(minute int) time.Time {
	return time.Date(2026, 8, 27, 12, minute, 0, 0, time.UTC)
}

func TestBambuMachineFollowsAPrint(t *testing.T) {
	m := newBambuMachine("p1s")

	// The pushall snapshot: idle, nothing to say.
	m.Apply([]byte(`{"print":{"gcode_state":"IDLE","subtask_name":"","nozzle_temper":24.5,"bed_temper":23.0}}`), at(0))
	if jobs := m.Jobs(); len(jobs) != 0 {
		t.Fatalf("idle printer reported jobs: %+v", jobs)
	}

	// A print starts, LAN mode: no usable task id.
	m.Apply([]byte(`{"print":{"gcode_state":"RUNNING","subtask_name":"bracket-x7k2p9","task_id":"0","total_layer_num":257,"spd_lvl":2,
		"ams":{"tray_now":"1","ams":[{"id":"0","tray":[{"id":"0","tray_type":"PLA","tray_color":"000000FF"},{"id":"1","tray_type":"PETG","tray_color":"22CC77FF"}]}]}}}`), at(1))
	jobs := m.Jobs()
	if len(jobs) != 1 || jobs[0].Status != printwatch.StatusPrinting {
		t.Fatalf("expected one printing job, got %+v", jobs)
	}
	if jobs[0].Filename != "bracket-x7k2p9" {
		t.Fatalf("job filename %q", jobs[0].Filename)
	}
	if jobs[0].Params["filament"] != "PETG #22CC77" {
		t.Fatalf("filament %q", jobs[0].Params["filament"])
	}
	if jobs[0].Params["speed"] != "standard" {
		t.Fatalf("speed %q", jobs[0].Params["speed"])
	}

	// Partial updates keep the same job current.
	m.Apply([]byte(`{"print":{"mc_percent":42,"layer_num":108,"nozzle_temper":220.0,"bed_temper":55.0}}`), at(30))
	jobs = m.Jobs()
	if len(jobs) != 1 {
		t.Fatalf("partials made extra jobs: %+v", jobs)
	}
	if jobs[0].Params["layers"] != "108/257" || jobs[0].Params["percent"] != "42" ||
		jobs[0].Params["nozzle"] != "220C" {
		t.Fatalf("params did not follow: %+v", jobs[0].Params)
	}

	// It finishes.
	m.Apply([]byte(`{"print":{"gcode_state":"FINISH","mc_percent":100}}`), at(55))
	jobs = m.Jobs()
	if jobs[0].Status != printwatch.StatusSucceeded || !jobs[0].EndedAt.Equal(at(55)) {
		t.Fatalf("finish not recorded: %+v", jobs[0])
	}

	// A reprint of the same file is a second job, not a resurrection.
	m.Apply([]byte(`{"print":{"gcode_state":"RUNNING","subtask_name":"bracket-x7k2p9","task_id":"0"}}`), at(58))
	jobs = m.Jobs()
	if len(jobs) != 2 || jobs[1].Status != printwatch.StatusPrinting {
		t.Fatalf("reprint mishandled: %+v", jobs)
	}
	if jobs[0].ExternalID == jobs[1].ExternalID {
		t.Fatalf("two prints share the id %q", jobs[0].ExternalID)
	}
}

func TestBambuMachineFailAndCancel(t *testing.T) {
	m := newBambuMachine("a1")

	m.Apply([]byte(`{"print":{"gcode_state":"RUNNING","subtask_name":"chute","task_id":"991"}}`), at(0))
	m.Apply([]byte(`{"print":{"gcode_state":"FAILED"}}`), at(9))
	if jobs := m.Jobs(); jobs[0].Status != printwatch.StatusFailed {
		t.Fatalf("failure not recorded: %+v", jobs)
	}

	// Printing straight to idle, with no FINISH between, is a cancel.
	m.Apply([]byte(`{"print":{"gcode_state":"RUNNING","subtask_name":"chute2","task_id":"992"}}`), at(10))
	m.Apply([]byte(`{"print":{"gcode_state":"IDLE"}}`), at(12))
	jobs := m.Jobs()
	if len(jobs) != 2 || jobs[1].Status != printwatch.StatusCanceled {
		t.Fatalf("cancel not recorded: %+v", jobs)
	}
}

func TestBambuMachineNameArrivingLate(t *testing.T) {
	m := newBambuMachine("p1p")

	// The state flips to RUNNING before any report mentioned the name.
	m.Apply([]byte(`{"print":{"gcode_state":"RUNNING"}}`), at(0))
	m.Apply([]byte(`{"print":{"subtask_name":"feeder-a2b3c4","gcode_state":"RUNNING"}}`), at(1))
	jobs := m.Jobs()
	if len(jobs) != 1 {
		t.Fatalf("late name split the job: %+v", jobs)
	}
	if jobs[0].Filename != "feeder-a2b3c4" {
		t.Fatalf("name never landed: %+v", jobs[0])
	}

	// A genuinely different print taking over cancels the current one.
	m.Apply([]byte(`{"print":{"gcode_state":"RUNNING","subtask_name":"other-part"}}`), at(2))
	jobs = m.Jobs()
	if len(jobs) != 2 || jobs[0].Status != printwatch.StatusCanceled ||
		jobs[1].Filename != "other-part" {
		t.Fatalf("takeover mishandled: %+v", jobs)
	}
}

func TestBambuMachineIgnoresNoise(t *testing.T) {
	m := newBambuMachine("x")
	m.Apply([]byte(`not json`), at(0))
	m.Apply([]byte(`{"system":{"command":"ledctrl"}}`), at(0))
	m.Apply([]byte(`{"print":{"gcode_state":"IDLE"}}`), at(0))
	if jobs := m.Jobs(); len(jobs) != 0 {
		t.Fatalf("noise made jobs: %+v", jobs)
	}
}
