package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

func artifactRefsStartRun(t *testing.T, f *artifactPrincipalFixture, run, message string) int {
	t.Helper()
	writer := journal.NewWriter(f.rig.db, newTestLogger(), journal.WriterOptions{ChainKey: []byte("synthetic-artifact-refs-chain"), FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	h := NewInternalHandler(f.rig.db, "synthetic-refs-master", newTestLogger())
	h.SetJournal(writer)
	raw, _ := json.Marshal(map[string]any{"id": run, "workspace_id": f.a.WorkspaceID, "agent_id": f.a.AgentID, "chat_id": f.a.ChatID, "trigger_type": "USER", "human_authority": f.receipt, "trigger_message_id": message})
	w := httptest.NewRecorder()
	h.CreateRun(w, httptest.NewRequest("POST", "/api/v1/internal/runs", bytes.NewReader(raw)))
	return w.Code
}

// Who may delegate: an ordinary member's reach, never above the sender's own
// role, and nothing from a suspended or restricted membership.
func TestArtifactRefs_DelegableViewerByStanding(t *testing.T) {
	cases := []struct {
		name, setup, wantRole string
	}{
		{name: "member", wantRole: "MEMBER"},
		{name: "admin reads as member", setup: `UPDATE workspace_members SET role='ADMIN' WHERE user_id=? AND workspace_id=?`, wantRole: "MEMBER"},
		{name: "owner reads as member", setup: `UPDATE workspace_members SET role='OWNER' WHERE user_id=? AND workspace_id=?`, wantRole: "MEMBER"},
		{name: "viewer stays viewer", setup: `UPDATE workspace_members SET role='VIEWER' WHERE user_id=? AND workspace_id=?`, wantRole: "VIEWER"},
		{name: "suspended delegates nothing", setup: `UPDATE users SET suspended_at='2026-11-13T10:00:00Z' WHERE id=? AND ?<>''`},
		{name: "restricted delegates nothing", setup: `UPDATE workspace_members SET access_mode='restricted' WHERE user_id=? AND workspace_id=?`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := artifactRefsFixture(t, true)
			if tc.setup != "" {
				mustExec(t, f.rig.db, tc.setup, f.a.HumanUserID, f.a.WorkspaceID)
			}
			ctx := context.WithValue(t.Context(), ctxWorkspaceID, f.a.WorkspaceID)
			v, err := f.pages.delegableViewer(ctx, f.a.WorkspaceID, f.a.HumanUserID)
			if err != nil {
				t.Fatal(err)
			}
			if v.Role != tc.wantRole {
				t.Fatalf("role %q, want %q", v.Role, tc.wantRole)
			}
			ok, err := f.pages.delegableRead(ctx, f.a.WorkspaceID, "ref-own", v)
			if err != nil || ok != (tc.wantRole != "") {
				t.Fatalf("own page delegable=%v err=%v", ok, err)
			}
		})
	}
}

// A restricted membership in another workspace does not block delegation here.
func TestArtifactRefs_RestrictionIsPerMembership(t *testing.T) {
	f := artifactRefsFixture(t, true)
	mustExec(t, f.rig.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('wm-elsewhere','ref-elsewhere',?,'MEMBER','restricted')`, f.a.HumanUserID)
	ctx := context.WithValue(t.Context(), ctxWorkspaceID, f.a.WorkspaceID)
	v, err := f.pages.delegableViewer(ctx, f.a.WorkspaceID, f.a.HumanUserID)
	if err != nil || v.Role != "MEMBER" {
		t.Fatalf("restricted elsewhere blocked delegation here: %+v %v", v, err)
	}
}

func TestArtifactRefs_RecordRefusals(t *testing.T) {
	cases := map[string]func(*testing.T, *artifactPrincipalFixture){
		"archived page": func(t *testing.T, f *artifactPrincipalFixture) {
			mustExec(t, f.rig.db, `UPDATE pages SET archived_at='2026-11-13T10:00:00Z' WHERE id='ref-own'`)
		},
		"someone else's chat": func(t *testing.T, f *artifactPrincipalFixture) {
			mustExec(t, f.rig.db, `UPDATE chats SET created_by=? WHERE id=?`, f.rig.adminID, f.a.ChatID)
		},
		"another human's message": func(t *testing.T, f *artifactPrincipalFixture) {
			mustExec(t, f.rig.db, `UPDATE conversation_messages SET author_user_id=? WHERE id=?`, f.rig.adminID, f.a.MessageID)
		},
		"an agent message": func(t *testing.T, f *artifactPrincipalFixture) {
			mustExec(t, f.rig.db, `UPDATE conversation_messages SET role='assistant' WHERE id=?`, f.a.MessageID)
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			f := artifactRefsFixture(t, true)
			setup(t, f)
			if err := f.pages.RecordArtifactRefs(t.Context(), f.receipt, f.a.MessageID, []string{"ref-own"}); err == nil {
				t.Fatal("recorded")
			}
			if got := artifactRefRows(t, f, `SELECT page_id FROM artifact_message_refs`); len(got) != 0 {
				t.Fatalf("rows %v", got)
			}
		})
	}
}

// The schema holds the recorder's shape on its own.
func TestArtifactRefs_TableRefusesAForeignShape(t *testing.T) {
	f := artifactRefsFixture(t, true)
	insert := func(human, page string) error {
		_, err := f.rig.db.Exec(`INSERT INTO artifact_message_refs(message_id,workspace_id,human_user_id,chat_id,page_id,created_at) VALUES(?,?,?,?,?,'x')`, f.a.MessageID, f.a.WorkspaceID, human, f.a.ChatID, page)
		return err
	}
	if insert(f.rig.adminID, "ref-own") == nil {
		t.Fatal("a reference named a human who did not write the message")
	}
	if insert(f.a.HumanUserID, "ref-foreign") == nil {
		t.Fatal("a reference named another workspace's page")
	}
	if err := insert(f.a.HumanUserID, "ref-own"); err != nil {
		t.Fatal("the recorder's own shape:", err)
	}
}

// Turning the flag off after references were recorded starts the run the
// ordinary way, binding nothing, instead of failing it.
func TestArtifactRefs_KillSwitchFallsBackToAPlainRun(t *testing.T) {
	f := artifactRefsFixture(t, true)
	if err := f.pages.RecordArtifactRefs(t.Context(), f.receipt, f.a.MessageID, []string{"ref-own"}); err != nil {
		t.Fatal(err)
	}
	mustExec(t, f.rig.db, `DELETE FROM feature_flag_overrides WHERE id='ref-flag-on'`)
	if code := artifactRefsStartRun(t, f, f.a.RunID, f.a.MessageID); code != 201 {
		t.Fatalf("flag off after recording: %d", code)
	}
	if got := artifactRefRows(t, f, `SELECT page_id FROM artifact_run_scope`); len(got) != 0 {
		t.Fatalf("bound with the flag off: %v", got)
	}
}

// Access lost between sending and run admission is not handed to the run.
func TestArtifactRefs_AdmissionChecksAccessAgain(t *testing.T) {
	f := artifactRefsFixture(t, true)
	if err := f.pages.RecordArtifactRefs(t.Context(), f.receipt, f.a.MessageID, []string{"ref-own"}); err != nil {
		t.Fatal(err)
	}
	mustExec(t, f.rig.db, `UPDATE pages SET archived_at='2026-11-13T10:00:00Z' WHERE id='ref-own'`)
	if code := artifactRefsStartRun(t, f, f.a.RunID, f.a.MessageID); code == 201 {
		t.Fatal("admitted a run over a reference its human can no longer read")
	}
	if got := artifactRefRows(t, f, `SELECT page_id FROM artifact_run_scope`); len(got) != 0 {
		t.Fatalf("bound %v", got)
	}
}

// An exact retry of an admitted start stays idempotent even if access changed
// after admission: the run already started, and failing the retry would mark
// it failed.
func TestArtifactRefs_AdmittedRetryIsIdempotent(t *testing.T) {
	f := artifactRefsFixture(t, true)
	if err := f.pages.RecordArtifactRefs(t.Context(), f.receipt, f.a.MessageID, []string{"ref-own"}); err != nil {
		t.Fatal(err)
	}
	if code := artifactRefsStartRun(t, f, f.a.RunID, f.a.MessageID); code != 201 {
		t.Fatalf("first admission: %d", code)
	}
	mustExec(t, f.rig.db, `UPDATE pages SET archived_at='2026-11-13T10:00:00Z' WHERE id='ref-own'`)
	if code := artifactRefsStartRun(t, f, f.a.RunID, f.a.MessageID); code != 201 {
		t.Fatalf("exact retry after admission: %d", code)
	}
}
