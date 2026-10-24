package api

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"
)

func TestCollaborationAgentAttemptRequiresActualConfirmedRunningLedger(t *testing.T) {
	f := originalUserPrincipalFixture(t)
	f.admit(t)
	// Preserve legacy generic behavior while refusing its journal-only fallback
	// for new Mooring. This fixture is SQL identity validation, not live E3.
	if err := f.pages.checkArtifactRunPrincipal(t.Context(), &f.a); err != nil {
		t.Fatal("legacy fixture", err)
	}
	check := func(want bool) collaborationAgentAttemptBinding {
		t.Helper()
		tx, e := f.rig.db.BeginTx(t.Context(), nil)
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback()
		if _, e = tx.Exec(`UPDATE work_items SET state=state WHERE id='collab-agent-work'`); e != nil {
			t.Fatal(e)
		}
		a, h, e := loadCollaborationAgentAttempt(t.Context(), tx, f.a.RunID)
		if (e == nil) != want {
			t.Fatalf("allowed=%v want=%v err=%v", e == nil, want, e)
		}
		if want && (a.workID != "collab-agent-work" || a.runID != f.a.RunID || a.attempt != 1 || a.generation != 1 || !time.Now().Before(h)) {
			t.Fatal("missing exact attempt/horizon")
		}
		return a
	}
	check(false)
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	digest := sha256.Sum256([]byte("{}"))
	hash := hex.EncodeToString(digest[:])
	mustExec(t, f.rig.db, `INSERT INTO work_items(id,workspace_id,source,source_ref,agent_id,crew_id,session_id,class,authorized_by_user_id,authorized_scope,input_json,input_sha256,state,generation,attempts,eligible_at,created_at,updated_at) VALUES('collab-agent-work',?,'chat',?,?,?,?,'chat',?,'pages:write','{}',?,'running',1,1,?,?,?)`, f.a.WorkspaceID, f.a.MessageID, f.a.AgentID, f.a.CrewID, f.a.ChatID, f.a.HumanUserID, hash, stamp, stamp, stamp)
	mustExec(t, f.rig.db, `INSERT INTO work_attempts(run_id,work_id,attempt,generation,lease_owner,lease_expires_at,heartbeat_at,runtime_locator,runtime_phase,started_at) VALUES(?,'collab-agent-work',1,1,'host-fixture',?,?,'fixture-runtime','confirmed',?)`, f.a.RunID, future, stamp, stamp)
	before := check(true)
	for _, tc := range []struct{ name, change, restore string }{
		{"starting", `UPDATE work_items SET state='starting'`, `UPDATE work_items SET state='running'`},
		{"reconciliation", `UPDATE work_items SET state='needs_reconciliation'`, `UPDATE work_items SET state='running'`},
		{"unconfirmed", `UPDATE work_attempts SET runtime_phase='starting'`, `UPDATE work_attempts SET runtime_phase='confirmed'`},
		{"generation", `UPDATE work_items SET generation=2`, `UPDATE work_items SET generation=1`},
		{"attempt ordinal", `UPDATE work_items SET attempts=2`, `UPDATE work_items SET attempts=1`},
		{"ended", `UPDATE work_attempts SET ended_at='2026-01-01T00:00:00Z'`, `UPDATE work_attempts SET ended_at=NULL`},
		{"terminal marker", `UPDATE work_items SET terminal_at='2026-01-01T00:00:00Z'`, `UPDATE work_items SET terminal_at=NULL`},
		{"input mismatch", `UPDATE work_items SET input_json='{"instruction":"changed"}'`, `UPDATE work_items SET input_json='{}'`},
		{"no owner", `UPDATE work_attempts SET lease_owner=''`, `UPDATE work_attempts SET lease_owner='host-fixture'`},
		{"no locator", `UPDATE work_attempts SET runtime_locator=''`, `UPDATE work_attempts SET runtime_locator='fixture-runtime'`},
		{"lease expired", `UPDATE work_attempts SET lease_expires_at='2026-01-01T00:00:00Z'`, "UPDATE work_attempts SET lease_expires_at='" + future + "'"},
		{"deadline expired", `UPDATE work_items SET deadline_at='2026-01-01T00:00:00Z'`, `UPDATE work_items SET deadline_at=NULL`},
		{"missing human", `UPDATE work_items SET authorized_by_user_id=''`, "UPDATE work_items SET authorized_by_user_id='" + f.a.HumanUserID + "'"},
		{"nonchat", `UPDATE work_items SET source='webhook'`, `UPDATE work_items SET source='chat'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mustExec(t, f.rig.db, tc.change)
			check(false)
			mustExec(t, f.rig.db, tc.restore)
			check(true)
		})
	}
	mustExec(t, f.rig.db, `UPDATE work_attempts SET runtime_locator='recovered-runtime'`)
	if after := check(true); after == before {
		t.Fatal("recovery locator change was not exposed to immutable mandate comparison")
	}
	mustExec(t, f.rig.db, `DELETE FROM work_items WHERE id='collab-agent-work'`)
	check(false)
}
