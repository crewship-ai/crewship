package api

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/documents"
)

// Real B2 admission and journal plus B3 policy, without exposing a new route.
func documentCreationAdmissionFixture(t *testing.T, drafting ...bool) (*artifactPrincipalFixture, documents.CreationIntent) {
	t.Helper()
	f := newArtifactPrincipalFixture(t)
	mustExec(t, f.rig.db, `UPDATE workspace_members SET capabilities='["chat","page.create"]' WHERE user_id=?`, f.a.HumanUserID)
	var err error
	f.receipt, err = chataudience.CaptureTrusted(t.Context(), f.rig.db, f.a.ChatID, f.a.HumanUserID, nil)
	if err != nil || f.receipt == nil {
		t.Fatalf("creation receipt: %+v %v", f.receipt, err)
	}
	f.a.MessageID = "creation-message"
	intent := documents.CreationIntent{SubmissionID: "explicit-consent"}
	if len(drafting) > 0 {
		intent.Drafting = drafting[0]
	}
	store := conversation.NewStore(t.TempDir(), newTestLogger(), conversation.WithDB(f.rig.db))
	t.Cleanup(store.Close)
	msg := conversation.Message{ID: f.a.MessageID, AgentID: f.a.AgentID, Role: conversation.RoleUser, AuthorUserID: f.a.HumanUserID, Content: "Write a complete document", Metadata: map[string]any{documents.CreationMetadataKey: intent}}
	if err := store.AppendDocumentCreation(t.Context(), f.a.ChatID, msg, intent, f.receipt); err != nil {
		t.Fatal(err)
	}
	f.admit(t)
	mustExec(t, f.rig.db, `INSERT INTO workspace_artifact_creation_policy(workspace_id,enabled,version,updated_at) VALUES(?,1,1,'2026-10-17T00:00:00Z')`, f.a.WorkspaceID)
	mustExec(t, f.rig.db, `INSERT INTO workspace_artifact_creation_crews(workspace_id,crew_id) VALUES(?,?)`, f.a.WorkspaceID, f.a.CrewID)
	if err := f.pages.checkArtifactRunPrincipal(t.Context(), &f.a); err != nil {
		t.Fatalf("fixture principal: %v", err)
	}
	if !f.pages.artifactPrincipalMayCreate(t.Context(), f.a.WorkspaceID, f.a.HumanUserID) || !f.pages.artifactAgentPolicyAllows(t.Context(), f.a.WorkspaceID, f.a.CrewID) {
		t.Fatal("fixture lacks live human create capability or crew policy")
	}
	return f, intent
}

func bindCreation(f *artifactPrincipalFixture, intent documents.CreationIntent) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return bindDocumentCreationRun(httptest.NewRequest("POST", "/", nil).WithContext(ctx), f.rig.db, f.a.RunID, f.a.MessageID, f.receipt, intent)
}

func TestDocumentCreationBindingExactRetryAndExpiry(t *testing.T) {
	f, intent := documentCreationAdmissionFixture(t)
	// A deliberately earlier live expiry detects accidental renewal to a new hour.
	mustExec(t, f.rig.db, `UPDATE artifact_run_principals SET expires_at=? WHERE run_id=?`, time.Now().Add(17*time.Minute).UTC().Format(time.RFC3339Nano), f.a.RunID)
	// One connection catches accidental reads through db while holding the writer transaction.
	f.rig.db.SetMaxOpenConns(1)
	if err := bindCreation(f, intent); err != nil {
		t.Fatal(err)
	}
	var original, stored, run string
	if err := f.rig.db.QueryRow(`SELECT expires_at FROM artifact_run_principals WHERE run_id=?`, f.a.RunID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if err := bindCreation(f, intent); err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	if err := f.rig.db.QueryRow(`SELECT run_id,expires_at FROM document_creation_consents WHERE message_id=?`, f.a.MessageID).Scan(&run, &stored); err != nil || run != f.a.RunID || stored != original {
		t.Fatalf("binding/expiry changed: %s %s %s %v", run, stored, original, err)
	}
	if _, err := f.rig.db.Exec(`UPDATE document_creation_consents SET expires_at='2100-01-01T00:00:00Z'`); err == nil {
		t.Fatal("bound expiry mutable")
	}
	if _, err := f.rig.db.Exec(`UPDATE document_creation_consents SET submission_id='changed'`); err == nil {
		t.Fatal("consent identity mutable")
	}
	// Even another valid B2 run cannot acquire this submission.
	other := "another-creation-run"
	if err := captureArtifactChatPrincipal(httptest.NewRequest("POST", "/", nil), f.rig.db, other, f.a.WorkspaceID, f.a.AgentID, f.a.CrewID, f.a.ChatID, f.a.MessageID, f.receipt); err != nil {
		t.Fatal(err)
	}
	mustExec(t, f.rig.db, `INSERT INTO journal_entries(id,workspace_id,crew_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('other-start',?,?,?,'run.started','system','test',?)`, f.a.WorkspaceID, f.a.CrewID, f.a.AgentID, other)
	if err := bindDocumentCreationRun(httptest.NewRequest("POST", "/", nil), f.rig.db, other, f.a.MessageID, f.receipt, intent); err == nil {
		t.Fatal("consent rebound to another live run")
	}
	if err := f.rig.db.QueryRow(`SELECT run_id,expires_at FROM document_creation_consents WHERE message_id=?`, f.a.MessageID).Scan(&run, &stored); err != nil || run != f.a.RunID || stored != original {
		t.Fatalf("denied rebinding changed original: %s %s %v", run, stored, err)
	}
}

func TestDocumentCreationBindingRejectsUntrustedAdmission(t *testing.T) {
	for _, change := range []string{"crew transport", "workspace transport", "no receipt", "forged human", "forged epoch", "missing message", "missing principal", "different intent", "selected origin", "revoked consent", "missing consent", "changed message author", "changed message role"} {
		t.Run(change, func(t *testing.T) {
			f, intent := documentCreationAdmissionFixture(t)
			r := httptest.NewRequest("POST", "/", nil)
			receipt := *f.receipt
			provided := &receipt
			message := f.a.MessageID
			switch change {
			case "crew transport":
				r = r.WithContext(context.WithValue(r.Context(), ctxInternalTokenCrew, f.a.CrewID))
			case "workspace transport":
				r = r.WithContext(context.WithValue(r.Context(), ctxInternalTokenWS, f.a.WorkspaceID))
			case "no receipt":
				provided = nil
			case "forged human":
				receipt.UserID = f.rig.adminID
			case "forged epoch":
				receipt.MemberRevision++
			case "missing message":
				message = "missing-message"
			case "missing principal":
				mustExec(t, f.rig.db, `DELETE FROM artifact_run_principals`)
			case "different intent":
				intent.SubmissionID = "changed"
			case "selected origin":
				mustExec(t, f.rig.db, `UPDATE artifact_run_principals SET slides_request_origin='selected-request'`)
			case "revoked consent":
				mustExec(t, f.rig.db, `UPDATE document_creation_consents SET revoked_at='2026-10-17T00:00:00Z'`)
			case "missing consent":
				mustExec(t, f.rig.db, `DELETE FROM document_creation_consents`)
			case "changed message author":
				mustExec(t, f.rig.db, `UPDATE conversation_messages SET author_user_id=? WHERE id=?`, f.rig.adminID, f.a.MessageID)
			case "changed message role":
				mustExec(t, f.rig.db, `UPDATE conversation_messages SET role='assistant' WHERE id=?`, f.a.MessageID)
			}
			if err := bindDocumentCreationRun(r, f.rig.db, f.a.RunID, message, provided, intent); err == nil {
				t.Fatal("untrusted creation binding admitted")
			}
			var run sql.NullString
			err := f.rig.db.QueryRow(`SELECT run_id FROM document_creation_consents WHERE message_id=?`, f.a.MessageID).Scan(&run)
			if err != nil && err != sql.ErrNoRows {
				t.Fatal(err)
			}
			if run.Valid {
				t.Fatal("denied admission bound consent")
			}
		})
	}
}

func TestDocumentCreationBindingRechecksLiveAuthority(t *testing.T) {
	for _, tc := range []struct{ name, mutation string }{
		{"expired", `UPDATE artifact_run_principals SET expires_at='2000-01-01T00:00:00Z'`},
		{"malformed expiry", `UPDATE artifact_run_principals SET expires_at='bad'`},
		{"membership epoch", `UPDATE workspace_members SET access_revision=access_revision+1 WHERE user_id='ptdeparting'`},
		{"chat epoch", `UPDATE chats SET authority_revision=authority_revision+1 WHERE id='principal-chat'`},
		{"suspended human", `UPDATE users SET suspended_at='2026-10-17T00:00:00Z' WHERE id='ptdeparting'`},
		{"disabled policy", `UPDATE workspace_artifact_creation_policy SET enabled=0`},
		{"removed crew allowlist", `DELETE FROM workspace_artifact_creation_crews`},
		{"create capability revoked", `UPDATE workspace_members SET capabilities='[]' WHERE user_id='ptdeparting'`},
		{"terminal run", `INSERT INTO journal_entries(id,workspace_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('ended','ptws1','principal-agent','run.completed','system','test','principal-run')`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, intent := documentCreationAdmissionFixture(t)
			mustExec(t, f.rig.db, tc.mutation)
			if err := bindCreation(f, intent); err == nil {
				t.Fatal("revoked creation authority admitted")
			}
			var n int
			if err := f.rig.db.QueryRow(`SELECT COUNT(*) FROM document_creation_consents WHERE run_id IS NOT NULL`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("denied admission changed consent: %d %v", n, err)
			}
		})
	}
}
