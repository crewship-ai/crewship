package database

import (
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func artifactBoardMigrationFixture(t *testing.T) ([]migration, []migration, []migration) {
	t.Helper()
	var fixture struct {
		Migrations []struct {
			Version int    `json:"version"`
			Name    string `json:"name"`
			SQL     string `json:"sql"`
		} `json:"migrations"`
	}
	raw, err := os.ReadFile("testdata/artifact_board_prerelease_migrations.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	var baseline, old, feature []migration
	for _, m := range migrations {
		if m.version < artifactBoardFirstMigration {
			baseline = append(baseline, m)
		} else if m.version <= artifactBoardConsolidatedVersions[2] {
			feature = append(feature, m)
		}
	}
	for _, m := range fixture.Migrations {
		old = append(old, migration{version: m.Version, name: m.Name, sql: m.SQL})
	}
	if len(old) != 33 || len(feature) != 3 {
		t.Fatalf("invalid migration fixture: old=%d consolidated=%d", len(old), len(feature))
	}
	return baseline, old, feature
}

func artifactBoardApplyFixture(t *testing.T, registry []migration) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "prerelease.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err = ensureMigrationsTable(t.Context(), db.DB); err != nil {
		t.Fatal(err)
	}
	sort.Slice(registry, func(i, j int) bool { return registry[i].version < registry[j].version })
	if err = applyRegistry(t.Context(), db.DB, registry, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal("apply original real migration programs", err)
	}
	return db
}

func artifactBoardMainSnapshot(t *testing.T, baseline []migration) string {
	t.Helper()
	db := artifactBoardApplyFixture(t, append([]migration{}, baseline...))
	path := filepath.Join(t.TempDir(), "shipped-main-snapshot.db")
	if err := SnapshotTo(t.Context(), db.DB, path); err != nil {
		t.Fatal(err)
	}
	return path
}

func artifactBoardCloneMain(t *testing.T, snapshot string, registry []migration) *DB {
	t.Helper()
	raw, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "prerelease.db")
	if err = os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sort.Slice(registry, func(i, j int) bool { return registry[i].version < registry[j].version })
	if err = applyRegistry(t.Context(), db.DB, registry, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal("apply original SQL to fresh shipped-main snapshot", err)
	}
	return db
}

func artifactBoardRowSnapshot(t *testing.T, db *sql.DB, ledgerOnly bool) map[string][]string {
	t.Helper()
	schema, err := readArtifactBoardSchema(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	result := map[string][]string{}
	for _, obj := range schema {
		if obj.kind != "table" {
			continue
		}
		if ledgerOnly {
			if obj.name != "_migrations" {
				continue
			}
		} else if !artifactBoardFeatureNamespace(obj.name) && !strings.Contains("|page_grants|pages|users|workspaces|crews|agents|chats|", "|"+obj.name+"|") {
			continue
		}
		rows, err := db.Query(`SELECT * FROM "` + strings.ReplaceAll(obj.name, `"`, `""`) + `"`)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		var saved []string
		for rows.Next() {
			values, pointers := make([]any, len(columns)), make([]any, len(columns))
			for i := range pointers {
				pointers[i] = &values[i]
			}
			if err = rows.Scan(pointers...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			raw, err := json.Marshal(values)
			if err != nil {
				rows.Close()
				t.Fatal(err)
			}
			saved = append(saved, string(raw))
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		sort.Strings(saved)
		result[obj.name] = saved
	}
	return result
}

func artifactBoardSeedPrereleaseRows(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO users(id,email) VALUES('owner','owner@example.invalid'),('reader','reader@example.invalid');
INSERT INTO workspaces(id,name,slug) VALUES('ws','Workspace','workspace');
INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('artifact','ws','artifact','Artifact','owner','{}'),('board','ws','board','Board','owner','{}');
INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES('artifact','user','reader','comment','owner','2000'),('artifact','agent','named-agent','comment','owner','2000');
INSERT INTO page_artifacts(page_id,head_rev) VALUES('artifact',1);
INSERT INTO page_artifact_revisions(id,page_id,revision,sha256,author_user_id,mutation_id,request_digest,created_at) VALUES('revision','artifact',1,'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','owner','publish','digest','2000');
INSERT INTO page_artifact_mutation_receipts(page_id,mutation_id,revision,request_digest,author_user_id,created_at) VALUES('artifact','publish',1,'digest','owner','2000');
INSERT INTO artifact_workspace_writes(id,workspace_id,created_at) VALUES('quota','ws','2000');
INSERT INTO page_artifact_threads(id,page_id,revision,anchor_json,created_by_user_id,created_at,updated_at) VALUES('artifact-thread','artifact',1,'{}','owner','2000','2000');
INSERT INTO page_artifact_comments(id,thread_id,thread_version,body,author_user_id,created_at) VALUES('artifact-comment','artifact-thread',1,'Private artifact comment','reader','2000');
INSERT INTO page_artifact_comment_history(id,page_id,thread_id,comment_id,version,body,action,actor_user_id,created_at) VALUES('artifact-history','artifact','artifact-thread','artifact-comment',1,'Private artifact comment','create','reader','2000');
INSERT INTO page_artifact_comment_mutations(id,page_id,thread_id,comment_id,mutation_id,request_digest,actor_user_id,operation,result_version,created_at) VALUES('artifact-receipt','artifact','artifact-thread','artifact-comment','comment','digest','reader','create',1,'2000');
INSERT INTO page_boards(page_id,seq,format) VALUES('board',1,'crewship-board-excalidraw-0.18.1-v1');
INSERT INTO page_board_elements(board_page_id,element_id,element_rev,element_json,author_user_id,updated_seq) VALUES('board','drawing',1,'{"id":"drawing","type":"rectangle","x":12,"y":24}','owner',1);
INSERT INTO page_board_files(id,board_page_id,sha256,content_type,width,height,byte_size,author_user_id,created_at,last_used_at) VALUES('file','board','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','image/png',4,3,99,'owner','2000','2001');
INSERT INTO page_board_artifact_placements(id,board_page_id,source_page_id,source_revision,x,y,width,height,updated_seq,author_user_id) VALUES('pin','board','artifact',1,0,0,100,100,1,'owner');
INSERT INTO page_board_mutations(board_page_id,mutation_id,author_user_id,fingerprint,result_seq,created_at,result_file_id,element_revs_json) VALUES('board','save','owner','digest',1,'2000','file','{"drawing":1}');
INSERT INTO page_board_write_audit(board_page_id,mutation_id,actor_user_id,result_seq,request_digest,created_at) VALUES('board','save','owner',1,'digest','2000');
INSERT INTO page_board_threads(id,page_id,anchor_json,created_by_user_id,created_at,updated_at) VALUES('board-thread','board','{}','owner','2000','2000');
INSERT INTO page_board_comments(id,thread_id,thread_version,body,author_user_id,created_at) VALUES('board-comment','board-thread',1,'Private board comment','reader','2000');
INSERT INTO page_board_comment_history(id,page_id,thread_id,comment_id,version,body,action,actor_user_id,created_at) VALUES('board-history','board','board-thread','board-comment',1,'Private board comment','create','reader','2000');
INSERT INTO page_board_comment_reactions(comment_id,user_id,emoji,created_at) VALUES('board-comment','reader','👍','2000');
INSERT INTO page_board_comment_mutations(id,page_id,thread_id,comment_id,mutation_id,request_digest,actor_user_id,operation,result_version,created_at,reaction_emoji,reaction_active) VALUES('board-receipt','board','board-thread','board-comment','react','digest','reader','react',2,'2000','👍',1);`)
	if err != nil {
		t.Fatal("seed original final-schema data", err)
	}
}

func TestArtifactBoardPrereleaseUpgradePreservesRowsAndLedgerThenMatchesFreshReset(t *testing.T) {
	baseline, old, feature := artifactBoardMigrationFixture(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mainSnapshot := artifactBoardMainSnapshot(t, baseline)
	fresh, err := Open(filepath.Join(t.TempDir(), "post-reset-fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if err = Migrate(t.Context(), fresh.DB, logger); err != nil {
		t.Fatal("fresh database after approved reset", err)
	}
	freshSchema := artifactBoardSchema(t, fresh.DB)
	for stage := 0; stage < 3; stage++ {
		t.Run(strconv.Itoa(stage)+"-consolidated-commits", func(t *testing.T) {
			db := artifactBoardCloneMain(t, mainSnapshot, append([]migration{}, old...))
			artifactBoardSeedPrereleaseRows(t, db.DB)
			if err := applyRegistry(t.Context(), db.DB, feature[:stage], logger); err != nil {
				t.Fatal("simulate committed prefix before process restart", err)
			}
			before := artifactBoardRowSnapshot(t, db.DB, false)
			oldLedger := artifactBoardRowSnapshot(t, db.DB, true)["_migrations"]
			// Reopen the persisted SQLite rather than reusing the same connection.
			path := db.Path()
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if err = Migrate(t.Context(), reopened.DB, logger); err != nil {
				t.Fatal("recognized prerelease upgrade", err)
			}
			if !reflect.DeepEqual(before, artifactBoardRowSnapshot(t, reopened.DB, false)) {
				t.Fatal("recognized upgrade changed existing source/Board/comment/quota/grant data")
			}
			afterLedger := artifactBoardRowSnapshot(t, reopened.DB, true)["_migrations"]
			for _, row := range oldLedger {
				if !slices.Contains(afterLedger, row) {
					t.Fatal("existing migration ledger row changed", row)
				}
			}
			if len(afterLedger) != len(oldLedger)+3-stage {
				t.Fatal("upgrade added anything other than the pending consolidated ledger rows")
			}
			if !reflect.DeepEqual(artifactBoardSchema(t, reopened.DB), freshSchema) {
				t.Fatal("compatibility upgrade and fresh-reset schemas differ")
			}
		})
	}
}

func TestArtifactBoardPrereleaseMismatchRefusesAllWrites(t *testing.T) {
	baseline, old, _ := artifactBoardMigrationFixture(t)
	mainSnapshot := artifactBoardMainSnapshot(t, baseline)
	for _, name := range []string{"partial-old", "wrong-name", "unknown-old-version", "changed-check", "literal-case", "changed-trigger", "changed-index", "no-feature-ledger"} {
		t.Run(name, func(t *testing.T) {
			legacy := append([]migration{}, old...)
			if name == "partial-old" {
				legacy = legacy[:len(legacy)-1]
			}
			if name == "wrong-name" {
				legacy[0].name = "unrecognized_prerelease_artifacts"
			}
			if name == "unknown-old-version" {
				legacy = append(legacy, migration{version: 20261013193202, name: "unrecognized_feature", sql: "SELECT 1;"})
			}
			if name == "literal-case" {
				for i := range legacy {
					if legacy[i].name == "board_requests" {
						legacy[i].sql = strings.ReplaceAll(legacy[i].sql, "kind IN ('diagram','artifact')", "kind IN ('DIAGRAM','artifact')")
					}
				}
			}
			if name == "changed-check" {
				for i := range legacy {
					if legacy[i].name == "board_comment_lifecycle" {
						legacy[i].sql = strings.ReplaceAll(legacy[i].sql, "CHECK(version>0)", "CHECK(version>=0)")
					}
				}
			}
			var registry []migration
			if name != "no-feature-ledger" {
				registry = append(registry, legacy...)
			}
			db := artifactBoardCloneMain(t, mainSnapshot, registry)
			if name == "no-feature-ledger" {
				// Actual original SQL creates the shape without inventing or deleting
				// feature ledger rows. A copied schema is not an admitted upgrade.
				for _, m := range legacy {
					if _, err := db.Exec(m.sql); err != nil {
						t.Fatal(err)
					}
				}
			}
			artifactBoardSeedPrereleaseRows(t, db.DB)
			if name == "changed-trigger" {
				if _, err := db.Exec(`DROP TRIGGER board_comment_mutation_immutable; CREATE TRIGGER board_comment_mutation_immutable BEFORE UPDATE ON page_board_comment_mutations BEGIN SELECT 1; END;`); err != nil {
					t.Fatal(err)
				}
			}
			if name == "changed-index" {
				if _, err := db.Exec(`DROP INDEX idx_artifact_revision_source_chat; CREATE INDEX idx_artifact_revision_source_chat ON page_artifact_revisions(source_chat_id,created_at ASC);`); err != nil {
					t.Fatal(err)
				}
			}
			beforeSchema := artifactBoardSchema(t, db.DB)
			beforeRows := artifactBoardRowSnapshot(t, db.DB, false)
			beforeLedger := artifactBoardRowSnapshot(t, db.DB, true)
			err := Migrate(t.Context(), db.DB, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err == nil || !strings.Contains(err.Error(), "refusing") {
				t.Fatalf("unrecognized schema did not fail closed: %v", err)
			}
			if !reflect.DeepEqual(beforeSchema, artifactBoardSchema(t, db.DB)) || !reflect.DeepEqual(beforeRows, artifactBoardRowSnapshot(t, db.DB, false)) || !reflect.DeepEqual(beforeLedger, artifactBoardRowSnapshot(t, db.DB, true)) {
				t.Fatal("failed preflight changed schema, data or any migration ledger row")
			}
		})
	}
}

func TestArtifactBoardUntrackedSchemaDoesNotCreateLedger(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "impostor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TABLE page_boards(page_id TEXT)`); err != nil {
		t.Fatal(err)
	}
	err = Migrate(t.Context(), db.DB, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("untracked schema accepted: %v", err)
	}
	var ledger int
	if err = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='_migrations'`).Scan(&ledger); err != nil || ledger != 0 {
		t.Fatalf("preflight wrote a ledger before refusing: ledger=%d err=%v", ledger, err)
	}
}

func TestArtifactBoardFreshConsolidationResumesCommittedPrefixes(t *testing.T) {
	baseline, _, feature := artifactBoardMigrationFixture(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mainSnapshot := artifactBoardMainSnapshot(t, baseline)
	for stage := 1; stage < 3; stage++ {
		t.Run(strconv.Itoa(stage)+"-commits", func(t *testing.T) {
			db := artifactBoardCloneMain(t, mainSnapshot, append([]migration{}, feature[:stage]...))
			if err := Migrate(t.Context(), db.DB, logger); err != nil {
				t.Fatal("fresh installation could not resume its committed prefix", err)
			}
			if err := Migrate(t.Context(), db.DB, logger); err != nil {
				t.Fatal("completed consolidation did not replay cleanly", err)
			}
		})
	}
}
