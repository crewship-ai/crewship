package api

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
)

// This is an actual canonical-SQL integration regression, not an auth-provider
// or mixed Rust/SQL batch qualification. Jana and the agent's original human
// are independent users. Current rights are measured, never adopted into the
// old proof to conceal its global workspace epoch mismatch.
func TestReviewD3OtherHumanGrantRevocationDoesNotTerminallyRejectIndependentAgent(t *testing.T) {
	x := newCollaborationAgentTestFixture(t)
	mustExec(t, x.f.rig.db, `INSERT INTO users(id,email) VALUES('d3-jana','d3-jana@synthetic.invalid')`)
	mustExec(t, x.f.rig.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('d3-jana-member',?,'d3-jana','MEMBER')`, x.key.WorkspaceID)
	mustExec(t, x.f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'user','d3-jana','write',?)`, x.key.ArtifactID, x.f.a.HumanUserID)
	expiry := time.Now().Add(time.Hour)
	mustExec(t, x.f.rig.db, `INSERT INTO user_sessions(id,user_id,expires_at) VALUES('d3-jana-session','d3-jana',?)`, expiry.UTC().Format(time.RFC3339Nano))
	source, err := captureOriginalHumanCredential(t.Context(), x.f.rig.db, &AuthUser{ID: "d3-jana", SessionID: "d3-jana-session", CredentialExpiresAt: expiry}, AuthKindSession, "")
	if err != nil {
		t.Fatal(err)
	}
	jana := context.WithValue(t.Context(), ctxUser, &AuthUser{ID: source.UserID, SessionID: source.ID, OriginalCredential: source})
	jana = context.WithValue(jana, ctxAuthKind, AuthKindSession)
	jana = auth.WithOriginalCredential(jana, *source)
	jana = collaborationIngressTestContext(t, x.fence, jana)
	handle := x.attach(t, x.prepare(t))
	var human collaborationHumanPageAccess
	var agent collaborationAgentPageAccess
	x.tx(t, func(tx *sql.Tx) {
		human, err = x.f.pages.authorizeCollaborationHumanPage(jana, tx, x.key, "write")
		if err != nil {
			t.Fatal("Jana original current write", err)
		}
		agent, err = x.store.Check(x.request(), tx, handle, x.key, "task")
		if err != nil {
			t.Fatal("independent agent original current write", err)
		}
		if human.memberID == agent.memberID || source.UserID == x.f.receipt.UserID {
			t.Fatal("fixture did not establish independent humans")
		}
		decision, e := x.store.DecideCaptured(t.Context(), tx, agent, x.key, "task")
		if e != nil || decision.kind != collaborationAgentAllowed {
			t.Fatal("baseline agent decision", decision.kind, e)
		}
	})
	mustExec(t, x.f.rig.db, `DELETE FROM page_grants WHERE page_id='agent-shared-page' AND subject_type='user' AND subject_id='d3-jana'`)
	x.tx(t, func(tx *sql.Tx) {
		if _, e := x.f.pages.authorizeCollaborationHumanPage(jana, tx, x.key, "write"); e == nil {
			t.Fatal("Jana write was not actually revoked")
		}
		current, e := x.f.pages.authorizeCollaborationPageSubject(t.Context(), tx, x.key, "task", x.f.receipt.OriginalCredential())
		if e != nil {
			t.Fatal("independent original human lost canonical rights", e)
		}
		pageCtx := context.WithValue(t.Context(), pageAuthReaderKey{}, tx)
		if !collaborationAgentGrantAllows(pageCtx, x.f.pages, x.key.WorkspaceID, current.record, x.f.a.AgentID, true) || !x.f.pages.artifactAgentPolicyAllows(pageCtx, x.key.WorkspaceID, x.f.a.CrewID) {
			t.Fatal("independent agent grant/app ceiling changed")
		}
		if current.memberID != agent.memberID || current.memberRevision != agent.memberRevision {
			t.Fatal("independent original member changed")
		}
		var currentUserRevision string
		if e = tx.QueryRow(`SELECT CAST(authority_revision AS TEXT) FROM users WHERE id=?`, x.f.receipt.UserID).Scan(&currentUserRevision); e != nil || currentUserRevision != x.f.receipt.UserAuthorityRevision {
			t.Fatal("independent original source user fence changed", e)
		}
		decision, e := x.store.DecideCaptured(t.Context(), tx, agent, x.key, "task")
		if e != nil || decision.kind != collaborationAgentAllowed {
			t.Fatalf("D3 independent author rejected solely by broad workspace epoch %s->%s; same source/member, current human+agent rights ALLOW, Jana write DENY: kind=%v err=%v", agent.permissionVersion, current.permissionVersion, decision.kind, e)
		}
	})
}
