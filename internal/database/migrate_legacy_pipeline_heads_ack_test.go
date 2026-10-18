package database

import (
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
)

// A one-time cutover must acknowledge the actual surviving native archive.
// These triggers obey the real T1 constraints; no schema/FK bypass is used.
func TestLegacyCurrentHeadCutoverRequiresPersistedInsertAcknowledgement(t *testing.T) {
	for _, tc := range []struct{ name, timing, action string }{
		{"ignored-insert", "BEFORE", "SELECT RAISE(IGNORE);"},
		{"deleted-after-insert", "AFTER", "DELETE FROM pipeline_versions WHERE id=NEW.id;"},
		{"changed-after-insert", "AFTER", "UPDATE pipeline_versions SET definition_json='{}',definition_hash='changed-native-content' WHERE id=NEW.id;"},
		{"blob-after-insert", "AFTER", "UPDATE pipeline_versions SET definition_json=CAST(definition_json AS BLOB) WHERE id=NEW.id;"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ENCRYPTION_KEY", strings.Repeat("a1b2c3d4", 8))
			db := migrationBoundaryDB(t, `PRAGMA foreign_keys=ON`)
			var fk int
			if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
				t.Fatalf("SETUP native FK: %d %v", fk, err)
			}
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			foundation, cutover := 0, 0
			for _, m := range migrations {
				if m.name == "pipeline_version_authority_identity" {
					foundation = m.version
				}
				if m.name == "legacy_pipeline_current_heads" {
					cutover = m.version
				}
			}
			if foundation == 0 || cutover <= foundation {
				t.Fatal("SETUP actual ordered migrations absent")
			}
			if err := applyMigrationsUpTo(t.Context(), db, foundation, logger); err != nil {
				t.Fatalf("SETUP T1: %v", err)
			}
			mustExecCtx(t, db, t.Context(), `INSERT INTO workspaces(id,name,slug) VALUES('g1-ack-w','ACK','g1-ack-w')`)
			raw := " {\"name\":\"exact ACK capture\",\"steps\":[]} "
			hash := fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))
			ids := []string{"g1-ack-a-first", "g1-ack-z-later"}
			before := map[string][]any{}
			for _, id := range ids {
				mustExecCtx(t, db, t.Context(), `INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash,head_version) VALUES(?,'g1-ack-w',?,'ACK',?,?,7)`, id, id, raw, hash)
				before[id] = legacyHeadNativeRow(t, db, id)
			}
			mustExecCtx(t, db, t.Context(), `CREATE TRIGGER g1_ack_fault `+tc.timing+` INSERT ON pipeline_versions WHEN NEW.pipeline_id='g1-ack-z-later' BEGIN `+tc.action+` END`)
			err := Migrate(t.Context(), db, logger)
			if err == nil {
				t.Error("one-time cutover committed an ignored/deleted/altered archive")
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM pipeline_versions WHERE pipeline_id IN ('g1-ack-a-first','g1-ack-z-later')`).Scan(&count); err != nil || count != 0 {
				t.Errorf("partial archives survived: %d %v", count, err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM _migrations WHERE version=?`, cutover).Scan(&count); err != nil || count != 0 {
				t.Errorf("cutover ledger survived failed acknowledgement: %d %v", count, err)
			}
			for _, id := range ids {
				if !reflect.DeepEqual(before[id], legacyHeadNativeRow(t, db, id)) {
					t.Errorf("source changed: %s", id)
				}
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("FK violation: %d %v", count, err)
			}
			if t.Failed() {
				return
			} // Preserve semantic RED before any retry assertions.
			mustExecCtx(t, db, t.Context(), `DROP TRIGGER g1_ack_fault`)
			if err := Migrate(t.Context(), db, logger); err != nil {
				t.Fatalf("clean retry: %v", err)
			}
			for _, id := range ids {
				var archiveID string
				if err := db.QueryRow(`SELECT id FROM pipeline_versions WHERE pipeline_id=? AND version=7 AND typeof(id)='text' AND typeof(version)='integer' AND typeof(definition_json)='text' AND typeof(definition_hash)='text' AND definition_json=? AND definition_hash=? AND chat_origin_id IS NULL AND authority_fingerprint='legacy-trusted:v1' AND author_type='system' AND author_id='legacy-upgrade' AND parent_version IS NULL AND change_summary='Current legacy definition archived during upgrade'`, id, raw, hash).Scan(&archiveID); err != nil || archiveID == "" {
					t.Errorf("clean retry not exact native archive: %s %v", id, err)
				}
				if !reflect.DeepEqual(before[id], legacyHeadNativeRow(t, db, id)) {
					t.Errorf("clean retry changed source: %s", id)
				}
			}
		})
	}
}
