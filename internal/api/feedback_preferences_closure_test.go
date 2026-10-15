package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/usermodel"
)

func savedClosurePreference(t *testing.T) *feedbackTestBed {
	t.Helper()
	b := setupFeedbackTestBed(t)
	execOrFatal(t, b.h.db, `UPDATE agents SET memory_enabled=1`)
	execOrFatal(t, b.h.db, `UPDATE conversation_messages SET role='assistant'`)
	rec := httptest.NewRecorder()
	b.h.Create(rec, feedbackReq("POST", "/api/v1/feedback", fmt.Sprintf(`{"message_id":%q,"signal":"helpful","reason":"Next time, keep answers concise."}`, b.messageID), b.userID))
	if rec.Code != http.StatusCreated {
		t.Fatalf("feedback: %d %s", rec.Code, rec.Body)
	}
	if err := usermodel.ProcessFeedbackPreferences(t.Context(), b.h.db); err != nil {
		t.Fatal(err)
	}
	body, err := usermodel.ReadFeedbackPreferences(t.Context(), b.h.db, b.wsID, "agent-fb", b.userID)
	if err != nil || !strings.Contains(body, "concise") {
		t.Fatalf("initial recall: %q %v", body, err)
	}
	return b
}

func TestFeedbackClosureDeleteAllWithoutCrew(t *testing.T) {
	b := savedClosurePreference(t)
	execOrFatal(t, b.h.db, `UPDATE agents SET crew_id=NULL WHERE id='agent-fb'`)
	// Structured memory has no dependency on a Markdown storage directory.
	h := NewPeerCardHandler(b.h.db, newTestLogger(), "")
	for range 2 {
		rec := httptest.NewRecorder()
		h.DeleteAgentPeer(rec, covPH2PeerReq(http.MethodDelete, b.wsID, b.userID, map[string]string{"agentId": "agent-fb", "userId": b.userID}))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("delete: %d %s", rec.Code, rec.Body)
		}
	}
	var status, value string
	var revision int
	if err := b.h.db.QueryRow(`SELECT status,value,revision FROM agent_user_preferences`).Scan(&status, &value, &revision); err != nil || status != "withdrawn" || value != "" || revision != 2 {
		t.Fatalf("tombstone: %s %q rev%d %v", status, value, revision, err)
	}
}

func TestFeedbackClosureRestrictedTransitionDoesNotResurrect(t *testing.T) {
	for _, mode := range []string{"update", "insert"} {
		t.Run(mode, func(t *testing.T) {
			b := savedClosurePreference(t)
			if mode == "update" {
				// Local restriction permanently withdraws memory in this workspace.
				execOrFatal(t, b.h.db, `UPDATE workspace_members SET role='MEMBER',access_mode='restricted' WHERE user_id=?`, b.userID)
			} else {
				execOrFatal(t, b.h.db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, b.wsID, b.userID)
				execOrFatal(t, b.h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('restricted-extra',?,?,'MEMBER','restricted')`, b.wsID, b.userID)
			}
			execOrFatal(t, b.h.db, `UPDATE workspace_members SET access_mode='trusted' WHERE user_id=?`, b.userID)
			if err := usermodel.ProcessFeedbackPreferences(t.Context(), b.h.db); err != nil {
				t.Fatal(err)
			}
			body, err := usermodel.ReadFeedbackPreferences(t.Context(), b.h.db, b.wsID, "agent-fb", b.userID)
			if err != nil || body != "" {
				t.Fatalf("restricted transition resurrected preference: %q %v", body, err)
			}
			var status, reason string
			var source, revision int
			if err := b.h.db.QueryRow(`SELECT status,reason,source_revision,revision FROM agent_user_preferences`).Scan(&status, &reason, &source, &revision); err != nil || status != "withdrawn" || reason != "" || source != 1 || revision != 2 {
				t.Fatalf("withdrawn source: %s %q source%d rev%d %v", status, reason, source, revision, err)
			}
		})
	}
}

func TestFeedbackClosureMigrationWithdrawsExistingRestrictedSubjects(t *testing.T) {
	b := savedClosurePreference(t)
	// Recreate the pre-upgrade state: recall is hidden but the saved value remains.
	execOrFatal(t, b.h.db, `DROP TRIGGER feedback_preference_restricted_insert;
CREATE TRIGGER feedback_preference_restricted_insert AFTER INSERT ON workspace_members BEGIN SELECT 1; END;
DROP TRIGGER feedback_preference_restricted_update;
CREATE TRIGGER feedback_preference_restricted_update AFTER UPDATE OF access_mode ON workspace_members BEGIN SELECT 1; END;`)
	execOrFatal(t, b.h.db, `UPDATE workspace_members SET role='MEMBER',access_mode='restricted' WHERE user_id=?`, b.userID)
	migration, err := os.ReadFile("../database/migrations/20261015203343_scope_preference_withdrawal_workspace.sql")
	if err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, b.h.db, string(migration))
	execOrFatal(t, b.h.db, `UPDATE workspace_members SET access_mode='trusted' WHERE user_id=?`, b.userID)
	body, err := usermodel.ReadFeedbackPreferences(t.Context(), b.h.db, b.wsID, "agent-fb", b.userID)
	if err != nil || body != "" {
		t.Fatalf("upgrade resurrected preference: %q %v", body, err)
	}
	var status string
	var source, revision int
	if err := b.h.db.QueryRow(`SELECT status,source_revision,revision FROM agent_user_preferences`).Scan(&status, &source, &revision); err != nil || status != "withdrawn" || source != 1 || revision != 2 {
		t.Fatalf("backfilled tombstone: %s source%d rev%d %v", status, source, revision, err)
	}
}
