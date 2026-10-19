package backup

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/testutil"
)

func TestPrivateArtifactMarkerUserReconciliation(t *testing.T) {
	for _, marker := range []any{"source-user", "", nil} {
		t.Run(map[bool]string{true: "known", false: "unknown"}[marker == "source-user"]+strings.Repeat("_", map[bool]int{true: 1, false: 0}[marker == nil]), func(t *testing.T) {
			db := newReconcileTestDB(t)
			if _, err := db.Exec(`INSERT INTO users(id,email) VALUES('target-user','same@example.test')`); err != nil {
				t.Fatal(err)
			}
			dump := &DBDump{WorkspaceID: "source-ws", Tables: map[string][]map[string]any{
				"users":      {{"id": "source-user", "email": "same@example.test"}},
				"workspaces": {{"id": "source-ws", "slug": "source"}},
				"chats":      {{"id": "source-chat", "workspace_id": "source-ws", "created_by": "source-user", "private_artifact_author_user_id": marker}},
			}}
			if err := RemapIDs(t.Context(), db, dump); err != nil {
				t.Fatal(err)
			}
			if dump.Tables["chats"][0]["id"] == "source-chat" || dump.Tables["workspaces"][0]["id"] == "source-ws" {
				t.Fatal("actual fork did not remap chat/workspace")
			}
			tx, err := db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := ReconcileUsersByEmail(t.Context(), tx, dump); err != nil {
				t.Fatal(err)
			}
			row := dump.Tables["chats"][0]
			want := marker
			if marker == "source-user" {
				want = "target-user"
			}
			if row["created_by"] != "target-user" || row["private_artifact_author_user_id"] != want {
				t.Errorf("fork/email identity lost: creator=%v marker=%v want=%v", row["created_by"], row["private_artifact_author_user_id"], want)
			}
		})
	}
}

func TestPrivateArtifactPortableRestorePreflight(t *testing.T) {
	for _, kind := range []string{"artifact era missing marker", "artifact era missing marker and visibility", "artifact era group missing marker", "pre artifacts ordinary", "new ordinary explicit null", "new protected into old target"} {
		t.Run(kind, func(t *testing.T) {
			source := openMigratedDBCov(t)
			ws, _ := seedCovWorkspace(t, source, "private-marker-dump")
			if _, err := source.Exec(`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('source-chat',?,'a_cov_private-marker-dump','u_cov_private-marker-dump','private')`, ws); err != nil {
				t.Fatal(err)
			}
			dump, err := DumpWorkspace(t.Context(), source, ws)
			if err != nil {
				t.Fatal(err)
			}
			// Model the serialized old/new schema shape on a real source dump, without
			// inferring content classification from messages or provider metadata.
			dump.Tables["document_creations"] = []map[string]any{}
			for _, row := range dump.Tables["chats"] {
				delete(row, "private_artifact_author_user_id")
				if kind == "artifact era missing marker and visibility" {
					delete(row, "visibility")
				}
				if kind == "artifact era group missing marker" {
					row["visibility"] = "group"
				}
				if kind == "new ordinary explicit null" {
					row["private_artifact_author_user_id"] = nil
				}
				if kind == "new protected into old target" {
					row["private_artifact_author_user_id"] = row["created_by"]
				}
			}
			if kind == "pre artifacts ordinary" {
				delete(dump.Tables, "document_creations")
			}
			target := openMigratedDBCov(t)
			if kind == "new protected into old target" {
				target = testutil.NewMemDB(t)
				if err := database.MigrateSkipping(t.Context(), target, slog.New(slog.NewTextHandler(io.Discard, nil)), "private_artifact_conversation_context"); err != nil {
					t.Fatal(err)
				}
			}
			touched := false
			hooks := &RestoreDumpHooks{PreInsert: func(context.Context, *sql.Tx) error { touched = true; return nil }}
			if kind == "pre artifacts ordinary" {
				hooks.legacySourceVersions = []int{169}
			}
			_, err = RestoreDumpTxHooks(t.Context(), target, dump, hooks)
			unsafe := strings.HasPrefix(kind, "artifact era") || kind == "new protected into old target"
			if unsafe {
				if err == nil || !strings.Contains(err.Error(), "private artifact conversation classification") {
					t.Errorf("ambiguous artifact-era bundle was not rejected specifically: %v", err)
				}
				if touched {
					t.Error("restore mutation hook ran before private classification preflight")
				}
				var count int
				if queryErr := target.QueryRow(`SELECT count(*) FROM workspaces WHERE id=?`, ws).Scan(&count); queryErr != nil || count != 0 {
					t.Errorf("ambiguous restore changed target: rows=%d err=%v", count, queryErr)
				}
			} else if err != nil || !touched {
				t.Errorf("compatible ordinary source rejected: err=%v touched=%v", err, touched)
			}
		})
	}
}

func TestPrivateArtifactNoKitRecoveryPreservesUnbackfilledProof(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "")
	dir := t.TempDir()
	db, err := database.Open("file:" + filepath.Join(dir, "crewship.db"))
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	err = database.MigrateSkipping(t.Context(), db.DB, logger, "private_artifact_conversation_context")
	if err != nil && strings.Contains(err.Error(), "no migration is named") {
		// Tests-first predecessor predates this migration. Future qualification
		// skips ONLY the new privacy migration; shipped registries are not edited.
		err = database.Migrate(t.Context(), db.DB, logger)
	}
	if err != nil {
		t.Fatal(err)
	}
	ws, _ := seedCovWorkspace(t, db.DB, "no-kit-private")
	for _, q := range []string{
		`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('old-private-chat','ws_cov_no-kit-private','a_cov_no-kit-private','u_cov_no-kit-private','private')`,
		`INSERT INTO conversation_messages(id,session_id,agent_id,role,content,author_user_id,ts) VALUES('old-private-message','old-private-chat','a_cov_no-kit-private','user','Create private output','u_cov_no-kit-private','2026-10-19T00:00:00Z')`,
		`INSERT INTO document_creation_consents(message_id,workspace_id,human_user_id,chat_id,submission_id,created_at) VALUES('old-private-message','ws_cov_no-kit-private','u_cov_no-kit-private','old-private-chat','original','2026-10-19T00:00:00Z')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	_, _, pending, err := database.PendingMigrations(t.Context(), db.DB)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("pending=%d (pre-migration baseline may be zero)", pending)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	report := &RecoverReport{DatabasePath: filepath.Join(dir, "crewship.db"), Stores: map[string]StoreRestore{}}
	err = finishRecoveredDatabase(t.Context(), report, nil, dir, logger)
	if err == nil || !strings.Contains(err.Error(), "private artifact conversation classification") {
		t.Errorf("unsafe deferred privacy migration did not stop recovery: %v", err)
	}
	restored, openErr := database.Open("file:" + report.DatabasePath)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer restored.Close()
	var count int
	if err := restored.QueryRow(`SELECT count(*) FROM document_creation_consents WHERE workspace_id=?`, ws).Scan(&count); err != nil || count != 1 {
		t.Errorf("unbackfilled proof discarded: count=%d err=%v", count, err)
	}
}

// A real artifact-era crew dump contains chats but neither creation table.
// Its applied source registry, not table presence, distinguishes it from a
// pre-artifact bundle. No transcript/provider inference can recover the seal.
func TestPrivateArtifactCrewRestoreRequiresRetainedClassification(t *testing.T) {
	source := testutil.NewMemDB(t)
	if err := database.MigrateSkipping(t.Context(), source, slog.New(slog.NewTextHandler(io.Discard, nil)), "private_artifact_conversation_context"); err != nil {
		t.Fatal(err)
	}
	ws, crew := seedCovWorkspace(t, source, "old-crew-private")
	for _, query := range []string{
		`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility,title) VALUES('crew-private-chat','ws_cov_old-crew-private','a_cov_old-crew-private','u_cov_old-crew-private','private','private-draft-marker')`,
		`INSERT INTO conversation_messages(id,session_id,agent_id,role,content,author_user_id,ts) VALUES('crew-private-message','crew-private-chat','a_cov_old-crew-private','user','Create private output','u_cov_old-crew-private','2026-10-19T00:00:00Z')`,
		`INSERT INTO document_creation_consents(message_id,workspace_id,human_user_id,chat_id,submission_id,created_at) VALUES('crew-private-message','ws_cov_old-crew-private','u_cov_old-crew-private','crew-private-chat','original','2026-10-19T00:00:00Z')`,
	} {
		if _, err := source.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	versions := AppliedMigrationVersions(t.Context(), source)
	artifactEra := false
	for _, version := range versions {
		if version == 20261018074722 {
			artifactEra = true
		}
	}
	if !artifactEra {
		t.Fatal("fixture lacks actual artifact-era source migration")
	}
	dump, err := DumpCrew(t.Context(), source, crew)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"document_creations", "document_creation_consents"} {
		if _, present := dump.Tables[table]; present {
			t.Fatalf("crew dump unexpectedly carries %s", table)
		}
	}
	if len(dump.Tables["chats"]) != 1 {
		t.Fatal("crew source chat not dumped")
	}
	if _, present := dump.Tables["chats"][0][privateArtifactMarker]; present {
		t.Fatal("old source unexpectedly has classification column")
	}
	for _, kind := range []string{"artifact-era registry", "unknown registry"} {
		t.Run(kind, func(t *testing.T) {
			target := openMigratedDBCov(t)
			touched := false
			hooks := &RestoreDumpHooks{PreInsert: func(context.Context, *sql.Tx) error { touched = true; return nil }}
			if kind == "artifact-era registry" {
				hooks.legacySourceVersions = versions
			}
			_, err := RestoreDumpTxHooks(t.Context(), target, dump, hooks)
			if err == nil || !strings.Contains(err.Error(), "private artifact conversation classification") {
				t.Errorf("ambiguous crew bundle classification was silently discarded: %v", err)
			}
			if touched {
				t.Error("restore hook ran before crew privacy preflight")
			}
			var count int
			if queryErr := target.QueryRow(`SELECT count(*) FROM chats WHERE workspace_id=?`, ws).Scan(&count); queryErr != nil || count != 0 {
				t.Errorf("ambiguous crew chat landed: rows=%d err=%v", count, queryErr)
			}
		})
	}
}
