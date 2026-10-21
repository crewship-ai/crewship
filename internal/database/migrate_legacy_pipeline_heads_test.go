package database

import (
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
)

// An old physical HEAD is not evidence of historical versions. The cutover
// may archive only that exact current definition, leaving all numeric pins.
func TestLegacyCurrentHeadUpgradeArchivesOnlyVerifiedPositiveHeads(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("a1b2c3d4", 8))
	db := migrationBoundaryDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	var foundation int
	for _, m := range migrations {
		if m.name == "pipeline_version_authority_identity" {
			foundation = m.version
		}
	}
	if foundation == 0 {
		t.Fatal("authority foundation missing")
	}
	if err := applyMigrationsUpTo(t.Context(), db, foundation, logger); err != nil {
		t.Fatal(err)
	}
	mustExecCtx(t, db, t.Context(), `INSERT INTO workspaces(id,name,slug) VALUES('legacy-head-ws','Legacy fixture','legacy-head-ws')`)
	mustExecCtx(t, db, t.Context(), `INSERT INTO execution_origins(id,workspace_id,root_run_id,chat_id,trigger_message_id,human_authority,created_at) VALUES('legacy-head-origin','legacy-head-ws','root','chat','message','{}','2026-10-01T00:00:00Z')`)
	raw := ` {"name":"known current","steps":[]} `
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))
	for _, fixture := range []struct {
		id   string
		head int
	}{{"legacy-one", 1}, {"legacy-seven", 7}, {"legacy-invalidhash", 1}, {"legacy-zero", 0}, {"corrupt-existing", 99}, {"bounded-existing", 2}} {
		mustExecCtx(t, db, t.Context(), `INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash,head_version,chat_origin_id) VALUES(?,'legacy-head-ws',?,'Fixture',?,?,?,'legacy-head-origin')`, fixture.id, fixture.id, raw, hash, fixture.head)
	}
	mustExecCtx(t, db, t.Context(), `UPDATE pipelines SET definition_hash='inconsistent' WHERE id='legacy-invalidhash'`)
	mustExecCtx(t, db, t.Context(), `INSERT INTO pipeline_versions(id,pipeline_id,version,definition_json,definition_hash,author_type,author_id,authority_fingerprint) VALUES('old-corrupt','corrupt-existing',1,'{}','old-content','system','fixture','legacy-trusted:v1'),('old-bounded','bounded-existing',1,'{}','bounded-content','system','fixture',?)`, strings.Repeat("a", 64))
	mustExecCtx(t, db, t.Context(), `INSERT INTO pipeline_runs(id,workspace_id,pipeline_id,pipeline_slug,pipeline_version,status,started_at) VALUES('legacy-unknown-run','legacy-head-ws','legacy-one','legacy-one',NULL,'completed','2026-10-01T00:00:00Z')`)
	mustExecCtx(t, db, t.Context(), `INSERT INTO pipeline_schedules(id,workspace_id,name,target_pipeline_id,target_pipeline_version,cron_expr,enabled) VALUES('legacy-pinned','legacy-head-ws','Unchanged pin','legacy-seven',7,'0 8 * * *',0)`)
	var beforeRevision int
	if err := db.QueryRow(`SELECT publication_revision FROM pipelines WHERE id='legacy-one'`).Scan(&beforeRevision); err != nil {
		t.Fatal(err)
	}
	mustExecCtx(t, db, t.Context(), `CREATE TRIGGER fail_legacy_capture BEFORE INSERT ON pipeline_versions WHEN NEW.pipeline_id='legacy-seven' BEGIN SELECT RAISE(ABORT,'synthetic legacy archive failure'); END`)
	if err := Migrate(t.Context(), db, logger); err == nil {
		t.Fatal("synthetic archive failure ignored")
	}
	var partial int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pipeline_versions WHERE pipeline_id IN ('legacy-one','legacy-seven')`).Scan(&partial); err != nil || partial != 0 {
		t.Fatalf("partial cutover survived rollback: %d %v", partial, err)
	}
	mustExecCtx(t, db, t.Context(), `DROP TRIGGER fail_legacy_capture`)
	if err := Migrate(t.Context(), db, logger); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []struct {
		id   string
		head int
	}{{"legacy-one", 1}, {"legacy-seven", 7}} {
		var count, head, revision int
		var definition, storedHash, origin, authority, authorType, authorID, summary string
		if err := db.QueryRow(`SELECT COUNT(*) FROM pipeline_versions WHERE pipeline_id=?`, expected.id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("verified unversioned %s: archived %d known current heads, want exactly 1", expected.id, count)
		}
		if err := db.QueryRow(`SELECT v.version,v.definition_json,v.definition_hash,COALESCE(v.chat_origin_id,''),v.authority_fingerprint,v.author_type,v.author_id,COALESCE(v.change_summary,''),p.publication_revision FROM pipeline_versions v JOIN pipelines p ON p.id=v.pipeline_id WHERE p.id=? AND v.version=p.head_version`, expected.id).Scan(&head, &definition, &storedHash, &origin, &authority, &authorType, &authorID, &summary, &revision); err != nil {
			t.Fatal(err)
		}
		if head != expected.head || definition != raw || storedHash != hash || origin != "legacy-head-origin" || authority != "legacy-trusted:v1" || revision != beforeRevision {
			t.Fatalf("current head changed: %d %q %s %s %s revision%d", head, definition, storedHash, origin, authority, revision)
		}
		if authorType != "system" || authorID == "" || !strings.Contains(summary, "upgrade") {
			t.Fatalf("capture misrepresented as historical authorship: %s/%s/%s", authorType, authorID, summary)
		}
	}
	var changed int
	for _, query := range []string{
		`SELECT COUNT(*) FROM pipeline_versions WHERE pipeline_id IN ('legacy-zero','legacy-invalidhash')`,
		`SELECT COUNT(*) FROM pipeline_versions WHERE pipeline_id IN ('corrupt-existing','bounded-existing') AND id NOT IN ('old-corrupt','old-bounded')`,
		`SELECT COUNT(*) FROM pipeline_runs WHERE id='legacy-unknown-run' AND pipeline_version IS NOT NULL`,
		`SELECT COUNT(*) FROM pipeline_schedules WHERE id='legacy-pinned' AND target_pipeline_version<>7`,
		`SELECT COUNT(*) FROM pragma_foreign_key_check`,
	} {
		if err := db.QueryRow(query).Scan(&changed); err != nil || changed != 0 {
			t.Fatalf("boundary changed: %s count%d err%v", query, changed, err)
		}
	}
	// This is a cutover, not a new-row fallback or a standing grandfather rule.
	mustExecCtx(t, db, t.Context(), `INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash) VALUES('future-missing','legacy-head-ws','future-missing','Fixture','{}','future')`)
	if err := Migrate(t.Context(), db, logger); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM pipeline_versions WHERE pipeline_id='future-missing'`).Scan(&changed); err != nil || changed != 0 {
		t.Fatalf("future missing authority inferred: %d %v", changed, err)
	}
}
