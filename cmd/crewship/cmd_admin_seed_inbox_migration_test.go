package main

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/spf13/cobra"
)

func TestClearMigratedSeedWaitpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crewship.db")
	t.Setenv("DATABASE_URL", path)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE inbox_items(workspace_id TEXT,kind TEXT,source_id TEXT);
 CREATE TABLE approvals_queue(workspace_id TEXT,reason TEXT);
 CREATE TABLE pipeline_waitpoints(workspace_id TEXT,token TEXT,pipeline_run_id TEXT);
 CREATE TABLE pipeline_runs(workspace_id TEXT,id TEXT);
 INSERT INTO pipeline_runs VALUES('ws','run_seed_abc')`)
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{pipeline.LegacyWaitpointPublicID("ws", "seed_abc_1"), "run_seed_abc"}, {"wp_keep", "run_real"}} {
		if _, err = db.Exec("INSERT INTO pipeline_waitpoints VALUES('ws',?,?)", pair[0], pair[1]); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec("INSERT INTO inbox_items VALUES('ws','waitpoint',?)", pair[0]); err != nil {
			t.Fatal(err)
		}
	}
	cmd := &cobra.Command{}
	cmd.Flags().Bool("local", true, "")
	cmd.Flags().Bool("clear", true, "")
	cmd.Flags().String("workspace", "ws", "")
	if err := runAdminSeedInbox(cmd, nil); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"SELECT source_id FROM inbox_items", "SELECT token FROM pipeline_waitpoints"} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM (" + query + ")").Scan(&count); err != nil || count != 1 {
			t.Errorf("%s: rows=%d want 1 (%v)", query, count, err)
		}
		var id string
		if err := db.QueryRow(query).Scan(&id); err != nil || id != "wp_keep" {
			t.Errorf("surviving row = %q (%v)", id, err)
		}
	}
}
