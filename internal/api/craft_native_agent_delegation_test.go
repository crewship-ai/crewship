package api

import (
	"errors"
	"testing"
)

// R71 §0.14/1 and §0.3/4 for native Craft documents (#5235): an agent task
// borrows its author's Edit only while the author owns the document. A
// workspace ADMIN or a human Edit grantee who does not own it cannot start one,
// unless the owner gave that agent write; a running task stops when ownership
// moves away from its author.
func TestCraftNativeAgentTaskNeedsOwnerOrOwnerDelegation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		role      string
		humanEdit bool
		agentEdit bool
		admitted  bool
	}{
		{"workspace admin who does not own the document", "ADMIN", false, false, false},
		{"human Edit grantee", "MEMBER", true, false, false},
		{"human Edit grantee with the owner's agent grant", "MEMBER", true, true, true},
		{"workspace admin with the owner's agent grant", "ADMIN", false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, m, ctx := nativeAgentAdmissionFixture(t)
			mustExec(t, f.rig.db, `UPDATE workspace_members SET role=? WHERE workspace_id=? AND user_id=?`, tc.role, f.a.WorkspaceID, f.a.HumanUserID)
			mustExec(t, f.rig.db, `UPDATE pages SET owner_user_id=? WHERE id='native-agent-page'`, f.rig.adminID)
			if tc.humanEdit {
				mustExec(t, f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('native-agent-page','user',?,'write',?)`, f.a.HumanUserID, f.rig.adminID)
			}
			if tc.agentEdit {
				mustExec(t, f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('native-agent-page','agent',?,'write',?)`, f.a.AgentID, f.rig.adminID)
			}
			accepted, err := m.produceTask(ctx, "@native-agent-page", craftNativeAgentTaskRequest{ChatID: f.a.ChatID, MessageID: f.a.MessageID, WholeDocument: true})
			if tc.admitted {
				if err != nil || accepted.WorkID == "" {
					t.Fatalf("owner-delegated agent task refused: %+v %v", accepted, err)
				}
				return
			}
			if !errors.Is(err, errCraftNativeAgentDenied) || accepted.WorkID != "" {
				t.Fatalf("undelegated agent task admitted: %+v %v", accepted, err)
			}
			var items int
			if err = f.rig.db.QueryRow(`SELECT count(*) FROM work_items WHERE domain_kind=?`, craftNativeAgentDomain).Scan(&items); err != nil || items != 0 {
				t.Fatal("refused task left a work item", items, err)
			}
		})
	}
}

// Admitted while the author owned the document, the task stops at its next
// recheck once ownership moves to someone else, even though the author keeps
// an Edit grant.
func TestCraftNativeAgentRecheckStopsWhenAuthorNoLongerOwns(t *testing.T) {
	f, m, ctx := nativeAgentAdmissionFixture(t)
	accepted, err := m.produceTask(ctx, "@native-agent-page", craftNativeAgentTaskRequest{ChatID: f.a.ChatID, MessageID: f.a.MessageID, WholeDocument: true})
	if err != nil {
		t.Fatal("owner admission", err)
	}
	m.mu.Lock()
	e := m.entries[accepted.WorkID]
	m.mu.Unlock()
	check := func() error {
		tx, err := f.rig.db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		return m.checkOriginalGrant(t.Context(), tx, e.grant)
	}
	if err = check(); err != nil {
		t.Fatal("owner recheck", err)
	}
	mustExec(t, f.rig.db, `UPDATE pages SET owner_user_id=? WHERE id='native-agent-page'`, f.rig.adminID)
	mustExec(t, f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('native-agent-page','user',?,'write',?)`, f.a.HumanUserID, f.rig.adminID)
	if err = check(); !errors.Is(err, errCraftNativeAgentDenied) {
		t.Fatalf("task kept writing after its author stopped owning the document: %v", err)
	}
}
