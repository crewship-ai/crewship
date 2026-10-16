package database

import (
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

func staticPagesMainBaseline(t *testing.T, beforeLegacy bool) (*DB, *slog.Logger) {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "static-pages.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err = ensureMigrationsTable(t.Context(), db.DB); err != nil {
		t.Fatal(err)
	}
	var baseline []migration
	for _, m := range migrations {
		if m.name != "static_pages_foundation" && (!beforeLegacy || m.version < 20261014114745) {
			baseline = append(baseline, m)
		}
	}
	if err = applyRegistry(t.Context(), db.DB, baseline, logger); err != nil {
		t.Fatal(err)
	}
	return db, logger
}

func staticPagesExec(t *testing.T, db *sql.DB, s string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), s, args...); err != nil {
		t.Fatal(err)
	}
}

func staticPagesSeedLegacy(t *testing.T, db *sql.DB) {
	t.Helper()
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i, ddl := range []string{staticPageCommentGrantsDDL, staticPageArtifactsDDL, staticPageBoardsDDL} {
		if _, err = tx.ExecContext(t.Context(), ddl); err != nil {
			t.Fatal(err)
		}
		name := []string{"page_comment_grants", "page_artifacts", "page_boards"}[i]
		if _, err = tx.ExecContext(t.Context(), `INSERT INTO _migrations(version,name,applied_at) VALUES(?,?,'2026-10-14 13:45:28')`, 20261014114745+i, name); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	staticPagesExec(t, db, `INSERT INTO users(id,email) VALUES('legacy-owner','legacy-owner@example.invalid'),('legacy-reader','legacy-reader@example.invalid');
 INSERT INTO workspaces(id,name,slug) VALUES('legacy-ws','Legacy','legacy-ws');
 INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES
 ('legacy-artifact','legacy-ws','legacy-artifact','Legacy Artifact','legacy-owner','{}'),
 ('legacy-board','legacy-ws','legacy-board','Legacy Board','legacy-owner','{}');
 INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES
 ('legacy-artifact','user','legacy-reader','read','legacy-owner','2026-10-14T13:45:28Z'),
 ('legacy-artifact','user','legacy-reader','comment','legacy-owner','2026-10-14T13:45:29Z');
 INSERT INTO page_artifacts(page_id,head_rev) VALUES('legacy-artifact',1);
 INSERT INTO page_artifact_revisions(id,page_id,revision,sha256,author_user_id,mutation_id,request_digest,created_at) VALUES
 ('legacy-rev','legacy-artifact',1,'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','legacy-owner','legacy-mutation','legacy-digest','2026-10-14T13:45:28Z');
 INSERT INTO page_boards(page_id,seq,format) VALUES('legacy-board',0,'crewship-board-excalidraw-0.18.1-v1');
 INSERT INTO page_board_artifact_placements(id,board_page_id,source_page_id,source_revision,x,y,width,height,updated_seq,author_user_id) VALUES
 ('legacy-pin','legacy-board','legacy-artifact',1,10,20,300,200,0,'legacy-owner');`)
}

func TestStaticPagesLegacyUpgradePreservesDataPinsAndHistory(t *testing.T) {
	for _, before := range []bool{false, true} {
		t.Run(map[bool]string{false: "current-main", true: "dev4-order"}[before], func(t *testing.T) {
			db, logger := staticPagesMainBaseline(t, before)
			staticPagesSeedLegacy(t, db.DB)
			// Unrelated main objects and an extra main-owned grant index do not change
			// the feature constraints and must not make discovery compare the whole DB.
			staticPagesExec(t, db.DB, `CREATE TABLE main_unrelated_probe(id TEXT); CREATE INDEX main_grants_probe ON page_grants(level)`)
			beforeSchema := artifactBoardSchema(t, db.DB)
			for n := 0; n < 2; n++ {
				if err := Migrate(t.Context(), db.DB, logger); err != nil {
					t.Fatal("upgrade/reboot", err)
				}
			}
			afterSchema := artifactBoardSchema(t, db.DB)
			var contract struct {
				Objects map[string]string `json:"objects"`
			}
			if err := json.Unmarshal(staticPagesLegacySchema, &contract); err != nil {
				t.Fatal(err)
			}
			// Earlier main migrations may legitimately alter their own objects
			// (for example mission_comments). Only the owned feature contract
			// and the deliberately harmless main additions must stay unchanged.
			for key, want := range beforeSchema {
				_, owned := contract.Objects[key]
				if !owned && key != "index main_grants_probe" && key != "table main_unrelated_probe" {
					continue
				}
				if got := afterSchema[key]; got != want {
					t.Fatalf("existing owned object changed %s", key)
				}
			}
			var n int
			if err := db.QueryRow(`SELECT COUNT(*) FROM _migrations WHERE version IN(20261014114745,20261014114746,20261014114747) AND applied_at='2026-10-14 13:45:28'`).Scan(&n); err != nil || n != 3 {
				t.Fatalf("legacy ledger not preserved %d %v", n, err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM _migrations WHERE version=? AND name='static_pages_foundation'`, staticPagesFoundationVersion).Scan(&n); err != nil || n != 1 {
				t.Fatalf("new ledger identity %d %v", n, err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM page_grants WHERE page_id='legacy-artifact' AND subject_id='legacy-reader'`).Scan(&n); err != nil || n != 2 {
				t.Fatalf("grants lost %d %v", n, err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM page_board_artifact_placements p JOIN page_artifact_revisions r ON r.page_id=p.source_page_id AND r.revision=p.source_revision WHERE p.id='legacy-pin' AND r.id='legacy-rev' AND p.width=300`).Scan(&n); err != nil || n != 1 {
				t.Fatalf("content/source pin lost %d %v", n, err)
			}
			rows, err := db.Query(`PRAGMA foreign_key_check`)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			if rows.Next() {
				t.Fatal("foreign key violation")
			}
		})
	}
}

func TestStaticPagesLegacyMismatchRefusesWithoutChangingData(t *testing.T) {
	for _, tc := range []struct{ name, sql, want string }{
		{"one-ID", `DELETE FROM _migrations WHERE version IN(20261014114746,20261014114747)`, "partial legacy"},
		{"two-IDs", `DELETE FROM _migrations WHERE version=20261014114747`, "partial legacy"},
		{"wrong-name", `UPDATE _migrations SET name='different-feature' WHERE version=20261014114746`, "identity mismatch"},
		{"missing-grant-subject-index", `DROP INDEX idx_page_grants_subject`, "object missing"},
		{"missing-grant-author-index", `DROP INDEX idx_page_grants_granted_by`, "object missing"},
		{"missing-index", `DROP INDEX idx_artifact_comment_agent`, "object missing"},
		{"different-constraint", `ALTER TABLE page_board_artifact_placements ADD COLUMN unexpected TEXT`, "schema mismatch"},
		{"unexpected-owned-trigger", `CREATE TRIGGER legacy_extra_trigger BEFORE INSERT ON page_artifacts BEGIN SELECT RAISE(ABORT,'unexpected'); END`, "unexpected legacy"},
		{"no-ledger", `DELETE FROM _migrations WHERE version IN(20261014114745,20261014114746,20261014114747)`, "without legacy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, logger := staticPagesMainBaseline(t, false)
			staticPagesSeedLegacy(t, db.DB)
			staticPagesExec(t, db.DB, tc.sql)
			beforeSchema := artifactBoardSchema(t, db.DB)
			err := Migrate(t.Context(), db.DB, logger)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
			afterSchema := artifactBoardSchema(t, db.DB)
			if len(beforeSchema) != len(afterSchema) {
				t.Fatal("failed migration changed schema count")
			}
			for k, v := range beforeSchema {
				if afterSchema[k] != v {
					t.Fatal("failed migration changed schema", k)
				}
			}
			var n int
			if err = db.QueryRow(`SELECT COUNT(*) FROM _migrations WHERE version=?`, staticPagesFoundationVersion).Scan(&n); err != nil || n != 0 {
				t.Fatalf("failed migration recorded %d %v", n, err)
			}
			if err = db.QueryRow(`SELECT COUNT(*) FROM page_board_artifact_placements WHERE id='legacy-pin'`).Scan(&n); err != nil || n != 1 {
				t.Fatalf("failed migration lost pin %d %v", n, err)
			}
		})
	}
}

func TestStaticPagesFreshFailureRollsBackDDLAndLedger(t *testing.T) {
	db, logger := staticPagesMainBaseline(t, false)
	staticPagesExec(t, db.DB, `CREATE TRIGGER refuse_static_pages_ledger BEFORE INSERT ON _migrations WHEN NEW.name='static_pages_foundation' BEGIN SELECT RAISE(ABORT,'fixture ledger failure'); END`)
	before := artifactBoardSchema(t, db.DB)
	if err := Migrate(t.Context(), db.DB, logger); err == nil || !strings.Contains(err.Error(), "fixture ledger failure") {
		t.Fatalf("want ledger failure, got %v", err)
	}
	after := artifactBoardSchema(t, db.DB)
	if len(before) != len(after) {
		t.Fatal("fresh failure leaked schema objects")
	}
	for k, v := range before {
		if after[k] != v {
			t.Fatal("fresh failure changed schema", k)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM _migrations WHERE version=?`, staticPagesFoundationVersion).Scan(&n); err != nil || n != 0 {
		t.Fatalf("failure ledger %d %v", n, err)
	}
	staticPagesExec(t, db.DB, `DROP TRIGGER refuse_static_pages_ledger`)
	if err := Migrate(t.Context(), db.DB, logger); err != nil {
		t.Fatal("recovery after failed transaction", err)
	}
}
