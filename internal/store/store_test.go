package store

import (
	"context"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/basicallysource/signum/internal/printwatch"
)

func open(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestProjectsNest(t *testing.T) {
	db := open(t)
	ctx := context.Background()

	top, err := db.CreateProject(ctx, "", "me", "sorter")
	if err != nil {
		t.Fatal(err)
	}
	sub, err := db.CreateProject(ctx, top.ID, "me", "feeder")
	if err != nil {
		t.Fatal(err)
	}

	roots, err := db.ChildProjects(ctx, "")
	if err != nil || len(roots) != 1 {
		t.Fatalf("roots: %v %v", roots, err)
	}
	children, err := db.ChildProjects(ctx, top.ID)
	if err != nil || len(children) != 1 || children[0].ID != sub.ID {
		t.Fatalf("children: %v %v", children, err)
	}

	path, err := db.ProjectPath(ctx, sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(path) != 2 || path[0].ID != top.ID || path[1].ID != sub.ID {
		t.Fatalf("path: %+v", path)
	}
}

func TestPartsAndFields(t *testing.T) {
	db := open(t)
	ctx := context.Background()

	project, err := db.CreateProject(ctx, "", "me", "sorter")
	if err != nil {
		t.Fatal(err)
	}

	part, err := db.CreatePart(ctx,
		Part{ProjectID: project.ID, Name: "bracket", CreatedBy: "me"},
		[]PartFile{{Kind: FileSource, Filename: "bracket.stl", SHA256: "aa", Size: 10}},
		[]Field{{Name: "version", Value: "v3"}, {Name: "infill", Value: "40%"}})
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[a-z0-9]{6}$`).MatchString(part.UID) {
		t.Fatalf("uid %q is not six base36 characters", part.UID)
	}

	fields, err := db.PartFields(ctx, part.UID)
	if err != nil || len(fields) != 2 || fields[0].Name != "version" {
		t.Fatalf("fields: %v %v", fields, err)
	}

	names, err := db.FieldNamesUsedBy(ctx, "me")
	if err != nil || len(names) != 2 {
		t.Fatalf("suggestions: %v %v", names, err)
	}
	if more, _ := db.FieldNamesUsedBy(ctx, "somebody-else"); len(more) != 0 {
		t.Fatalf("suggestions leaked across people: %v", more)
	}

	// Lookup is case- and whitespace-forgiving, since uids get read off
	// physical parts.
	found, err := db.PartByUID(ctx, "  "+part.UID+" ")
	if err != nil || found.Name != "bracket" {
		t.Fatalf("lookup: %v %v", found, err)
	}
}

func TestUIDNeverAllDigits(t *testing.T) {
	for range 200 {
		uid, err := mintUID()
		if err != nil {
			t.Fatal(err)
		}
		if regexp.MustCompile(`^[0-9]{6}$`).MatchString(uid) {
			t.Fatalf("minted an all-digit uid %q", uid)
		}
	}
}

func TestRecordJobMatchesParts(t *testing.T) {
	db := open(t)
	ctx := context.Background()

	project, _ := db.CreateProject(ctx, "", "me", "sorter")
	part, err := db.CreatePart(ctx,
		Part{ProjectID: project.ID, Name: "bracket", CreatedBy: "me"},
		[]PartFile{{Kind: FileSource, Filename: "bracket.stl", SHA256: "deadbeef", Size: 10}},
		nil)
	if err != nil {
		t.Fatal(err)
	}

	started := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)

	// A hash match ties the job to the part even under a renamed file.
	err = db.RecordJob(ctx, printwatch.Job{
		Printer: "left", ExternalID: "1", Filename: "whatever.stl",
		SHA256: "deadbeef", Status: printwatch.StatusPrinting, StartedAt: started,
	})
	if err != nil {
		t.Fatal(err)
	}

	// The same job finishing updates in place rather than duplicating.
	err = db.RecordJob(ctx, printwatch.Job{
		Printer: "left", ExternalID: "1", Filename: "whatever.stl",
		SHA256: "deadbeef", Status: printwatch.StatusSucceeded,
		StartedAt: started, EndedAt: started.Add(time.Hour),
		Params: map[string]string{"filament": "PETG"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// A filename carrying the uid matches without a hash.
	err = db.RecordJob(ctx, printwatch.Job{
		Printer: "right", ExternalID: "9", Filename: "bracket-" + part.UID + ".stl",
		Status: printwatch.StatusFailed, StartedAt: started.Add(2 * time.Hour),
		EndedAt: started.Add(3 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}

	jobs, err := db.JobsForPart(ctx, part.UID)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs for the part, got %+v", jobs)
	}
	if jobs[0].Status != printwatch.StatusFailed || jobs[1].Status != printwatch.StatusSucceeded {
		t.Fatalf("jobs out of order or not updated: %+v", jobs)
	}
	if jobs[1].Params["filament"] != "PETG" {
		t.Fatalf("params lost: %+v", jobs[1])
	}
}

func TestUIDMatchesHoweverTheNameMutated(t *testing.T) {
	db := open(t)
	ctx := context.Background()

	project, _ := db.CreateProject(ctx, "", "me", "sorter")
	part, err := db.CreatePart(ctx,
		Part{ProjectID: project.ID, Name: "bracket", CreatedBy: "me"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	// A printer reports the job name with no extension; a slicer leaves
	// .gcode.3mf; case wanders. All of them are the same part.
	for i, filename := range []string{
		"bracket-" + part.UID,
		"bracket-" + part.UID + ".gcode.3mf",
		"BRACKET-" + strings.ToUpper(part.UID) + ".STL",
		"bracket-" + part.UID + "_plate_4",
		"camera lamp - bracket-" + part.UID + ".gcode.3mf_plate_12",
	} {
		err := db.RecordJob(ctx, printwatch.Job{
			Printer: "p", ExternalID: strconv.Itoa(i), Filename: filename,
			Status: printwatch.StatusPrinting, StartedAt: time.Now().UTC().Add(time.Duration(i) * time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	jobs, err := db.JobsForPart(ctx, part.UID)
	if err != nil || len(jobs) != 5 {
		t.Fatalf("expected all 5 name shapes to match, got %d (%v)", len(jobs), err)
	}
}

func TestSliceObjectsBecomePartPrintEvents(t *testing.T) {
	db := open(t)
	ctx := context.Background()

	project, _ := db.CreateProject(ctx, "", "me", "lamp")
	arm, _ := db.CreatePart(ctx, Part{ProjectID: project.ID, Name: "arm", CreatedBy: "me"}, nil, nil)

	// The job arrives first with no document: the plate name matches nothing.
	job := printwatch.Job{
		Printer: "a1", ExternalID: "77", Filename: "old project_plate_5",
		Status: printwatch.StatusPrinting, StartedAt: time.Now().UTC(),
	}
	if err := db.RecordJob(ctx, job); err != nil {
		t.Fatal(err)
	}

	// The document lands on a later report: objects appear, and since
	// exactly one resolves to a part, the job adopts it.
	job.Slice = []byte(`{"plate":5,"objects":[
		{"name":"arm-` + arm.UID + `.stl","overrides":{"sparse_infill_density":"40%"}},
		{"name":"stranger.stl"}]}`)
	if err := db.RecordJob(ctx, job); err != nil {
		t.Fatal(err)
	}

	jobs, err := db.JobsForPart(ctx, arm.UID)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("part print events: %v %v", jobs, err)
	}
	if jobs[0].PartUID != arm.UID {
		t.Fatalf("job did not adopt its only tracked part: %+v", jobs[0])
	}

	objects, err := db.ObjectsForJob(ctx, jobs[0].ID)
	if err != nil || len(objects) != 2 {
		t.Fatalf("objects: %v %v", objects, err)
	}
	if objects[0].PartUID != arm.UID || objects[0].Overrides["sparse_infill_density"] != "40%" {
		t.Fatalf("object link/overrides wrong: %+v", objects[0])
	}
	if objects[1].PartUID != "" {
		t.Fatalf("a stranger matched: %+v", objects[1])
	}

	// A re-report replaces, never duplicates, the events.
	if err := db.RecordJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	objects, _ = db.ObjectsForJob(ctx, jobs[0].ID)
	if len(objects) != 2 {
		t.Fatalf("objects duplicated: %d", len(objects))
	}
}
