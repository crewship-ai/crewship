package backup

import (
	"context"
	"strings"
	"testing"
)

func TestPageProjectsBackupPreservesDraftMetadataAndWorkspaceScope(t *testing.T) {
	db := openMigratedDBCov(t)
	ctx := context.Background()
	var wanted string
	for _, suffix := range []string{"project-one", "project-two"} {
		ws, crew := seedCovWorkspace(t, db, suffix)
		if wanted == "" {
			wanted = ws
		}
		page := "page-" + suffix
		if _, err := db.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_crew_id,spec_json) VALUES(?,?,?,?,?,?)`, page, ws, "health", "Health", crew, "{}"); err != nil {
			t.Fatal(err)
		}
		// The editor can be absent after account deletion. Scope belongs to the
		// Page, so neither snapshot nor audit may disappear in that case.
		digest := strings.Repeat("a", 64)
		if _, err := db.Exec(`INSERT INTO page_project_drafts(page_id,source_digest,revision,spec_json,updated_at) VALUES(?,?,1,'{}','2026-09-08T00:00:00Z')`, page, digest); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO page_project_revisions(page_id,revision,source_digest,created_at,actor_json) VALUES(?,1,?,'2026-09-08T00:00:00Z','{"agent_id":"agent-author","crew_id":"crew-author"}')`, page, digest); err != nil {
			t.Fatal(err)
		}
		build := "build-" + suffix
		if _, err := db.Exec(`INSERT INTO page_project_builds(id,page_id,source_revision,source_digest,state,artifact_digest,created_at,actor_json) VALUES(?,?,1,?,'ready',?,'2026-09-08T00:00:00Z','{"agent_id":"agent-author","crew_id":"crew-author"}')`, build, page, digest, digest); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO page_project_publications(page_id,version,build_id,source_revision,source_digest,git_commit,artifact_digest,spec_json,checks_json,created_at) VALUES(?,1,?,1,?,'',?,'{}','{}','2026-09-08T00:00:00Z')`, page, build, digest, digest); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO page_project_live(page_id,version) VALUES(?,1)`, page); err != nil {
			t.Fatal(err)
		}
	}
	dump, err := DumpWorkspace(ctx, db, wanted)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"page_project_drafts", "page_project_revisions", "page_project_builds", "page_project_publications", "page_project_live"} {
		rows := dump.Tables[table]
		if len(rows) != 1 || rows[0]["page_id"] != "page-project-one" {
			t.Fatalf("%s: wrong workspace rows %v", table, rows)
		}
	}
	target := openMigratedDBCov(t)
	if err := RestoreDump(ctx, target, dump); err != nil {
		t.Fatal(err)
	}
	if err := target.QueryRow(`SELECT version FROM page_project_live WHERE page_id='page-project-one'`).Scan(new(int)); err != nil {
		t.Fatal(err)
	}
	var digest string
	if err := target.QueryRow(`SELECT source_digest FROM page_project_drafts WHERE page_id='page-project-one'`).Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if digest != strings.Repeat("a", 64) {
		t.Fatal("draft source pointer lost")
	}
	for _, table := range []string{"page_project_revisions", "page_project_builds"} {
		var actor string
		if err := target.QueryRow("SELECT json_extract(actor_json,'$.agent_id') FROM " + table).Scan(&actor); err != nil || actor != "agent-author" {
			t.Fatalf("%s agent provenance lost: %q %v", table, actor, err)
		}
	}
	var count int
	if err := target.QueryRow(`SELECT count(*) FROM page_project_revisions`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("audit restore %d: %v", count, err)
	}
}
