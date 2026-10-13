package database

import (
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/runauthority"
)

func TestExecutionOriginsUpgradeFencesUnknownQueuedWork(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "upgrade.db")+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := applyMigrationsUpTo(t.Context(), db, 20261013162000-1, logger); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(t.Context(), q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO workspaces(id,name,slug) VALUES('origin-upgrade-ws','Upgrade','origin-upgrade')`)
	exec(`INSERT INTO crews(id,workspace_id,name,slug) VALUES('origin-upgrade-crew','origin-upgrade-ws','Crew','crew')`)
	exec(`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role) VALUES('origin-upgrade-agent','origin-upgrade-ws','origin-upgrade-crew','Lead','lead','LEAD')`)
	exec(`INSERT INTO chats(id,workspace_id,agent_id) VALUES('origin-upgrade-chat','origin-upgrade-ws','origin-upgrade-agent')`)
	statuses := []string{"PENDING", "QUEUED", "RUNNING", "COMPLETED", "FAILED", "CANCELLED", "TIMEOUT"}
	for _, status := range statuses {
		exec(`INSERT INTO assignments(id,workspace_id,chat_id,assigned_by_id,assigned_to_id,task,status,created_at) VALUES(?,'origin-upgrade-ws','origin-upgrade-chat','origin-upgrade-agent','origin-upgrade-agent','Preserve this work',?,'2026-01-01T00:00:00Z')`, "upgrade-assignment-"+status, status)
	}
	missionStatuses := []string{"PLANNING", "IN_PROGRESS", "PAUSED", "COMPLETED", "FAILED", "CANCELLED"}
	for _, status := range missionStatuses {
		exec(`INSERT INTO missions(id,workspace_id,crew_id,lead_agent_id,trace_id,title,status,mission_type) VALUES(?,'origin-upgrade-ws','origin-upgrade-crew','origin-upgrade-agent',?,'Preserve this mission',?,'orchestration')`, "upgrade-mission-"+status, "upgrade-trace-"+status, status)
	}
	exec(`INSERT INTO missions(id,workspace_id,crew_id,lead_agent_id,trace_id,title,status,mission_type) VALUES('upgrade-issue','origin-upgrade-ws','origin-upgrade-crew','origin-upgrade-agent','upgrade-issue-trace','Ordinary issue','IN_PROGRESS','issue')`)

	exec(`INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash) VALUES('upgrade-pipeline','origin-upgrade-ws','upgrade-pipeline','Upgrade','{"steps":[]}','upgrade-hash')`)
	exec(`INSERT INTO journal_entries(id,workspace_id,agent_id,entry_type,actor_type,summary,trace_id,payload) VALUES('upgrade-human-start','origin-upgrade-ws','origin-upgrade-agent','run.started','orchestrator','Chat root','upgrade-human-root','{"human_authority":{"chat_id":"legacy-chat"},"trigger_message_id":"legacy-trigger"}')`)
	for _, kind := range []string{"human", "standing"} {
		source := "standing-schedule"
		if kind == "human" {
			source = "upgrade-human-root"
		}
		exec(`INSERT INTO pending_runs(id,workspace_id,pipeline_id,pipeline_slug,fire_at,status,chain_origin) VALUES(?,'origin-upgrade-ws','upgrade-pipeline','upgrade-pipeline','2026-10-13T00:00:00Z','pending',?)`, "upgrade-pending-"+kind, source)
		exec(`INSERT INTO pipeline_runs(id,workspace_id,pipeline_id,pipeline_slug,status,started_at,triggered_by_id) VALUES(?,'origin-upgrade-ws','upgrade-pipeline','upgrade-pipeline','waiting','2026-10-13T00:00:00Z',?)`, "upgrade-active-"+kind, source)
	}
	if err := Migrate(t.Context(), db, logger); err != nil {
		t.Fatal(err)
	}
	var sentinel string
	if err := db.QueryRow(`SELECT id FROM execution_origins WHERE workspace_id='origin-upgrade-ws' AND root_run_id=?`, runauthority.LegacyUnverifiedDelegationRootRunID).Scan(&sentinel); err != nil {
		t.Fatal(err)
	}

	for _, table := range []string{"pending_runs", "pipeline_runs"} {
		prefix := "upgrade-pending-"
		if table == "pipeline_runs" {
			prefix = "upgrade-active-"
		}
		for _, kind := range []string{"human", "standing"} {
			var origin sql.NullString
			if err := db.QueryRow("SELECT chat_origin_id FROM "+table+" WHERE id=?", prefix+kind).Scan(&origin); err != nil {
				t.Fatal(err)
			}
			if (kind == "human" && (!origin.Valid || origin.String != sentinel)) || (kind == "standing" && origin.Valid) {
				t.Fatalf("%s %s origin %+v", table, kind, origin)
			}
		}
	}
	for _, status := range statuses {
		var actual, task, created string
		var origin sql.NullString
		if err := db.QueryRow(`SELECT status,task,created_at,chat_origin_id FROM assignments WHERE id=?`, "upgrade-assignment-"+status).Scan(&actual, &task, &created, &origin); err != nil {
			t.Fatal(err)
		}
		pending := status == "PENDING" || status == "QUEUED" || status == "RUNNING"
		if actual != status || task != "Preserve this work" || created != "2026-01-01T00:00:00Z" || origin.Valid != pending || (pending && origin.String != sentinel) {
			t.Fatalf("assignment %s changed incorrectly: %s %s %s %+v", status, actual, task, created, origin)
		}
	}
	for _, status := range missionStatuses {
		var actual, title string
		var origin sql.NullString
		if err := db.QueryRow(`SELECT status,title,chat_origin_id FROM missions WHERE id=?`, "upgrade-mission-"+status).Scan(&actual, &title, &origin); err != nil {
			t.Fatal(err)
		}
		active := status == "PLANNING" || status == "IN_PROGRESS" || status == "PAUSED"
		if actual != status || title != "Preserve this mission" || origin.Valid != active || (active && origin.String != sentinel) {
			t.Fatalf("mission %s changed incorrectly: %s %s %+v", status, actual, title, origin)
		}
	}
	if _, err := runauthority.ValidateOrigin(t.Context(), db, sentinel, "origin-upgrade-agent", "origin-upgrade-ws"); !errors.Is(err, runauthority.ErrIdentityMismatch) {
		t.Fatalf("legacy queued authority accepted: %v", err)
	}
	exec(`INSERT INTO assignments(id,workspace_id,chat_id,assigned_by_id,assigned_to_id,task,status,created_at) VALUES('new-autonomous','origin-upgrade-ws','origin-upgrade-chat','origin-upgrade-agent','origin-upgrade-agent','new work','QUEUED','2026-10-13T00:00:00Z')`)
	var untouched int
	if err := db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE id='new-autonomous' AND chat_origin_id IS NULL`).Scan(&untouched); err != nil || untouched != 1 {
		t.Fatalf("new autonomous work fenced: %d %v", untouched, err)
	}
	var issueOrigin sql.NullString
	if err := db.QueryRow(`SELECT chat_origin_id FROM missions WHERE id='upgrade-issue'`).Scan(&issueOrigin); err != nil || issueOrigin.Valid {
		t.Fatalf("ordinary issue altered: %+v %v", issueOrigin, err)
	}
	if _, err := db.Exec(`UPDATE execution_origins SET human_authority='null' WHERE id=?`, sentinel); err == nil || !strings.Contains(err.Error(), "immutable") {
		t.Fatalf("sentinel can be rewritten: %v", err)
	}
}
