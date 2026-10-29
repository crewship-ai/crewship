package api

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/mooring"
)

func captureCollaborationAgentDecisionFixture(t *testing.T) (*collaborationAgentTestFixture, collaborationAgentPageAccess) {
	t.Helper()
	x := newCollaborationAgentTestFixture(t)
	handle := x.attach(t, x.prepare(t))
	var captured collaborationAgentPageAccess
	x.tx(t, func(tx *sql.Tx) {
		var err error
		captured, err = x.store.Check(x.request(), tx, handle, x.key, "task")
		if err != nil {
			t.Fatal(err)
		}
	})
	return x, captured
}

func checkCollaborationAgentDecision(t *testing.T, x *collaborationAgentTestFixture, captured collaborationAgentPageAccess, key mooring.SessionKey, want collaborationAgentDecisionKind) {
	t.Helper()
	x.tx(t, func(tx *sql.Tx) {
		decision, err := x.store.DecideCaptured(t.Context(), tx, captured, key, "task")
		if decision.kind != want || (err == nil) != (want != collaborationAgentUnqualified) {
			t.Fatalf("kind=%v want=%v err=%v", decision.kind, want, err)
		}
		if want == collaborationAgentAllowed && (decision.access.agentID != captured.agentID || decision.access.credentialExpiry.After(captured.credentialExpiry)) {
			t.Fatal("decision changed identity or extended original proof horizon")
		}
		if want != collaborationAgentAllowed && decision.access.store != nil {
			t.Fatal("denied/uncertain decision supplied an allowed proof")
		}
	})
}

func TestCollaborationAgentCapturedDecisionCanonicalChangesAndABA(t *testing.T) {
	for _, mutation := range []string{
		`DELETE FROM page_grants WHERE page_id='agent-shared-page'`,
		`UPDATE user_sessions SET revoked_at='2026-01-01T00:00:00Z' WHERE id='original-session'; UPDATE user_sessions SET revoked_at=NULL WHERE id='original-session'`,
		`UPDATE workspace_artifact_creation_policy SET enabled=0; UPDATE workspace_artifact_creation_policy SET enabled=1`,
	} {
		t.Run(mutation[:16], func(t *testing.T) {
			x, captured := captureCollaborationAgentDecisionFixture(t)
			checkCollaborationAgentDecision(t, x, captured, x.key, collaborationAgentAllowed)
			mustExec(t, x.f.rig.db, mutation)
			// A new Check is refused, but the original sealed participant proof
			// can establish the canonical same-writer epoch change without refresh.
			x.check(t, x.request(), captured.mandateRef, false)
			checkCollaborationAgentDecision(t, x, captured, x.key, collaborationAgentCanonicalPermissionChanged)
		})
	}
}

func TestCollaborationAgentCapturedDecisionSQLUncertaintyNeverCanonical(t *testing.T) {
	for _, table := range []string{"workspaces", "work_attempts", "workspace_artifact_creation_crews", "journal_entries", "artifact_run_principals"} {
		t.Run(table, func(t *testing.T) {
			x, captured := captureCollaborationAgentDecisionFixture(t)
			tx, err := x.f.rig.db.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err = tx.Exec(`UPDATE mooring_session_writer_fence SET seq=seq WHERE id=1`); err != nil {
				t.Fatal(err)
			}
			// Private synthetic test schema only. This provokes an actual failed
			// SQL read in the scalar fence, loader or bool app-policy helper.
			if _, err = tx.Exec(`ALTER TABLE ` + table + ` RENAME TO decision_hidden_table`); err != nil {
				t.Fatal(err)
			}
			decision, err := x.store.DecideCaptured(t.Context(), tx, captured, x.key, "task")
			if err == nil || decision.kind != collaborationAgentUnqualified {
				t.Fatalf("storage uncertainty became terminal: kind=%v err=%v", decision.kind, err)
			}
		})
	}
}

func TestCollaborationAgentCapturedDecisionRequiresOriginalHostProof(t *testing.T) {
	x, captured := captureCollaborationAgentDecisionFixture(t)
	checkCollaborationAgentDecision(t, x, collaborationAgentPageAccess{}, x.key, collaborationAgentUnqualified)
	other := x.key
	other.ArtifactID = "other-page"
	checkCollaborationAgentDecision(t, x, captured, other, collaborationAgentUnqualified)
	forged := captured
	forged.credentialBinding[0] ^= 1
	checkCollaborationAgentDecision(t, x, forged, x.key, collaborationAgentUnqualified)
	x.f.pages.artifactMasterToken = "rotated-synthetic-mandate-master"
	checkCollaborationAgentDecision(t, x, captured, x.key, collaborationAgentUnqualified)
}

func TestCollaborationAgentCapturedDecisionReadProofCannotAdoptTaskWriteScope(t *testing.T) {
	x, original := captureCollaborationAgentDecisionFixture(t)
	var readProof collaborationAgentPageAccess
	x.tx(t, func(tx *sql.Tx) {
		var err error
		readProof, err = x.store.Check(x.request(), tx, original.mandateRef, x.key, "read")
		if err != nil {
			t.Fatal(err)
		}
	})
	// The human task's ceiling is write, but this original authenticated proof
	// was checked for read. It must neither authorize a write nor produce a
	// terminal permission-change decision for a write submission.
	checkCollaborationAgentDecision(t, x, readProof, x.key, collaborationAgentUnqualified)
	mustExec(t, x.f.rig.db, `DELETE FROM page_grants WHERE page_id='agent-shared-page'`)
	checkCollaborationAgentDecision(t, x, readProof, x.key, collaborationAgentUnqualified)
	x.tx(t, func(tx *sql.Tx) {
		decision, err := x.store.DecideCaptured(t.Context(), tx, readProof, x.key, "read")
		if err != nil || decision.kind != collaborationAgentCanonicalPermissionChanged {
			t.Fatalf("original read scope lost its known revocation: kind=%v err=%v", decision.kind, err)
		}
	})
}

func TestCollaborationAgentCapturedDecisionCannotUseRetiredRuntimeOrIngress(t *testing.T) {
	for _, mode := range []string{"runtime", "restore", "expiry", "cancelled-context", "principal", "journal", "chat"} {
		t.Run(mode, func(t *testing.T) {
			x, captured := captureCollaborationAgentDecisionFixture(t)
			mustExec(t, x.f.rig.db, `DELETE FROM page_grants WHERE page_id='agent-shared-page'`)
			switch mode {
			case "runtime":
				mustExec(t, x.f.rig.db, `UPDATE work_attempts SET runtime_locator='replacement-runtime'`)
			case "principal":
				mustExec(t, x.f.rig.db, `DELETE FROM artifact_run_principals WHERE run_id='principal-run'`)
			case "journal":
				mustExec(t, x.f.rig.db, `INSERT INTO journal_entries(id,workspace_id,agent_id,entry_type,actor_type,summary,trace_id) VALUES('decision-ended',?,'principal-agent','run.completed','system','synthetic decision test','principal-run')`, x.f.rig.wsID)
			case "chat":
				mustExec(t, x.f.rig.db, `UPDATE chats SET authority_generation=authority_generation+1 WHERE id=?`, x.f.a.ChatID)
			case "restore":
				x.fence.SuspendBeforeRestore()
				if err := x.fence.ResumeAfterRestore(); err != nil {
					t.Fatal(err)
				}
			case "expiry":
				x.clock.advance(time.Minute)
			case "cancelled-context":
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				x.tx(t, func(tx *sql.Tx) {
					decision, err := x.store.DecideCaptured(ctx, tx, captured, x.key, "task")
					if err == nil || decision.kind != collaborationAgentUnqualified {
						t.Fatal("cancelled SQL context became a canonical decision")
					}
				})
				return
			}
			checkCollaborationAgentDecision(t, x, captured, x.key, collaborationAgentUnqualified)
		})
	}
}
