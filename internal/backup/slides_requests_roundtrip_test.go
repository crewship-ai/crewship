package backup_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/slides"
)

// Storage/backup fixture: runtime authorization and dispatcher are qualified
// separately through the actual API and sidecar, not by this trusted callback.
func seedBackupSelected(t *testing.T, db *sql.DB, ws, page, owner, agent, chat string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES(?,?,?,?,?,'{}')`, page, ws, page, page, owner); err != nil {
		t.Fatal(err)
	}
	deck := slides.Deck{SchemaVersion: 1, DeckID: page, Width: 960, Height: 540, Theme: slides.Theme{Background: "#ffffff", Color: "#000000", FontFamily: "Arial", FontSize: 24}, SlideOrder: []string{"slide_one"}, Slides: []slides.Slide{{ID: "slide_one", Elements: []slides.Element{{ID: "selected_text", Type: "text", X: 0, Y: 0, Width: 500, Height: 100, Text: &slides.Text{Value: "Český text\n🐎", FontFamily: "Arial", FontSize: 24, Color: "#000000", Align: "left"}}}}}}
	key := slides.Key{WorkspaceID: ws, PageID: page}
	store := slides.New(db, slides.Options{})
	auth := func(context.Context, *sql.Tx) (string, error) { return "user:" + owner, nil }
	if _, err := store.Initialize(t.Context(), key, slides.InitializeRequest{PageID: page, SchemaVersion: 1, MutationID: "backup_initialize", Deck: deck}, auth); err != nil {
		t.Fatal(err)
	}
	hash, _ := slides.ElementHash(deck.Slides[0].Elements[0])
	input := slides.SelectedRequestInput{PageID: page, TypeID: "slides", SchemaVersion: 1, BaseRev: 1, SubmissionID: page + "-submission", ChatID: chat, Instruction: "Improve selected wording", Selection: slides.TextSelection{SelectionSchemaVersion: 1, Kind: "text_range", SlideID: "slide_one", ElementID: "selected_text", ElementHash: hash, From: 0, To: 5, Quote: "Český"}}
	admission := slides.SelectedAdmission{HumanUserID: owner, AgentID: agent, ChatID: chat, AudienceJSON: `{"trusted":"backup fixture"}`, CredentialKind: "session", CredentialID: "backup_session"}
	for _, suffix := range []string{"result", "pending", "cancelled"} {
		input.SubmissionID = page + "-" + suffix
		if _, err := store.AdmitSelectedRequest(t.Context(), key, page+"-"+suffix, input, admission, auth); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.CancelSelectedRequest(t.Context(), key, page+"-cancelled", slides.CancelSelectedRequestInput{PageID: page, SchemaVersion: 1, MutationID: "backup_cancel", IfVersion: 1}, auth); err != nil {
		t.Fatal(err)
	}
	id, run := page+"-result", page+"-run"
	if _, err := store.ClaimSelectedRequest(t.Context(), key, id, auth); err != nil {
		t.Fatal(err)
	}
	if err := store.BindSelectedRun(t.Context(), key, id, run, auth, func(context.Context, *sql.Tx, slides.SelectedAuthorityState) error { return nil }); err != nil {
		t.Fatal(err)
	}
	agentAuth := func(context.Context, *sql.Tx) (string, error) { return "agent:" + agent + ":" + run, nil }
	if _, err := store.ReplaceSelectedText(t.Context(), key, id, run, "Upravený", agentAuth); err != nil {
		t.Fatal(err)
	}
}
func assertBackupSelectedInert(t *testing.T, db *sql.DB, key slides.Key) {
	t.Helper()
	store := slides.New(db, slides.Options{})
	rows, err := db.Query(`SELECT id FROM slides_requests WHERE workspace_id=? AND page_id=? ORDER BY request_seq`, key.WorkspaceID, key.PageID)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if len(ids) != 3 {
		t.Fatal("lost requests", ids)
	}
	for i, id := range ids {
		detail, err := store.ReadSelectedRequest(t.Context(), key, id, backupSlideAuthority)
		if err != nil {
			t.Fatal(err)
		}
		r := detail.Request
		if !r.Restored || r.Selection.ElementID != "selected_text" || r.BaseRevision != 1 || r.PageID != key.PageID {
			t.Fatal("lost immutable provenance", r)
		}
		base, err := store.Read(t.Context(), key, 1, backupSlideAuthority)
		if err != nil || base.ContentHash != r.BaseContentHash {
			t.Fatal("base hash mismatch", base, err)
		}
		expectedStatus, expectedVersion := "interrupted", int64(2)
		if i == 0 {
			expectedVersion = 5
		} else if i == 2 {
			expectedStatus = "cancelled"
		}
		if r.Status != expectedStatus || r.Version != expectedVersion || int64(len(detail.History)) != expectedVersion {
			t.Fatal("history not preserved/fenced", detail)
		}
		if i == 0 {
			result, err := store.Read(t.Context(), key, *r.ResultRevision, backupSlideAuthority)
			if err != nil || result.ContentHash != r.ResultHash || result.Revision != 2 {
				t.Fatal("owned result lost", result, err)
			}
		}
		var live int
		if err = db.QueryRow(`SELECT COUNT(*) FROM slides_requests WHERE id=? AND (run_id IS NOT NULL OR human_user_id IS NOT NULL OR agent_id IS NOT NULL OR chat_id IS NOT NULL OR audience_json!='{}' OR credential_id!='' OR credential_hash!='')`, id).Scan(&live); err != nil || live != 0 {
			t.Fatal("restored authority live", live, err)
		}
		if _, err = store.ClaimSelectedRequest(t.Context(), key, id, backupSlideAuthority); err == nil {
			t.Fatal("restored request restarted")
		}
	}
	var receipts int
	if err = db.QueryRow(`SELECT COUNT(*) FROM slides_request_receipts WHERE workspace_id=? AND page_id=?`, key.WorkspaceID, key.PageID).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatal("restored receipts retained", receipts, err)
	}
}
func TestSlidesSelectedEncryptedBackupForkReplace(t *testing.T) {
	source := openMigratedDB(t)
	ws := seedWorkspace(t, source)
	seedBackupSelected(t, source, ws, "selected-page", "u_admin", "a_alice", "ch_alice_1")
	actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
	bundle, err := backup.CreateBackup(t.Context(), source, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: ws, OutputDir: t.TempDir(), Passphrase: "selected-backup-passphrase", Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"same", "fork", "replace"} {
		t.Run(mode, func(t *testing.T) {
			target := openMigratedDB(t)
			opts := backup.RestoreOptions{Path: bundle.Path, Passphrase: "selected-backup-passphrase", Actor: actor}
			if mode == "fork" {
				opts.AsWorkspace = "selected-fork"
			}
			if mode == "replace" {
				seedWorkspace(t, target)
				seedBackupSelected(t, target, ws, "selected-page", "u_admin", "a_alice", "ch_alice_1")
				opts.Replace = true
			}
			if _, err := backup.RestoreBackup(t.Context(), target, opts); err != nil {
				t.Fatal(err)
			}
			var key slides.Key
			if err := target.QueryRow(`SELECT workspace_id,page_id FROM slides_heads`).Scan(&key.WorkspaceID, &key.PageID); err != nil {
				t.Fatal(err)
			}
			if mode == "fork" && key.PageID == "selected-page" {
				t.Fatal("fork Page identity unchanged")
			}
			assertBackupSelectedInert(t, target, key)
		})
	}
}
func TestSlidesSelectedInstanceEncryptedRecovery(t *testing.T) {
	f := newInstanceFixture(t)
	if _, err := f.db.Exec(`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('ch_alice_1','ws_e2e_1','a_alice','u_admin','private')`); err != nil {
		t.Fatal(err)
	}
	seedBackupSelected(t, f.db, "ws_e2e_1", "instance-selected", "u_admin", "a_alice", "ch_alice_1")
	if _, err := f.db.Exec(`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('selected_lab_chat','ws_lab','a_lab','u_lab','private')`); err != nil {
		t.Fatal(err)
	}
	seedBackupSelected(t, f.db, "ws_lab", "lab-selected", "u_lab", "a_lab", "selected_lab_chat")
	bundle := f.create(t, true)
	dir := filepath.Join(t.TempDir(), "recovered")
	if _, err := backup.RecoverInstance(t.Context(), backup.RecoverOptions{BundlePath: bundle.Path, Identities: []age.Identity{f.identity}, DataDir: dir, Actor: "u_admin"}); err != nil {
		t.Fatal(err)
	}
	target, err := database.Open("file:" + filepath.Join(dir, "crewship.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	for _, key := range []slides.Key{{WorkspaceID: "ws_e2e_1", PageID: "instance-selected"}, {WorkspaceID: "ws_lab", PageID: "lab-selected"}} {
		assertBackupSelectedInert(t, target.DB, key)
	}
}
func TestSlidesSelectedMalformedRestoreRollsBack(t *testing.T) {
	source := openMigratedDB(t)
	ws := seedWorkspace(t, source)
	seedBackupSelected(t, source, ws, "malformed-selected", "u_admin", "a_alice", "ch_alice_1")
	for _, column := range []string{"base_content_hash", "result_hash", "protocol_version", "restored"} {
		t.Run(column, func(t *testing.T) {
			dump, err := backup.DumpWorkspace(t.Context(), source, ws)
			if err != nil {
				t.Fatal(err)
			}
			row := dump.Tables["slides_requests"][0]
			if column == "protocol_version" {
				row[column] = int64(2)
			} else if column == "restored" {
				row[column] = int64(1)
			} else {
				row[column] = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
			}
			target := openMigratedDB(t)
			if err = backup.RestoreDump(t.Context(), target, dump); err == nil {
				t.Fatal("malformed restore accepted", column)
			}
			var n int
			if err = target.QueryRow(`SELECT COUNT(*) FROM slides_heads`).Scan(&n); err != nil || n != 0 {
				t.Fatal("failed restore left content", n, err)
			}
		})
	}
}

func TestSlidesSelectedRestorePreservesForeignDelegationAndGlobalIdentity(t *testing.T) {
	source := openMigratedDB(t)
	ws := seedWorkspace(t, source)
	seedBackupSelected(t, source, ws, "source-selected", "u_admin", "a_alice", "ch_alice_1")
	dump, err := backup.DumpWorkspace(t.Context(), source, ws)
	if err != nil {
		t.Fatal(err)
	}
	target := openMigratedDB(t)
	seedWorkspace(t, target)
	for _, query := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('foreign-selected-ws','Foreign','foreign-selected')`,
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES('foreign-selected-crew','foreign-selected-ws','Foreign','foreign')`,
		`INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('foreign-selected-agent','foreign-selected-ws','foreign-selected-crew','Foreign','foreign')`,
		`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('foreign-selected-chat','foreign-selected-ws','foreign-selected-agent','u_admin','private')`,
	} {
		if _, err = target.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	seedBackupSelected(t, target, "foreign-selected-ws", "foreign-selected-page", "u_admin", "foreign-selected-agent", "foreign-selected-chat")
	if _, err = target.Exec(`INSERT INTO artifact_run_principals(run_id,workspace_id,agent_id,crew_id,human_user_id,chat_id,message_id,member_id,member_revision,chat_generation,chat_revision,expires_at,slides_request_origin) VALUES('foreign-selected-page-run','foreign-selected-ws','foreign-selected-agent','foreign-selected-crew','u_admin','foreign-selected-chat','fixture-message','fixture-member',1,'fixture-generation',1,'2099-01-01T00:00:00Z','foreign-selected-page-result')`); err != nil {
		t.Fatal(err)
	}
	if _, err = target.Exec(`INSERT INTO page_artifact_run_delegations(page_id,run_id,granted_by_user_id,expires_at,granted_at,slides_request_id) VALUES('foreign-selected-page','foreign-selected-page-run','u_admin','2099-01-01T00:00:00Z','2026-10-16T00:00:00Z','foreign-selected-page-result')`); err != nil {
		t.Fatal(err)
	}
	if err = backup.RestoreDump(t.Context(), target, dump); err != nil {
		t.Fatal(err)
	}
	assertBackupSelectedInert(t, target, slides.Key{WorkspaceID: ws, PageID: "source-selected"})
	var grants int
	if err = target.QueryRow(`SELECT COUNT(*) FROM page_artifact_run_delegations WHERE page_id='foreign-selected-page' AND slides_request_id='foreign-selected-page-result'`).Scan(&grants); err != nil || grants != 1 {
		t.Fatal("foreign delegation stripped", grants, err)
	}
	foreign, err := slides.New(target, slides.Options{}).ReadSelectedRequest(t.Context(), slides.Key{WorkspaceID: "foreign-selected-ws", PageID: "foreign-selected-page"}, "foreign-selected-page-result", backupSlideAuthority)
	if err != nil || foreign.Request.Restored || foreign.Request.Status != "running" {
		t.Fatal("foreign history fenced", foreign, err)
	}
	// IDs are globally unique even across workspace/Page namespaces. Demonstrate
	// the exact same ID insert is refused instead of relying on that assumption.
	rows, err := target.Query(`PRAGMA table_info(slides_requests)`)
	if err != nil {
		t.Fatal(err)
	}
	var columns []string
	for rows.Next() {
		var cid, notnull, pk int
		var name, kind string
		var def any
		if err = rows.Scan(&cid, &name, &kind, &notnull, &def, &pk); err != nil {
			t.Fatal(err)
		}
		columns = append(columns, name)
	}
	rows.Close()
	var expressions []string
	for _, name := range columns {
		switch name {
		case "id":
			expressions = append(expressions, "'source-selected-result'")
		case "request_seq":
			expressions = append(expressions, "999")
		case "submission_id":
			expressions = append(expressions, "'duplicate-cross-workspace'")
		case "run_id":
			expressions = append(expressions, "NULL")
		default:
			expressions = append(expressions, name)
		}
	}
	_, err = target.Exec(`INSERT INTO slides_requests(` + strings.Join(columns, ",") + `) SELECT ` + strings.Join(expressions, ",") + ` FROM slides_requests WHERE id='foreign-selected-page-result'`)
	if err == nil || !strings.Contains(err.Error(), "slides_requests.id") {
		t.Fatal("global request identity not enforced", err)
	}
}
