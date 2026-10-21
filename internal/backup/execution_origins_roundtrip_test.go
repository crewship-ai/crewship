package backup_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/chatentities"
	"github.com/crewship-ai/crewship/internal/runauthority"
)

func seedExecutionOriginHistory(t *testing.T, db *sql.DB) string {
	t.Helper()
	workspace := seedChatEntityHistory(t, db)
	if _, e := db.Exec(`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('origin-owner','ws_e2e_1','u_admin','OWNER')`); e != nil {
		t.Fatal(e)
	}
	receipt, e := chataudience.CaptureTrusted(t.Context(), db, "history-chat", "u_admin", nil)
	if e != nil || receipt == nil {
		t.Fatalf("origin receipt: %+v %v", receipt, e)
	}
	raw, e := json.Marshal(receipt)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`INSERT INTO execution_origins(id,workspace_id,root_run_id,chat_id,trigger_message_id,human_authority,created_at) VALUES('execution-root','ws_e2e_1','historical-root-run','history-chat','historical-trigger',?,'2026-10-13T10:00:00Z')`, string(raw)); e != nil {
		t.Fatal(e)
	}
	for _, q := range []string{
		`UPDATE missions SET chat_origin_id='execution-root' WHERE id='history-live'`,
		`INSERT INTO assignments(id,workspace_id,chat_id,assigned_by_id,assigned_to_id,task,status,chat_origin_id) VALUES('origin-assignment','ws_e2e_1','history-chat','a_alice','a_bob','Historical work','COMPLETED','execution-root')`,
		`INSERT INTO pipelines(id,workspace_id,slug,name,definition_json,definition_hash,chat_origin_id) VALUES('origin-pipeline','ws_e2e_1','origin-pipeline','Origin','{}','origin-definition','execution-root')`,
		`INSERT INTO pipeline_versions(id,pipeline_id,version,definition_json,definition_hash,author_type,author_id,chat_origin_id) VALUES('origin-version','origin-pipeline',1,'{}','origin-version-hash','system','system','execution-root')`,
		`INSERT INTO pipeline_drafts(id,workspace_id,slug,revision,document_json,updated_by,created_at,updated_at,chat_origin_id) VALUES('origin-draft','ws_e2e_1','origin-draft',1,'{}','u_admin','now','now','execution-root')`,
		`INSERT INTO mission_comments(id,mission_id,author_type,author_id,body,chat_origin_id) VALUES('origin-comment','history-live','agent','a_alice','Historical comment','execution-root')`,
		`INSERT INTO pipeline_versions(id,pipeline_id,version,definition_json,definition_hash,author_type,author_id) VALUES('legacy-origin-version','origin-pipeline',2,'{}','legacy-version-hash','system','system')`,
		`INSERT INTO mission_comments(id,mission_id,author_type,author_id,body) VALUES('legacy-origin-comment','history-live','agent','a_alice','Comment without an admission origin')`,
		`INSERT INTO pipeline_schedules(id,workspace_id,name,target_pipeline_id,cron_expr,enabled,chat_origin_id) VALUES('origin-schedule','ws_e2e_1','Historical schedule','origin-pipeline','0 8 * * *',0,'execution-root')`,
		`INSERT INTO pipeline_runs(id,workspace_id,pipeline_id,pipeline_slug,status,started_at,chat_origin_id) VALUES('origin-run','ws_e2e_1','origin-pipeline','origin-pipeline','completed','2026-10-13T10:00:00Z','execution-root')`,
		`INSERT INTO pending_runs(id,workspace_id,pipeline_id,pipeline_slug,status,fire_at,chat_origin_id) VALUES('origin-pending','ws_e2e_1','origin-pipeline','origin-pipeline','cancelled','2026-10-13T10:00:00Z','execution-root')`,
	} {
		if _, e := db.Exec(q); e != nil {
			t.Fatal(e)
		}
	}
	return workspace
}
func TestExecutionOriginsBackupNormalRestoreRetainsProvenance(t *testing.T) {
	source := openMigratedDB(t)
	workspace := seedExecutionOriginHistory(t, source)
	if _, e := runauthority.ValidateOrigin(t.Context(), source, "execution-root", "a_bob", workspace); e != nil {
		t.Fatalf("source admission invalid: %v", e)
	}
	dump, e := backup.DumpWorkspace(t.Context(), source, workspace)
	if e != nil {
		t.Fatal(e)
	}
	if len(dump.Tables["mission_comments"]) != 2 || len(dump.Tables["pipeline_versions"]) != 2 {
		t.Fatal("optional origin dropped untagged historical content")
	}
	if len(dump.Tables["execution_origins"]) != 1 {
		t.Fatal("execution provenance omitted")
	}
	target := openMigratedDB(t)
	if e = backup.RestoreDump(t.Context(), target, dump); e != nil {
		t.Fatal(e)
	}
	var authority, chat, root string
	if e = target.QueryRow(`SELECT human_authority,chat_id,root_run_id FROM execution_origins WHERE id='execution-root'`).Scan(&authority, &chat, &root); e != nil {
		t.Fatal(e)
	}
	if authority == "{}" || chat != "history-chat" || root != "historical-root-run" {
		t.Fatalf("lost origin %s %s %s", authority, chat, root)
	}
	// Chat INSERT intentionally allocates a fresh authority_generation even
	// on a normal restore. Preserve provenance without reauthorizing paid work.
	if _, e = runauthority.ValidateOrigin(t.Context(), target, "execution-root", "a_bob", workspace); !errors.Is(e, runauthority.ErrRequesterStale) {
		t.Fatalf("normal restore revived admission authority: %v", e)
	}
	for _, table := range []string{"assignments", "missions", "pipelines", "pipeline_versions", "pipeline_drafts", "mission_comments", "pipeline_schedules", "pipeline_runs", "pending_runs"} {
		var count int
		if e = target.QueryRow(`SELECT COUNT(*) FROM ` + table + ` WHERE chat_origin_id='execution-root'`).Scan(&count); e != nil || count != 1 {
			t.Fatalf("%s lost origin count=%d %v", table, count, e)
		}
	}
}
func TestExecutionOriginsBackupForkRetainsInvalidOriginRatherThanAutonomy(t *testing.T) {
	source := openMigratedDB(t)
	workspace := seedExecutionOriginHistory(t, source)
	dump, e := backup.DumpWorkspace(t.Context(), source, workspace)
	if e != nil {
		t.Fatal(e)
	}
	if e = backup.RemapIDs(t.Context(), source, dump); e != nil {
		t.Fatal(e)
	}
	dump.Tables["workspaces"][0]["slug"] = "execution-origin-fork"
	if e = backup.RestoreDump(t.Context(), source, dump); e != nil {
		t.Fatal(e)
	}
	origin, _ := dump.Tables["execution_origins"][0]["id"].(string)
	forkWorkspace, _ := dump.Tables["workspaces"][0]["id"].(string)
	var authority, originWorkspace, chatWorkspace, root string
	if e = source.QueryRow(`SELECT o.human_authority,o.workspace_id,c.workspace_id,o.root_run_id FROM execution_origins o JOIN chats c ON c.id=o.chat_id WHERE o.id=?`, origin).Scan(&authority, &originWorkspace, &chatWorkspace, &root); e != nil {
		t.Fatal(e)
	}
	if origin == "execution-root" || authority != "{}" || originWorkspace != forkWorkspace || chatWorkspace != forkWorkspace || root != "historical-root-run" {
		t.Fatalf("unsafe fork origin=%s authority=%s workspace=%s chat=%s root=%s", origin, authority, originWorkspace, chatWorkspace, root)
	}
	for _, table := range []string{"assignments", "missions", "pipelines", "pipeline_versions", "pipeline_drafts", "mission_comments", "pipeline_schedules", "pipeline_runs", "pending_runs"} {
		var count int
		if e = source.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE chat_origin_id=?`, origin).Scan(&count); e != nil || count != 1 {
			t.Fatalf("%s lost origin count=%d %v", table, count, e)
		}
	}
	// The copied deferred job must not point back to the source tenant.
	var pendingWorkspace string
	if e = source.QueryRow(`SELECT workspace_id FROM pending_runs WHERE chat_origin_id=?`, origin).Scan(&pendingWorkspace); e != nil || pendingWorkspace != forkWorkspace {
		t.Fatalf("deferred job retained source workspace: %s %v", pendingWorkspace, e)
	}
	var executor string
	if e = source.QueryRow(`SELECT id FROM agents WHERE workspace_id=? AND slug='bob'`, forkWorkspace).Scan(&executor); e != nil {
		t.Fatal(e)
	}
	if _, e = runauthority.ValidateOrigin(t.Context(), source, origin, executor, forkWorkspace); !errors.Is(e, runauthority.ErrIdentityMismatch) {
		t.Fatalf("fork revived human authority: %v", e)
	}

}

func TestExecutionOriginsForkRemapsPipelineOperationActor(t *testing.T) {
	source := openMigratedDB(t)
	workspace := seedExecutionOriginHistory(t, source)
	if _, err := source.Exec(`UPDATE pipelines SET author_crew_id='c_alpha' WHERE id='origin-pipeline'`); err != nil {
		t.Fatal(err)
	}
	tx, err := source.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	actors := []string{"pipeline:origin-pipeline:crew:c_alpha", "historical-system", "pipeline:origin-pipeline:crew:c_alpha:extra", "pipeline:unknown:crew:c_alpha"}
	for _, actor := range actors {
		op, _, err := chatentities.BeginOperation(t.Context(), tx, chatentities.OperationSpec{
			ID: "system-op-" + actor, WorkspaceID: workspace, Key: "system-key-" + actor,
			RequestHash: "request-hash", ActorType: "system", ActorID: actor,
			Provenance: "legacy", Kind: "issue", Verb: "created",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err = chatentities.CompleteOperation(t.Context(), tx, op.ID, chatentities.ResultSpec{
			EntityID: "history-live", Revision: 1, AggregateRevision: 1, Result: json.RawMessage(`{"created":true}`),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	dump, err := backup.DumpWorkspace(t.Context(), source, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err = backup.RemapIDs(t.Context(), source, dump); err != nil {
		t.Fatal(err)
	}
	dump.Tables["workspaces"][0]["slug"] = "system-operation-fork"
	if err = backup.RestoreDump(t.Context(), source, dump); err != nil {
		t.Fatal(err)
	}
	forkWorkspace := dump.Tables["workspaces"][0]["id"].(string)
	var pipeline string
	if err = source.QueryRow(`SELECT id FROM pipelines WHERE workspace_id=? AND slug='origin-pipeline'`, forkWorkspace).Scan(&pipeline); err != nil || pipeline == "origin-pipeline" {
		t.Fatalf("pipeline not forked: %s %v", pipeline, err)
	}
	var crew string
	if err = source.QueryRow(`SELECT id FROM crews WHERE workspace_id=? AND slug='alpha'`, forkWorkspace).Scan(&crew); err != nil || crew == "c_alpha" {
		t.Fatalf("crew not forked: %s %v", crew, err)
	}
	for _, actor := range actors {
		expected := actor
		if actor == actors[0] {
			expected = "pipeline:" + pipeline + ":crew:" + crew
		}
		op, err := chatentities.FindOperation(t.Context(), source, forkWorkspace, "system", expected, "system-key-"+actor)
		if err != nil || op.ActorID != expected || op.EntityID == "history-live" || op.RunID != "" {
			t.Fatalf("forked system actor %s: %+v %v", actor, op, err)
		}
		var refs int
		if err = source.QueryRow(`SELECT COUNT(*) FROM chat_entity_refs WHERE operation_id=?`, op.ID).Scan(&refs); err != nil || refs != 0 {
			t.Fatalf("autonomous operation gained chat refs: %d %v", refs, err)
		}
	}
}

func TestExecutionOriginsCustomRoutineBundleRestoresWithoutChatCategory(t *testing.T) {
	source := openMigratedDB(t)
	workspace := seedExecutionOriginHistory(t, source)
	actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
	const passphrase = "synthetic-routine-origin-backup-passphrase"
	created, e := backup.CreateBackup(t.Context(), source, backup.CreateOptions{
		Scope: backup.ScopeWorkspace, WorkspaceID: workspace, OutputDir: t.TempDir(), Actor: actor, Passphrase: passphrase,
		Categories: []string{backup.CategoryRoutines, backup.CategoryAgents},
	})
	if e != nil {
		t.Fatal(e)
	}
	target := openMigratedDB(t)
	if _, e = backup.RestoreBackup(t.Context(), target, backup.RestoreOptions{Path: created.Path, Passphrase: passphrase, Actor: actor}); e != nil {
		t.Fatal(e)
	}
	var origin, chat string
	if e = target.QueryRow(`SELECT chat_origin_id FROM pipelines WHERE id='origin-pipeline'`).Scan(&origin); e != nil || origin == "" {
		t.Fatalf("routine lost origin: %s %v", origin, e)
	}
	if e = target.QueryRow(`SELECT chat_id FROM execution_origins WHERE id=?`, origin).Scan(&chat); e != nil || chat != "history-chat" {
		t.Fatalf("origin missing: %s %v", chat, e)
	}
	var chats int
	if e = target.QueryRow(`SELECT COUNT(*) FROM chats WHERE id='history-chat'`).Scan(&chats); e != nil || chats != 0 {
		t.Fatalf("excluded private chat restored: %d %v", chats, e)
	}
	// Missing source chat revokes the imported origin. Keep the origin present
	// so a later dispatch cannot classify the routine as autonomous instead.
	if _, e = runauthority.ValidateOrigin(t.Context(), target, origin, "a_bob", workspace); !errors.Is(e, runauthority.ErrRequesterStale) {
		t.Fatalf("missing chat did not fence execution: %v", e)
	}
}

func TestExecutionOriginsCustomChatBundleRetainsAdmissionFence(t *testing.T) {
	source := openMigratedDB(t)
	workspace := seedExecutionOriginHistory(t, source)
	actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
	const passphrase = "synthetic-chat-origin-backup-passphrase"
	created, e := backup.CreateBackup(t.Context(), source, backup.CreateOptions{
		Scope: backup.ScopeWorkspace, WorkspaceID: workspace, OutputDir: t.TempDir(), Actor: actor, Passphrase: passphrase,
		Categories: []string{backup.CategoryChats, backup.CategoryAgents},
	})
	if e != nil {
		t.Fatal(e)
	}
	target := openMigratedDB(t)
	if _, e = backup.RestoreBackup(t.Context(), target, backup.RestoreOptions{Path: created.Path, Passphrase: passphrase, Actor: actor}); e != nil {
		t.Fatal(e)
	}
	var authority, chat string
	if e = target.QueryRow(`SELECT human_authority,chat_id FROM execution_origins WHERE id='execution-root'`).Scan(&authority, &chat); e != nil || authority == "{}" || chat != "history-chat" {
		t.Fatalf("chat bundle lost admission fence %s %s %v", authority, chat, e)
	}
	var count int
	if e = target.QueryRow(`SELECT COUNT(*) FROM chats WHERE id='history-chat'`).Scan(&count); e != nil || count != 1 {
		t.Fatalf("chat missing %d %v", count, e)
	}
	if e = target.QueryRow(`SELECT COUNT(*) FROM pipelines WHERE id='origin-pipeline'`).Scan(&count); e != nil || count != 0 {
		t.Fatalf("excluded routine restored %d %v", count, e)
	}
	if _, e = runauthority.ValidateOrigin(t.Context(), target, "execution-root", "a_bob", workspace); !errors.Is(e, runauthority.ErrRequesterStale) {
		t.Fatalf("custom chat restore revived admission: %v", e)
	}

}

func TestExecutionOriginsRestoreRejectsAuthoritySchemaDowngrade(t *testing.T) {
	for _, tc := range []struct {
		name   string
		origin any
		deny   bool
	}{{"bound origin", "execution-root", true}, {"legacy null", nil, false}, {"legacy absent", nil, false}, {"legacy empty", "", false}} {
		t.Run(tc.name, func(t *testing.T) {
			target, e := sql.Open("sqlite", filepath.Join(t.TempDir(), "older.db"))
			if e != nil {
				t.Fatal(e)
			}
			defer target.Close()
			if _, e = target.Exec(`CREATE TABLE workspaces(id TEXT PRIMARY KEY);CREATE TABLE pipelines(id TEXT PRIMARY KEY,workspace_id TEXT);`); e != nil {
				t.Fatal(e)
			}
			dump := &backup.DBDump{WorkspaceID: "old-target", Tables: map[string][]map[string]any{
				"workspaces": {{"id": "old-target"}}, "pipelines": {{"id": "pipeline", "workspace_id": "old-target", "chat_origin_id": tc.origin}},
			}}
			if tc.name == "legacy absent" {
				delete(dump.Tables["pipelines"][0], "chat_origin_id")
			}
			e = backup.RestoreDump(t.Context(), target, dump)
			if tc.deny {
				if e == nil || !strings.Contains(e.Error(), "unsupported authority schema") {
					t.Fatalf("authority downgraded: %v", e)
				}
				var count int
				if e = target.QueryRow(`SELECT COUNT(*) FROM workspaces`).Scan(&count); e != nil || count != 0 {
					t.Fatalf("partial restore: %d %v", count, e)
				}
			} else if e != nil {
				t.Fatalf("legacy restore rejected: %v", e)
			}
		})
	}
}
