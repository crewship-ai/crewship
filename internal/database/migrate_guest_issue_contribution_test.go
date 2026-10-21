package database

import (
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

func TestGuestIssueUpgradeKeepsExistingCommentsPrivate(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("a1b2c3d4", 8))
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	version := 0
	for _, migration := range migrations {
		if migration.name == "guest_issue_contribution" {
			version = migration.version
		}
	}
	if version == 0 {
		t.Fatal("missing migration")
	}
	if err = applyMigrationsUpTo(t.Context(), db, version-1, logger); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`PRAGMA foreign_keys=ON`,
		`INSERT INTO users(id,email) VALUES('owner','owner@example.test'),('guest','guest@example.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('w','Workspace','w'),('other','Other','other')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('owner-m','w','owner','OWNER','trusted'),('guest-m','w','guest','VIEWER','restricted')`,
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES('crew','w','Crew','crew'),('foreign-crew','other','Other crew','other-crew')`,
		`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role) VALUES('lead','w','crew','Lead','lead','LEAD')`,
		`INSERT INTO projects(id,workspace_id,name,slug) VALUES('project','w','Project','project'),('other-project','other','Other project','other-project')`,
		`INSERT INTO missions(id,workspace_id,crew_id,lead_agent_id,trace_id,title,status,mission_type,project_id) VALUES('issue','w','crew','lead','trace','Issue','TODO','issue','project')`,
		`INSERT INTO mission_comments(id,mission_id,author_type,author_id,body) VALUES('old-comment','issue','user','owner','Existing private staff note')`,
		`INSERT INTO access_grants(id,member_id,resource_kind,project_id,operation,created_by,created_at) VALUES('old-grant','guest-m','project','project','read','owner','2026-10-14T00:00:00Z')`,
	} {
		if _, err = db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	var before int64
	if err = db.QueryRow(`SELECT access_revision FROM workspace_members WHERE id='guest-m'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(t.Context(), db, logger); err != nil {
		t.Fatal(err)
	}
	var private int
	if err = db.QueryRow(`SELECT COUNT(*) FROM mission_comments WHERE id='old-comment' AND body='Existing private staff note' AND guest_visible=0 AND guest_project_id IS NULL`).Scan(&private); err != nil || private != 1 {
		t.Fatalf("old comment was shared/changed: %d %v", private, err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM access_grants WHERE id='old-grant' AND created_by='owner' AND created_at='2026-10-14T00:00:00Z'`).Scan(&private); err != nil || private != 1 {
		t.Fatalf("grant metadata changed: %d %v", private, err)
	}
	var after int64
	if err = db.QueryRow(`SELECT access_revision FROM workspace_members WHERE id='guest-m'`).Scan(&after); err != nil || after != before {
		t.Fatalf("unrelated shares fenced: %d -> %d %v", before, after, err)
	}
	if _, err = db.Exec(`INSERT INTO project_guest_issue_intake(project_id,workspace_id,crew_id,updated_by,updated_at) VALUES('project','w','foreign-crew','owner','2026-10-14T00:00:00Z')`); err == nil {
		t.Fatal("cross-workspace intake accepted")
	}
	if _, err = db.Exec(`INSERT INTO mission_comments(id,mission_id,author_type,author_id,body,guest_visible,guest_project_id) VALUES('bad-comment','issue','user','owner','Wrong audience',1,'other-project')`); err == nil {
		t.Fatal("cross-project comment accepted")
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&private); err != nil || private != 0 {
		t.Fatalf("foreign keys: %d %v", private, err)
	}
}
