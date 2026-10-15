package database

import (
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoutineGrantsUpgradePreservesExistingPolicies(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("a1b2c3d4", 8))
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	version := 0
	for _, m := range migrations {
		if m.name == "routine_resource_grants" {
			version = m.version
		}
	}
	if version == 0 {
		t.Fatal("migration not registered")
	}
	if err = applyMigrationsUpTo(t.Context(), db, version-1, logger); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`PRAGMA foreign_keys=ON`,
		`INSERT INTO users(id,email) VALUES('upgrade-owner','upgrade-owner@example.test'),('upgrade-guest','upgrade-guest@example.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('upgrade-ws','Workspace','upgrade-ws')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('upgrade-owner-m','upgrade-ws','upgrade-owner','OWNER','trusted'),('upgrade-guest-m','upgrade-ws','upgrade-guest','VIEWER','restricted')`,
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES('upgrade-crew','upgrade-ws','Crew','upgrade-crew')`,
		`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role) VALUES('upgrade-agent','upgrade-ws','upgrade-crew','Agent','upgrade-agent','AGENT')`,
		`INSERT INTO projects(id,workspace_id,name,slug) VALUES('upgrade-project','upgrade-ws','Project','upgrade-project')`,
		`INSERT INTO pipelines(id,workspace_id,name,slug,definition_json,definition_hash,status) VALUES('upgrade-routine','upgrade-ws','Routine','upgrade-routine','{}','fixture','active')`,
		`INSERT INTO access_grants(id,member_id,resource_kind,agent_id,operation,created_by,created_at) VALUES('old-agent-grant','upgrade-guest-m','agent','upgrade-agent','chat','upgrade-owner','2026-10-13T00:00:00Z')`,
		`INSERT INTO access_grants(id,member_id,resource_kind,project_id,operation,created_by,created_at) VALUES('old-project-grant','upgrade-guest-m','project','upgrade-project','read','upgrade-owner','2026-10-13T00:00:00Z')`,
	} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	var before int64
	if err = db.QueryRow(`SELECT access_revision FROM workspace_members WHERE id='upgrade-guest-m'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(t.Context(), db, logger); err != nil {
		t.Fatal(err)
	}
	var retained int
	if err = db.QueryRow(`SELECT COUNT(*) FROM access_grants WHERE id IN ('old-agent-grant','old-project-grant') AND created_by='upgrade-owner' AND created_at='2026-10-13T00:00:00Z'`).Scan(&retained); err != nil || retained != 2 {
		t.Fatalf("old grants changed: %d %v", retained, err)
	}
	var guestRevision, ownerRevision int64
	if err = db.QueryRow(`SELECT access_revision FROM workspace_members WHERE id='upgrade-guest-m'`).Scan(&guestRevision); err != nil || guestRevision != before+1 {
		t.Fatalf("pending work not fenced: %d %d %v", before, guestRevision, err)
	}
	if err = db.QueryRow(`SELECT access_revision FROM workspace_members WHERE id='upgrade-owner-m'`).Scan(&ownerRevision); err != nil || ownerRevision != 1 {
		t.Fatalf("trusted compatibility changed: %d %v", ownerRevision, err)
	}
	if _, err = db.Exec(`INSERT INTO access_grants(id,member_id,resource_kind,routine_id,operation,created_by,created_at) VALUES('new-routine-grant','upgrade-guest-m','routine','upgrade-routine','run','upgrade-owner','2026-10-14T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE pipelines SET deleted_at='2026-10-14T00:00:00Z' WHERE id='upgrade-routine'`); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM access_grants WHERE id='new-routine-grant'`).Scan(&retained); err != nil || retained != 0 {
		t.Fatalf("deleted routine retained authority: %d %v", retained, err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&retained); err != nil || retained != 0 {
		t.Fatalf("foreign keys: %d %v", retained, err)
	}
}
