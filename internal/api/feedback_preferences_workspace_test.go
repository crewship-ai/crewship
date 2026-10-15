package api

import (
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/usermodel"
)

func TestFeedbackForeignRestrictionPreservesSavedPreference(t *testing.T) {
	for _, mode := range []string{"insert", "update", "upgrade"} {
		t.Run(mode, func(t *testing.T) {
			b := savedClosurePreference(t)
			// A guest elsewhere is valid for a regular member, not a global admin.
			execOrFatal(t, b.h.db, `UPDATE workspace_members SET role='MEMBER' WHERE user_id=?`, b.userID)
			if mode == "upgrade" {
				// Simulate a pre-fix database before installing the replacement triggers.
				// Leave the old trigger names in place: the upgrade must replace them.
				execOrFatal(t, b.h.db, `DROP TRIGGER feedback_preference_restricted_insert;
CREATE TRIGGER feedback_preference_restricted_insert AFTER INSERT ON workspace_members BEGIN SELECT 1; END;
DROP TRIGGER feedback_preference_restricted_update;
CREATE TRIGGER feedback_preference_restricted_update AFTER UPDATE OF access_mode ON workspace_members BEGIN SELECT 1; END;`)
			}
			initial := "restricted"
			if mode == "update" {
				initial = "trusted"
			}
			execOrFatal(t, b.h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('foreign-guest','ws-other-fb',?,'MEMBER',?)`, b.userID, initial)
			if mode == "update" {
				execOrFatal(t, b.h.db, `UPDATE workspace_members SET access_mode='restricted' WHERE id='foreign-guest'`)
			}
			if mode == "upgrade" {
				migration, err := os.ReadFile("../database/migrations/20261015203343_scope_preference_withdrawal_workspace.sql")
				if err != nil {
					t.Fatal(err)
				}
				execOrFatal(t, b.h.db, string(migration))
			}
			var status, value, reason string
			var revision int
			if err := b.h.db.QueryRow(`SELECT status,value,reason,revision FROM agent_user_preferences`).Scan(&status, &value, &reason, &revision); err != nil || status != "saved" || value == "" || reason == "" || revision != 1 {
				t.Fatalf("foreign membership destroyed memory: %s %q %q rev%d %v", status, value, reason, revision, err)
			}
			// Keep the existing account-wide recall ceiling; do not grant new access.
			body, err := usermodel.ReadFeedbackPreferences(t.Context(), b.h.db, b.wsID, "agent-fb", b.userID)
			if err != nil || body != "" {
				t.Fatalf("restricted recall: %q %v", body, err)
			}
			execOrFatal(t, b.h.db, `UPDATE workspace_members SET access_mode='trusted' WHERE id='foreign-guest'`)
			body, err = usermodel.ReadFeedbackPreferences(t.Context(), b.h.db, b.wsID, "agent-fb", b.userID)
			if err != nil || !strings.Contains(body, "concise") {
				t.Fatalf("foreign restriction permanently lost memory: %q %v", body, err)
			}
		})
	}
}
