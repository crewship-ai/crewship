package database

import (
	"fmt"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

func TestEventWaitDeadlineMigration_BackfillsOriginalRecipeAndCancels(t *testing.T) {
	db := migrationBoundaryDB(t,
		migrationSignalWaits,
		`CREATE TABLE pipeline_runs(id TEXT PRIMARY KEY,status TEXT,pipeline_id TEXT,pipeline_version INTEGER,executed_definition_json TEXT);
   CREATE TABLE pipelines(id TEXT PRIMARY KEY,definition_json TEXT);
   CREATE TABLE pipeline_versions(pipeline_id TEXT,version INTEGER,definition_json TEXT);`)
	recipe := func(timeout int) string {
		return `{"steps":[{"id":"gate","timeout_seconds":` + fmt.Sprint(timeout) + `}]}`
	}
	if _, err := db.Exec(`INSERT INTO pipelines VALUES('p',?)`, recipe(60)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO pipeline_versions VALUES('p',1,?)`, recipe(120)); err != nil {
		t.Fatal(err)
	}

	created := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		id, status, snapshot string
		version              any
		seconds              int
	}{
		{"snapshot", "waiting", recipe(10), 1, 10},
		{"pinned", "waiting", "", 1, 120},
		{"head", "waiting", "", nil, 60},
		{"default", "waiting", `{"steps":[{"id":"gate"}]}`, nil, 3600},
		{"invalid", "waiting", "bad json", nil, 3600},
		{"cancelled", "cancelled", recipe(10), nil, 10},
	} {
		if _, err := db.Exec(`INSERT INTO pipeline_runs VALUES(?,?,?,?,?)`, tc.id, tc.status, "p", tc.version, tc.snapshot); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO pipeline_signal_waits(id,workspace_id,run_id,step_id,event_type,created_at) VALUES(?,'ws',?,'gate','event',?)`, tc.id, tc.id, tsformat.Format(created)); err != nil {
			t.Fatal(err)
		}

	}
	migration, err := migrationFS.ReadFile("migrations/20261004204026_event_wait_deadlines.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	for id, seconds := range map[string]int{"snapshot": 10, "pinned": 120, "head": 60, "default": 3600, "invalid": 3600} {
		var deadline string
		if err := db.QueryRow(`SELECT timeout_at FROM pipeline_signal_waits WHERE id=?`, id).Scan(&deadline); err != nil {
			t.Fatal(err)
		}
		if want := tsformat.Format(created.Add(time.Duration(seconds) * time.Second)); deadline != want {
			t.Errorf("%s deadline=%s want=%s", id, deadline, want)
		}
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM pipeline_signal_waits WHERE id='cancelled'`).Scan(&status); err != nil || status != "cancelled" {
		t.Fatalf("legacy cancel status=%s err=%v", status, err)
	}
	// Both directions of the arm/terminal-transition race must settle it.
	if _, err := db.Exec(`UPDATE pipeline_runs SET status='cancelled' WHERE id='snapshot'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO pipeline_signal_waits(id,workspace_id,run_id,step_id,event_type,created_at) VALUES('late','ws','snapshot','late','event',?)`, tsformat.Format(created)); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"snapshot", "late"} {
		if err := db.QueryRow(`SELECT status FROM pipeline_signal_waits WHERE id=?`, id).Scan(&status); err != nil || status != "cancelled" {
			t.Fatalf("race %s status=%s err=%v", id, status, err)
		}
	}
}
