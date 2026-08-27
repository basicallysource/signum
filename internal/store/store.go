// Package store is the database: projects that nest, the parts inside them,
// the files and fields describing each part, and every print job a watcher
// has reported.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/basicallysource/signum/internal/printwatch"
	_ "modernc.org/sqlite"
)

// ErrNotFound is the one answer for anything that isn't there.
var ErrNotFound = errors.New("store: not found")

// Project is a folder: it holds parts and other projects. ParentID empty
// means top level.
type Project struct {
	ID        string
	ParentID  string
	Owner     string
	Name      string
	CreatedAt time.Time
}

// Part is one described upload: a design revision somebody stamped, not the
// bytes of one file.
type Part struct {
	// UID is the six lowercase base36 characters engraved into the part.
	UID       string
	ProjectID string
	Name      string
	CreatedBy string
	CreatedAt time.Time
	// Placements is the ranked candidate faces, as JSON from the engraver,
	// computed once at upload so the picker needs no geometry pass.
	Placements string
	// Engrave records what was cut and where, as JSON.
	Engrave string
}

// Kinds of file a part carries.
const (
	FileSource   = "source"
	FileEngraved = "engraved"
)

// PartFile is one stored file of a part.
type PartFile struct {
	ID        string
	PartUID   string
	Kind      string
	Filename  string
	SHA256    string
	Size      int64
	CreatedAt time.Time
}

// Field is one named fact about a part, in the order the person wrote them.
type Field struct {
	Name  string
	Value string
}

// PrintJob is a job as the database keeps it.
type PrintJob struct {
	ID         string
	Printer    string
	ExternalID string
	Filename   string
	SHA256     string
	PartUID    string
	Status     string
	StartedAt  time.Time
	EndedAt    time.Time
	Params     map[string]string
}

// DB is the open database.
type DB struct {
	sql *sql.DB
}

// Open opens (creating if needed) the database at path.
func Open(path string) (*DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA foreign_keys = ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("store: %s: %w", pragma, err)
		}
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: create schema: %w", err)
	}
	return &DB{sql: db}, nil
}

// Close closes the database.
func (db *DB) Close() error { return db.sql.Close() }

const schema = `
CREATE TABLE IF NOT EXISTS projects (
	id         TEXT PRIMARY KEY,
	parent_id  TEXT NOT NULL DEFAULT '',
	owner      TEXT NOT NULL DEFAULT '',
	name       TEXT NOT NULL,
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS projects_parent ON projects(parent_id);
CREATE TABLE IF NOT EXISTS parts (
	uid        TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(id),
	name       TEXT NOT NULL,
	created_by TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	placements TEXT NOT NULL DEFAULT '',
	engrave    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS parts_project ON parts(project_id);
CREATE TABLE IF NOT EXISTS part_files (
	id         TEXT PRIMARY KEY,
	part_uid   TEXT NOT NULL REFERENCES parts(uid),
	kind       TEXT NOT NULL,
	filename   TEXT NOT NULL,
	sha256     TEXT NOT NULL,
	size       INTEGER NOT NULL,
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS part_files_part ON part_files(part_uid);
CREATE INDEX IF NOT EXISTS part_files_sha ON part_files(sha256);
CREATE TABLE IF NOT EXISTS part_fields (
	part_uid TEXT NOT NULL REFERENCES parts(uid),
	position INTEGER NOT NULL,
	name     TEXT NOT NULL,
	value    TEXT NOT NULL,
	PRIMARY KEY (part_uid, position)
);
CREATE TABLE IF NOT EXISTS print_jobs (
	id          TEXT PRIMARY KEY,
	printer     TEXT NOT NULL,
	external_id TEXT NOT NULL,
	filename    TEXT NOT NULL,
	sha256      TEXT NOT NULL DEFAULT '',
	part_uid    TEXT NOT NULL DEFAULT '',
	status      TEXT NOT NULL,
	started_at  TEXT NOT NULL,
	ended_at    TEXT,
	params      TEXT NOT NULL DEFAULT '{}',
	UNIQUE (printer, external_id, started_at)
);
CREATE INDEX IF NOT EXISTS print_jobs_part ON print_jobs(part_uid);
`

// -- projects ---------------------------------------------------------------

// CreateProject makes a folder. An empty parent is the top level.
func (db *DB) CreateProject(ctx context.Context, parentID, owner, name string) (Project, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Project{}, errors.New("store: a project needs a name")
	}
	if parentID != "" {
		if _, err := db.ProjectByID(ctx, parentID); err != nil {
			return Project{}, err
		}
	}
	project := Project{
		ID:        mustID(),
		ParentID:  parentID,
		Owner:     owner,
		Name:      name,
		CreatedAt: time.Now().UTC(),
	}
	_, err := db.sql.ExecContext(ctx,
		`INSERT INTO projects (id, parent_id, owner, name, created_at) VALUES (?, ?, ?, ?, ?)`,
		project.ID, project.ParentID, project.Owner, project.Name, stamp(project.CreatedAt))
	if err != nil {
		return Project{}, fmt.Errorf("store: create project: %w", err)
	}
	return project, nil
}

// ProjectByID returns one project, or ErrNotFound.
func (db *DB) ProjectByID(ctx context.Context, id string) (Project, error) {
	row := db.sql.QueryRowContext(ctx,
		`SELECT id, parent_id, owner, name, created_at FROM projects WHERE id = ?`, id)
	return scanProject(row.Scan)
}

// ChildProjects lists the projects inside one (or the top level, for "").
func (db *DB) ChildProjects(ctx context.Context, parentID string) ([]Project, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT id, parent_id, owner, name, created_at FROM projects
		 WHERE parent_id = ? ORDER BY name COLLATE NOCASE`, parentID)
	if err != nil {
		return nil, fmt.Errorf("store: list projects: %w", err)
	}
	defer rows.Close()
	var projects []Project
	for rows.Next() {
		p, err := scanProject(rows.Scan)
		if err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

// ProjectPath walks from the top level down to a project, inclusive.
func (db *DB) ProjectPath(ctx context.Context, id string) ([]Project, error) {
	var path []Project
	for id != "" && len(path) < 32 {
		project, err := db.ProjectByID(ctx, id)
		if err != nil {
			return nil, err
		}
		path = append([]Project{project}, path...)
		id = project.ParentID
	}
	return path, nil
}

// -- parts ------------------------------------------------------------------

// CreatePart records a described upload: the part row, its source files, and
// its fields, atomically, under a freshly minted uid.
func (db *DB) CreatePart(ctx context.Context, part Part, files []PartFile, fields []Field) (Part, error) {
	if part.Name == "" {
		return Part{}, errors.New("store: a part needs a name")
	}
	if _, err := db.ProjectByID(ctx, part.ProjectID); err != nil {
		return Part{}, err
	}

	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return Part{}, fmt.Errorf("store: begin part: %w", err)
	}
	defer tx.Rollback()

	part.CreatedAt = time.Now().UTC()

	// Mint until an unused uid lands; at six base36 characters, collisions
	// are lottery wins.
	for range 100 {
		part.UID, err = mintUID()
		if err != nil {
			return Part{}, err
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO parts (uid, project_id, name, created_by, created_at, placements, engrave)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			part.UID, part.ProjectID, part.Name, part.CreatedBy,
			stamp(part.CreatedAt), part.Placements, part.Engrave)
		if err == nil {
			break
		}
		if !strings.Contains(err.Error(), "UNIQUE") {
			return Part{}, fmt.Errorf("store: create part: %w", err)
		}
	}
	if err != nil {
		return Part{}, fmt.Errorf("store: could not mint a uid: %w", err)
	}

	for _, file := range files {
		file.PartUID = part.UID
		if err := insertFile(ctx, tx, file); err != nil {
			return Part{}, err
		}
	}
	for position, field := range fields {
		if strings.TrimSpace(field.Name) == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO part_fields (part_uid, position, name, value) VALUES (?, ?, ?, ?)`,
			part.UID, position, field.Name, field.Value); err != nil {
			return Part{}, fmt.Errorf("store: add field: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return Part{}, fmt.Errorf("store: commit part: %w", err)
	}
	return part, nil
}

// PartByUID returns one part, or ErrNotFound.
func (db *DB) PartByUID(ctx context.Context, uid string) (Part, error) {
	row := db.sql.QueryRowContext(ctx,
		`SELECT uid, project_id, name, created_by, created_at, placements, engrave
		 FROM parts WHERE uid = ?`, strings.ToLower(strings.TrimSpace(uid)))
	return scanPart(row.Scan)
}

// PartsInProject lists a project's parts, newest first.
func (db *DB) PartsInProject(ctx context.Context, projectID string) ([]Part, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT uid, project_id, name, created_by, created_at, placements, engrave
		 FROM parts WHERE project_id = ? ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("store: list parts: %w", err)
	}
	defer rows.Close()
	var parts []Part
	for rows.Next() {
		p, err := scanPart(rows.Scan)
		if err != nil {
			return nil, err
		}
		parts = append(parts, p)
	}
	return parts, rows.Err()
}

// SetEngrave records what ended up cut into the part.
func (db *DB) SetEngrave(ctx context.Context, uid, engrave string) error {
	res, err := db.sql.ExecContext(ctx,
		`UPDATE parts SET engrave = ? WHERE uid = ?`, engrave, uid)
	if err != nil {
		return fmt.Errorf("store: set engrave: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetPlacements records the ranked candidate faces, computed once at upload.
func (db *DB) SetPlacements(ctx context.Context, uid, placements string) error {
	res, err := db.sql.ExecContext(ctx,
		`UPDATE parts SET placements = ? WHERE uid = ?`, placements, uid)
	if err != nil {
		return fmt.Errorf("store: set placements: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteFilesOfKind drops a part's files of one kind, for when a re-engrave
// replaces the engraved output. The blobs stay; they are content-addressed
// and something else may reference the same bytes.
func (db *DB) DeleteFilesOfKind(ctx context.Context, uid, kind string) error {
	_, err := db.sql.ExecContext(ctx,
		`DELETE FROM part_files WHERE part_uid = ? AND kind = ?`, uid, kind)
	if err != nil {
		return fmt.Errorf("store: delete %s files: %w", kind, err)
	}
	return nil
}

// AddPartFile records one more stored file of a part.
func (db *DB) AddPartFile(ctx context.Context, file PartFile) error {
	if _, err := db.PartByUID(ctx, file.PartUID); err != nil {
		return err
	}
	return insertFile(ctx, db.sql, file)
}

// PartFiles lists a part's files, sources first, oldest first within a kind.
func (db *DB) PartFiles(ctx context.Context, uid string) ([]PartFile, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT id, part_uid, kind, filename, sha256, size, created_at
		 FROM part_files WHERE part_uid = ? ORDER BY kind DESC, created_at ASC`, uid)
	if err != nil {
		return nil, fmt.Errorf("store: list files: %w", err)
	}
	defer rows.Close()
	var files []PartFile
	for rows.Next() {
		var f PartFile
		var created string
		if err := rows.Scan(&f.ID, &f.PartUID, &f.Kind, &f.Filename, &f.SHA256, &f.Size, &created); err != nil {
			return nil, fmt.Errorf("store: read file: %w", err)
		}
		f.CreatedAt = parseStamp(created)
		files = append(files, f)
	}
	return files, rows.Err()
}

// PartFileByID returns one file row of a part.
func (db *DB) PartFileByID(ctx context.Context, uid, fileID string) (PartFile, error) {
	row := db.sql.QueryRowContext(ctx,
		`SELECT id, part_uid, kind, filename, sha256, size, created_at
		 FROM part_files WHERE part_uid = ? AND id = ?`, uid, fileID)
	var f PartFile
	var created string
	err := row.Scan(&f.ID, &f.PartUID, &f.Kind, &f.Filename, &f.SHA256, &f.Size, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return PartFile{}, ErrNotFound
	}
	if err != nil {
		return PartFile{}, fmt.Errorf("store: read file: %w", err)
	}
	f.CreatedAt = parseStamp(created)
	return f, nil
}

// PartFields lists a part's fields in the order they were written.
func (db *DB) PartFields(ctx context.Context, uid string) ([]Field, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT name, value FROM part_fields WHERE part_uid = ? ORDER BY position`, uid)
	if err != nil {
		return nil, fmt.Errorf("store: list fields: %w", err)
	}
	defer rows.Close()
	var fields []Field
	for rows.Next() {
		var f Field
		if err := rows.Scan(&f.Name, &f.Value); err != nil {
			return nil, fmt.Errorf("store: read field: %w", err)
		}
		fields = append(fields, f)
	}
	return fields, rows.Err()
}

// FieldNamesUsedBy is the suggestion list: every field name this person has
// used before, most recently used first. History is the preference store.
func (db *DB) FieldNamesUsedBy(ctx context.Context, owner string) ([]string, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT f.name, MAX(p.created_at) AS last
		FROM part_fields f JOIN parts p ON p.uid = f.part_uid
		WHERE p.created_by = ?
		GROUP BY f.name ORDER BY last DESC LIMIT 50`, owner)
	if err != nil {
		return nil, fmt.Errorf("store: field names: %w", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name, last string
		if err := rows.Scan(&name, &last); err != nil {
			return nil, fmt.Errorf("store: read field name: %w", err)
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// -- print jobs -------------------------------------------------------------

// uidInFilename spots an engraved name wherever the file went next:
// "bracket-x7k2p9.stl" as uploaded, "bracket-x7k2p9" as a printer reports
// the job, "bracket-x7k2p9.gcode.3mf" as a slicer left it.
var uidInFilename = regexp.MustCompile(`(?i)-([a-z0-9]{6})(?:\.[0-9a-z]+)*$`)

// RecordJob upserts a watched job and ties it to a part when it can: an
// exact file-hash match first, the uid in an engraved filename second.
func (db *DB) RecordJob(ctx context.Context, job printwatch.Job) error {
	partUID := ""
	if job.SHA256 != "" {
		var uid string
		err := db.sql.QueryRowContext(ctx,
			`SELECT part_uid FROM part_files WHERE sha256 = ? LIMIT 1`, job.SHA256).Scan(&uid)
		if err == nil {
			partUID = uid
		}
	}
	if partUID == "" {
		if match := uidInFilename.FindStringSubmatch(job.Filename); match != nil {
			uid := strings.ToLower(match[1])
			if _, err := db.PartByUID(ctx, uid); err == nil {
				partUID = uid
			}
		}
	}

	params, err := json.Marshal(job.Params)
	if err != nil {
		return fmt.Errorf("store: encode params: %w", err)
	}
	var ended any
	if !job.EndedAt.IsZero() {
		ended = stamp(job.EndedAt)
	}

	_, err = db.sql.ExecContext(ctx, `
		INSERT INTO print_jobs (id, printer, external_id, filename, sha256, part_uid, status, started_at, ended_at, params)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (printer, external_id, started_at) DO UPDATE SET
			filename = excluded.filename,
			sha256   = CASE WHEN excluded.sha256 != '' THEN excluded.sha256 ELSE print_jobs.sha256 END,
			part_uid = CASE WHEN excluded.part_uid != '' THEN excluded.part_uid ELSE print_jobs.part_uid END,
			status   = excluded.status,
			ended_at = excluded.ended_at,
			params   = excluded.params`,
		mustID(), job.Printer, job.ExternalID, job.Filename, job.SHA256, partUID,
		job.Status, stamp(job.StartedAt), ended, string(params))
	if err != nil {
		return fmt.Errorf("store: record job: %w", err)
	}
	return nil
}

// JobsForPart lists every print of a part, newest first.
func (db *DB) JobsForPart(ctx context.Context, uid string) ([]PrintJob, error) {
	return db.jobs(ctx, `WHERE part_uid = ? ORDER BY started_at DESC`, uid)
}

// RecentJobs lists the latest activity across every printer.
func (db *DB) RecentJobs(ctx context.Context, limit int) ([]PrintJob, error) {
	if limit <= 0 {
		limit = 50
	}
	return db.jobs(ctx, `ORDER BY started_at DESC LIMIT ?`, limit)
}

func (db *DB) jobs(ctx context.Context, tail string, args ...any) ([]PrintJob, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT id, printer, external_id, filename, sha256, part_uid, status, started_at, ended_at, params
		 FROM print_jobs `+tail, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list jobs: %w", err)
	}
	defer rows.Close()

	var jobs []PrintJob
	for rows.Next() {
		var j PrintJob
		var started string
		var ended sql.NullString
		var params string
		if err := rows.Scan(&j.ID, &j.Printer, &j.ExternalID, &j.Filename, &j.SHA256,
			&j.PartUID, &j.Status, &started, &ended, &params); err != nil {
			return nil, fmt.Errorf("store: read job: %w", err)
		}
		j.StartedAt = parseStamp(started)
		if ended.Valid {
			j.EndedAt = parseStamp(ended.String)
		}
		json.Unmarshal([]byte(params), &j.Params)
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// -- shared -----------------------------------------------------------------

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func insertFile(ctx context.Context, ex execer, file PartFile) error {
	if file.ID == "" {
		file.ID = mustID()
	}
	if file.CreatedAt.IsZero() {
		file.CreatedAt = time.Now().UTC()
	}
	_, err := ex.ExecContext(ctx,
		`INSERT INTO part_files (id, part_uid, kind, filename, sha256, size, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		file.ID, file.PartUID, file.Kind, file.Filename, file.SHA256, file.Size, stamp(file.CreatedAt))
	if err != nil {
		return fmt.Errorf("store: add file: %w", err)
	}
	return nil
}

func scanProject(scan func(...any) error) (Project, error) {
	var p Project
	var created string
	err := scan(&p.ID, &p.ParentID, &p.Owner, &p.Name, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, ErrNotFound
	}
	if err != nil {
		return Project{}, fmt.Errorf("store: read project: %w", err)
	}
	p.CreatedAt = parseStamp(created)
	return p, nil
}

func scanPart(scan func(...any) error) (Part, error) {
	var p Part
	var created string
	err := scan(&p.UID, &p.ProjectID, &p.Name, &p.CreatedBy, &created, &p.Placements, &p.Engrave)
	if errors.Is(err, sql.ErrNoRows) {
		return Part{}, ErrNotFound
	}
	if err != nil {
		return Part{}, fmt.Errorf("store: read part: %w", err)
	}
	p.CreatedAt = parseStamp(created)
	return p, nil
}

func stamp(t time.Time) string          { return t.UTC().Format(time.RFC3339Nano) }
func parseStamp(s string) (t time.Time) { t, _ = time.Parse(time.RFC3339Nano, s); return }

// uidAlphabet is what gets engraved: lowercase base36, every character
// distinct in the engraving font.
const uidAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// mintUID makes a six-character uid that is never all digits, so a uid can
// always be told from a number.
func mintUID() (string, error) {
	for {
		out := make([]byte, 6)
		letters := false
		for i := range out {
			n, err := rand.Int(rand.Reader, big.NewInt(int64(len(uidAlphabet))))
			if err != nil {
				return "", fmt.Errorf("store: mint uid: %w", err)
			}
			out[i] = uidAlphabet[n.Int64()]
			if out[i] >= 'a' {
				letters = true
			}
		}
		if letters {
			return string(out), nil
		}
	}
}

// mustID mints a random row id. The randomness source failing is a machine
// too broken to keep serving.
func mustID() string {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x", raw)
}
