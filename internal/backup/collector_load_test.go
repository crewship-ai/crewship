package backup

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// collector.go — LoadCrewTarget, isNotFoundErr, EnsureSectionReader,
// noopReader.Read. Existing tests in this package cover the DockerOps-
// driven CollectCrew path; these fill in the DB-only resolver, the
// Docker error-classifier, and the bundle-reader nil safety helper.
// ---------------------------------------------------------------------------

const collectorTestSchema = `
CREATE TABLE workspaces (
    id TEXT PRIMARY KEY,
    slug TEXT NOT NULL,
    name TEXT NOT NULL
);

CREATE TABLE crews (
    id                  TEXT PRIMARY KEY,
    workspace_id        TEXT NOT NULL REFERENCES workspaces(id),
    slug                TEXT NOT NULL,
    name                TEXT NOT NULL,
    devcontainer_config TEXT,
    mise_config         TEXT,
    runtime_image       TEXT,
    cached_image        TEXT,
    config_hash         TEXT,
    deleted_at          TEXT,
    created_at          TEXT NOT NULL DEFAULT (datetime('now','subsec'))
);

CREATE TABLE agents (
    id      TEXT PRIMARY KEY,
    crew_id TEXT NOT NULL REFERENCES crews(id)
);
`

func openCollectorTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := t.TempDir() + "/coll.db"
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(collectorTestSchema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

// ---- isNotFoundErr ----

func TestIsNotFoundErr_Classification(t *testing.T) {
	// The source comment makes the contract explicit: only the two
	// "path missing" phrasings qualify; "no such container" must NOT
	// match so it propagates as a hard error rather than silently
	// producing an empty backup.
	cases := []struct {
		name string
		in   error
		want bool
	}{
		{"nil", nil, false},
		{"path-missing-v1", errors.New("Could not find the file /workspace in container abc"), true},
		{"path-missing-v2", errors.New("No such container:path: abc:/workspace"), true},
		{"wrapped-path-missing", errors.New("backup: Could not find the file /memory"), true},
		// "No such container" without :path is the regression-class the
		// source explicitly calls out — must NOT classify as not-found.
		{"no-such-container-must-not-match", errors.New("Error: No such container: abc123"), false},
		{"unrelated-docker-error", errors.New("daemon refused"), false},
		{"empty-string-error", errors.New(""), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isNotFoundErr(tc.in); got != tc.want {
				t.Errorf("isNotFoundErr(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// ---- LoadCrewTarget ----

func TestLoadCrewTarget_NotFound(t *testing.T) {
	db := openCollectorTestDB(t)
	_, err := LoadCrewTarget(context.Background(), db, "missing", func(_, s string) string { return "n-" + s })
	if err == nil {
		t.Fatal("expected error for missing crew")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Errorf("err = %q, expected mention of crew id", err)
	}
}

func TestLoadCrewTarget_SoftDeletedCrewIsInvisible(t *testing.T) {
	db := openCollectorTestDB(t)
	if _, err := db.Exec(`INSERT INTO workspaces (id, slug, name) VALUES ('ws1', 'ws-1', 'Workspace 1')`); err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, slug, name, deleted_at)
		VALUES ('c-gone', 'ws1', 'gone', 'Gone', '2024-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	if _, err := LoadCrewTarget(context.Background(), db, "c-gone", nil); err == nil {
		t.Error("soft-deleted crew should be invisible to LoadCrewTarget (deleted_at IS NULL filter)")
	}
}

func TestLoadCrewTarget_HappyPath_PopulatesEverything(t *testing.T) {
	db := openCollectorTestDB(t)
	if _, err := db.Exec(`INSERT INTO workspaces (id, slug, name) VALUES ('ws1', 'ws-1', 'Workspace 1')`); err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, slug, name,
	    devcontainer_config, mise_config, runtime_image, cached_image, config_hash)
		VALUES ('c1', 'ws1', 'alpha', 'Alpha',
		    '{"image":"ubuntu"}', '[tools]', 'ubuntu:22.04', 'crewship-cache:abc', 'hash-1')`); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO agents (id, crew_id) VALUES ('a1', 'c1'), ('a2', 'c1'), ('a3', 'c1')`); err != nil {
		t.Fatalf("seed agents: %v", err)
	}

	wt, err := LoadCrewTarget(context.Background(), db, "c1", func(_, slug string) string {
		return "crewship-team-" + slug
	})
	if err != nil {
		t.Fatalf("LoadCrewTarget: %v", err)
	}
	if wt.ID != "ws1" || wt.Slug != "ws-1" || wt.Name != "Workspace 1" {
		t.Errorf("workspace = %+v, want id=ws1 slug=ws-1 name=Workspace 1", wt)
	}
	if len(wt.CrewTargets) != 1 {
		t.Fatalf("CrewTargets len = %d, want 1", len(wt.CrewTargets))
	}
	got := wt.CrewTargets[0]
	if got.ID != "c1" || got.Slug != "alpha" || got.Name != "Alpha" {
		t.Errorf("crew identity = %+v, want id=c1 slug=alpha name=Alpha", got)
	}
	if got.DevcontainerConfig != `{"image":"ubuntu"}` {
		t.Errorf("DevcontainerConfig = %q", got.DevcontainerConfig)
	}
	if got.MiseConfig != "[tools]" {
		t.Errorf("MiseConfig = %q", got.MiseConfig)
	}
	if got.RuntimeImage != "ubuntu:22.04" {
		t.Errorf("RuntimeImage = %q", got.RuntimeImage)
	}
	if got.CachedImageDigest != "crewship-cache:abc" {
		t.Errorf("CachedImageDigest = %q", got.CachedImageDigest)
	}
	if got.ConfigHash != "hash-1" {
		t.Errorf("ConfigHash = %q", got.ConfigHash)
	}
	if got.ContainerID != "crewship-team-alpha" {
		t.Errorf("ContainerID = %q, want crewship-team-alpha (callback applied)", got.ContainerID)
	}
	if got.AgentCount != 3 {
		t.Errorf("AgentCount = %d, want 3", got.AgentCount)
	}
}

func TestLoadCrewTarget_NilContainerNameCallback_LeavesContainerIDEmpty(t *testing.T) {
	// The runner sometimes constructs the target without a Docker provider
	// (DB-only backups for testing the collector). nil callback must NOT
	// crash; ContainerID stays empty and CollectCrew's len-check downgrades
	// to "container never created" — covered separately.
	db := openCollectorTestDB(t)
	if _, err := db.Exec(`INSERT INTO workspaces (id, slug, name) VALUES ('ws1', 'ws-1', 'WS')`); err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, slug, name)
		VALUES ('c1', 'ws1', 'a', 'A')`); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	wt, err := LoadCrewTarget(context.Background(), db, "c1", nil)
	if err != nil {
		t.Fatalf("LoadCrewTarget: %v", err)
	}
	if len(wt.CrewTargets) != 1 {
		t.Fatalf("CrewTargets len = %d, want 1", len(wt.CrewTargets))
	}
	if wt.CrewTargets[0].ContainerID != "" {
		t.Errorf("ContainerID with nil callback = %q, want empty", wt.CrewTargets[0].ContainerID)
	}
}

func TestLoadCrewTarget_WorkspaceMissing_Errors(t *testing.T) {
	// Crew row references a workspace that doesn't exist. The crew query
	// succeeds (it doesn't validate the FK at read time) but the
	// subsequent workspace lookup fails. Pin that the function returns
	// a clean error rather than a half-populated struct.
	db := openCollectorTestDB(t)
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("disable fk: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, slug, name)
		VALUES ('c1', 'ws-orphan', 'a', 'A')`); err != nil {
		t.Fatalf("seed orphan crew: %v", err)
	}
	_, err := LoadCrewTarget(context.Background(), db, "c1", nil)
	if err == nil {
		t.Error("expected error when workspace row is missing")
	}
}

func TestLoadCrewTarget_AgentCountIsBestEffort_MissingTableNotFatal(t *testing.T) {
	// Source comment on the parallel call site: "Best-effort agent count;
	// a missing table is not fatal." Pin that contract — drop the agents
	// table, call LoadCrewTarget, expect a successful return with
	// AgentCount=0.
	db := openCollectorTestDB(t)
	if _, err := db.Exec(`INSERT INTO workspaces (id, slug, name) VALUES ('ws1', 'ws-1', 'WS')`); err != nil {
		t.Fatalf("seed ws: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, slug, name) VALUES ('c1', 'ws1', 'a', 'A')`); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE agents`); err != nil {
		t.Fatalf("drop agents: %v", err)
	}
	wt, err := LoadCrewTarget(context.Background(), db, "c1", nil)
	if err != nil {
		t.Fatalf("LoadCrewTarget with missing agents table should not error: %v", err)
	}
	if wt.CrewTargets[0].AgentCount != 0 {
		t.Errorf("AgentCount = %d, want 0 (missing table degrades silently)", wt.CrewTargets[0].AgentCount)
	}
}

// ---- EnsureSectionReader / noopReader ----

func TestEnsureSectionReader_NilReturnsNoopReader(t *testing.T) {
	r := EnsureSectionReader(nil)
	if r == nil {
		t.Fatal("EnsureSectionReader(nil) = nil, want non-nil noop reader")
	}
	// Read on the noop returns (0, io.EOF) immediately.
	buf := make([]byte, 16)
	n, err := r.Read(buf)
	if n != 0 {
		t.Errorf("noop Read n = %d, want 0", n)
	}
	if !errors.Is(err, io.EOF) {
		t.Errorf("noop Read err = %v, want io.EOF", err)
	}
	// Idempotent: a second Read must also be (0, EOF) — callers may loop.
	n, err = r.Read(buf)
	if n != 0 || !errors.Is(err, io.EOF) {
		t.Errorf("second noop Read = (%d, %v), want (0, io.EOF)", n, err)
	}
}

func TestEnsureSectionReader_NonNilPassesThrough(t *testing.T) {
	want := []byte("payload bytes")
	source := bytes.NewReader(want)
	got := EnsureSectionReader(source)
	if got == nil {
		t.Fatal("EnsureSectionReader(non-nil) = nil")
	}
	// io.ReadAll must produce the original bytes verbatim — the helper
	// must NOT wrap or re-buffer a non-nil reader.
	out, err := io.ReadAll(got)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(out, want) {
		t.Errorf("read = %q, want %q", out, want)
	}
}

func TestNoopReader_DirectUse(t *testing.T) {
	// Same coverage as EnsureSectionReader's nil path but invokes the
	// noopReader type directly — pins that nothing weird happens with
	// a zero-length buffer (would otherwise allow a subtle regression
	// to "return len(p), nil" that wouldn't surface in the EnsureSection
	// test where len(buf) > 0).
	r := noopReader{}
	n, err := r.Read(nil)
	if n != 0 || !errors.Is(err, io.EOF) {
		t.Errorf("noopReader.Read(nil) = (%d, %v), want (0, io.EOF)", n, err)
	}
}
