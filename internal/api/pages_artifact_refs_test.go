package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

// artifactRefsFixture is the original-run fixture (a MEMBER in their own
// private chat with an agent, an OWNER admin beside them) plus pages to
// reference and, unless off, the workspace flag turned on.
func artifactRefsFixture(t *testing.T, flagOn bool) *artifactPrincipalFixture {
	t.Helper()
	f := artifactProjectOriginalRunFixture(t)
	db, ws := f.rig.db, f.a.WorkspaceID
	mustExec(t, db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('ref-own',?,'ref-own','Own',?,'{}')`, ws, f.a.HumanUserID)
	mustExec(t, db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,private_audience) VALUES('ref-own-private',?,'ref-own-private','Own private',?,'{}',1)`, ws, f.a.HumanUserID)
	mustExec(t, db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('ref-admins',?,'ref-admins','Admin only',?,'{}')`, ws, f.rig.adminID)
	mustExec(t, db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,private_audience) VALUES('ref-admins-private',?,'ref-admins-private','Admin private',?,'{}',1)`, ws, f.rig.adminID)
	mustExec(t, db, `INSERT INTO workspaces(id,name,slug) VALUES('ref-elsewhere','Elsewhere','ref-elsewhere')`)
	mustExec(t, db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('ref-foreign','ref-elsewhere','ref-foreign','Foreign',?,'{}')`, f.a.HumanUserID)
	if flagOn {
		mustExec(t, db, `INSERT INTO feature_flag_overrides(id,flag_id,workspace_id,enabled) VALUES('ref-flag-on','ff_artifact_agent_access_modes',?,1)`, ws)
	}
	return f
}

func artifactRefRows(t *testing.T, f *artifactPrincipalFixture, query string, args ...any) []string {
	t.Helper()
	rows, err := f.rig.db.Query(query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

// With the flag off the bridge ignores the key (TestArtifactRefsSendPath);
// the server, asked directly, still records nothing.
func TestArtifactRefs_FlagOffRecordsNothing(t *testing.T) {
	f := artifactRefsFixture(t, false)
	if on, err := f.pages.ArtifactRefsEnabled(t.Context(), f.a.WorkspaceID); err != nil || on {
		t.Fatalf("flag reported on: %v %v", on, err)
	}
	if err := f.pages.CheckArtifactRefs(t.Context(), f.receipt, []string{"ref-own"}); !errors.Is(err, errArtifactRefUnavailable) {
		t.Fatalf("flag off must refuse references: %v", err)
	}
	if err := f.pages.RecordArtifactRefs(t.Context(), f.receipt, f.a.MessageID, []string{"ref-own"}); !errors.Is(err, errArtifactRefUnavailable) {
		t.Fatalf("flag off must record nothing: %v", err)
	}
	if got := artifactRefRows(t, f, `SELECT page_id FROM artifact_message_refs`); len(got) != 0 {
		t.Fatalf("recorded with the flag off: %v", got)
	}
}

func TestArtifactRefs_RecordsWhatTheHumanReadsAsAMember(t *testing.T) {
	f := artifactRefsFixture(t, true)
	refs := []string{"ref-own", "ref-own-private"}
	if err := f.pages.CheckArtifactRefs(t.Context(), f.receipt, refs); err != nil {
		t.Fatal("pre-check", err)
	}
	for i := 0; i < 2; i++ { // an exact retry is the same set
		if err := f.pages.RecordArtifactRefs(t.Context(), f.receipt, f.a.MessageID, refs); err != nil {
			t.Fatal("record", i, err)
		}
	}
	got := artifactRefRows(t, f, `SELECT page_id FROM artifact_message_refs WHERE message_id=? ORDER BY page_id`, f.a.MessageID)
	if len(got) != 2 || got[0] != "ref-own" || got[1] != "ref-own-private" {
		t.Fatalf("recorded %v", got)
	}
	if err := f.pages.RecordArtifactRefs(t.Context(), f.receipt, f.a.MessageID, []string{"ref-own"}); err == nil {
		t.Fatal("a different set for the same message was accepted")
	}
}

// Test 15 (#5317): a refusal never tells the sender that something exists.
func TestArtifactRefs_OneRefusalForMissingUnreadableForeignAndMalformed(t *testing.T) {
	f := artifactRefsFixture(t, true)
	for name, refs := range map[string][]string{
		"missing":              {"ref-nowhere"},
		"unreadable":           {"ref-admins"},
		"someone else private": {"ref-admins-private"},
		"other workspace":      {"ref-foreign"},
		"malformed":            {"ref own"},
		"duplicate":            {"ref-own", "ref-own"},
		"one bad among good":   {"ref-own", "ref-nowhere"},
		"over the bound":       {"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"},
		"nothing":              {},
	} {
		t.Run(name, func(t *testing.T) {
			if err := f.pages.CheckArtifactRefs(t.Context(), f.receipt, refs); err != errArtifactRefUnavailable {
				t.Fatalf("check: %v", err)
			}
			if err := f.pages.RecordArtifactRefs(t.Context(), f.receipt, f.a.MessageID, refs); err != errArtifactRefUnavailable {
				t.Fatalf("record: %v", err)
			}
		})
	}
	if got := artifactRefRows(t, f, `SELECT page_id FROM artifact_message_refs`); len(got) != 0 {
		t.Fatalf("a refused message left references: %v", got)
	}
}

// Test 14 (#5317), read side: what the human reaches only through the ADMIN
// or OWNER role is not theirs to hand to an agent.
func TestArtifactRefs_RoleOnlyReachIsNotDelegable(t *testing.T) {
	f := artifactRefsFixture(t, true)
	ctx := context.WithValue(t.Context(), ctxWorkspaceID, f.a.WorkspaceID)
	ownerAsOwner := &pageViewer{UserID: f.rig.adminID, Role: "OWNER", Crews: map[string]bool{}}
	rec, err := f.pages.loadPage(withDelegationUser(ctx, f.rig.adminID), f.a.WorkspaceID, "@ref-own")
	if err != nil {
		t.Fatal(err)
	}
	if seen, err := f.pages.canSeePage(ctx, f.a.WorkspaceID, rec, nil, ownerAsOwner); err != nil || !seen {
		t.Fatalf("baseline: the OWNER reads another member's page by role: %v %v", seen, err)
	}
	delegable, err := f.pages.delegableViewer(ctx, f.a.WorkspaceID, f.rig.adminID)
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range []string{"ref-own", "ref-own-private"} {
		if ok, err := f.pages.delegableRead(ctx, f.a.WorkspaceID, page, delegable); err != nil || ok {
			t.Fatalf("%s: role-only reach became delegable: %v %v", page, ok, err)
		}
	}
	if ok, err := f.pages.delegableRead(ctx, f.a.WorkspaceID, "ref-admins", delegable); err != nil || !ok {
		t.Fatalf("the OWNER's own page must stay delegable: %v %v", ok, err)
	}
}

func TestArtifactRefs_OnlyTheOwnPrivateChat(t *testing.T) {
	f := artifactRefsFixture(t, true)
	mustExec(t, f.rig.db, `UPDATE chats SET visibility='group' WHERE id=?`, f.a.ChatID)
	if err := f.pages.CheckArtifactRefs(t.Context(), f.receipt, []string{"ref-own"}); err == nil {
		t.Fatal("a group chat admitted a reference")
	}
}

// Test 12 (#5317): neither an internal token nor a direct write widens a run.
func TestArtifactRefs_NoWideningOutsideHumanAdmission(t *testing.T) {
	f := artifactRefsFixture(t, true)
	crew := context.WithValue(t.Context(), ctxInternalTokenCrew, f.a.CrewID)
	if err := f.pages.RecordArtifactRefs(crew, f.receipt, f.a.MessageID, []string{"ref-own"}); err != errArtifactRefUnavailable {
		t.Fatalf("crew IPC recorded a reference: %v", err)
	}
	if err := f.pages.RecordArtifactRefs(t.Context(), f.receipt, f.a.MessageID, []string{"ref-own"}); err != nil {
		t.Fatal(err)
	}
	f.admit(t)
	if _, err := f.rig.db.Exec(`INSERT INTO artifact_run_scope(run_id,workspace_id,message_id,page_id) VALUES(?,?,?,?)`, f.a.RunID, f.a.WorkspaceID, f.a.MessageID, "ref-admins"); err == nil {
		t.Fatal("the run scope took a page its message never referenced")
	}
	if _, err := f.rig.db.Exec(`INSERT INTO artifact_message_refs(message_id,workspace_id,human_user_id,chat_id,page_id,created_at) VALUES(?,?,?,?,?,'x')`, "principal-next-message", f.a.WorkspaceID, f.a.HumanUserID, f.a.ChatID, "ref-admins"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.rig.db.Exec(`INSERT INTO artifact_run_scope(run_id,workspace_id,message_id,page_id) VALUES(?,?,?,?)`, f.a.RunID, f.a.WorkspaceID, "principal-next-message", "ref-admins"); err == nil {
		t.Fatal("the run scope took another message's reference")
	}
	if _, err := f.rig.db.Exec(`UPDATE artifact_message_refs SET page_id='ref-admins' WHERE message_id=?`, f.a.MessageID); err == nil {
		t.Fatal("a recorded reference was rewritten")
	}
}

// Test 13 (#5317): a run reaches its own message's references; the next
// message without the chip reaches none of them.
func TestArtifactRefs_RunScopeIsItsOwnMessage(t *testing.T) {
	f := artifactRefsFixture(t, true)
	if err := f.pages.RecordArtifactRefs(t.Context(), f.receipt, f.a.MessageID, []string{"ref-own", "ref-own-private"}); err != nil {
		t.Fatal(err)
	}
	writer := journal.NewWriter(f.rig.db, newTestLogger(), journal.WriterOptions{ChainKey: []byte("synthetic-artifact-refs-chain"), FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	h := NewInternalHandler(f.rig.db, "synthetic-refs-master", newTestLogger())
	h.SetJournal(writer)
	start := func(ctx context.Context, run, message string) int {
		raw, _ := json.Marshal(map[string]any{"id": run, "workspace_id": f.a.WorkspaceID, "agent_id": f.a.AgentID, "chat_id": f.a.ChatID, "trigger_type": "USER", "human_authority": f.receipt, "trigger_message_id": message})
		w := httptest.NewRecorder()
		h.CreateRun(w, httptest.NewRequest("POST", "/api/v1/internal/runs", bytes.NewReader(raw)).WithContext(ctx))
		return w.Code
	}
	if code := start(context.WithValue(t.Context(), ctxInternalTokenCrew, f.a.CrewID), "crew-run", f.a.MessageID); code == 201 {
		t.Fatal("crew IPC admitted a referenced run")
	}
	for i := 0; i < 2; i++ {
		if code := start(t.Context(), f.a.RunID, f.a.MessageID); code != 201 {
			t.Fatalf("referenced run admission %d: %d", i, code)
		}
	}
	got := artifactRefRows(t, f, `SELECT page_id FROM artifact_run_scope WHERE run_id=? ORDER BY page_id`, f.a.RunID)
	if len(got) != 2 || got[0] != "ref-own" || got[1] != "ref-own-private" {
		t.Fatalf("scope %v", got)
	}
	if code := start(t.Context(), "next-run", "principal-next-message"); code != 201 {
		t.Fatalf("plain follow-up run: %d", code)
	}
	if got := artifactRefRows(t, f, `SELECT page_id FROM artifact_run_scope WHERE run_id='next-run'`); len(got) != 0 {
		t.Fatalf("a message without chips reached earlier references: %v", got)
	}
}
