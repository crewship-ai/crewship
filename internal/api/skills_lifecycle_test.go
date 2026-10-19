package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

// Archive and Restore are the owner's side of the keeper's skill lifecycle
// (ODCH-SK-07): the Archived view needs a way back and a way to archive by hand.
func TestSkillArchiveRestore(t *testing.T) {
	tests := []struct {
		name       string
		action     string // "archive" or "restore"
		startState string
		source     string // defaults to CUSTOM
		role       string
		skillID    string
		wantCode   int
		wantState  string
		wantEntry  journal.EntryType
	}{
		{"archive an active skill", "archive", "active", "", "OWNER", "sk-life", http.StatusOK, "archived", journal.EntrySkillArchived},
		{"archive a stale skill", "archive", "stale", "", "ADMIN", "sk-life", http.StatusOK, "archived", journal.EntrySkillArchived},
		{"archiving twice is idempotent", "archive", "archived", "", "OWNER", "sk-life", http.StatusOK, "archived", ""},
		{"restore an archived skill", "restore", "archived", "", "OWNER", "sk-life", http.StatusOK, "active", journal.EntrySkillRestored},
		{"restoring an active skill is a no-op", "restore", "active", "", "OWNER", "sk-life", http.StatusOK, "active", ""},
		{"deprecated is terminal: archive refused", "archive", "deprecated", "", "OWNER", "sk-life", http.StatusConflict, "deprecated", ""},
		{"deprecated is terminal: restore refused", "restore", "deprecated", "", "OWNER", "sk-life", http.StatusConflict, "deprecated", ""},
		{"bundled skills are shared: archive refused", "archive", "active", "BUNDLED", "OWNER", "sk-life", http.StatusForbidden, "active", ""},
		{"below manage is forbidden", "archive", "active", "", "MANAGER", "sk-life", http.StatusForbidden, "active", ""},
		{"unknown skill", "archive", "active", "", "OWNER", "ghost", http.StatusNotFound, "active", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			userID := seedTestUser(t, db)
			wsID := seedTestWorkspace(t, db, userID)
			seedSkillForTest(t, db, "sk-life", "life", "CODING")
			execOrFatal(t, db, `UPDATE skills SET lifecycle_state = ?, source = COALESCE(NULLIF(?, ''), source) WHERE id = 'sk-life'`, tc.startState, tc.source)

			h := NewSkillHandler(db, newTestLogger())
			rec := &recordingEmitter{}
			h.SetJournal(rec)
			req := httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/skills/"+tc.skillID+"/"+tc.action, nil)
			req.SetPathValue("skillId", tc.skillID)
			req = withWorkspaceUser(req, userID, wsID, tc.role)
			rr := httptest.NewRecorder()
			if tc.action == "archive" {
				h.Archive(rr, req)
			} else {
				h.Restore(rr, req)
			}
			if rr.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d: %s", rr.Code, tc.wantCode, rr.Body.String())
			}
			var got string
			if err := db.QueryRow(`SELECT lifecycle_state FROM skills WHERE id = 'sk-life'`).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != tc.wantState {
				t.Errorf("lifecycle_state = %q, want %q", got, tc.wantState)
			}
			switch {
			case tc.wantEntry == "" && len(rec.entries) != 0:
				t.Errorf("unexpected journal entries: %+v", rec.entries)
			case tc.wantEntry != "" && (len(rec.entries) != 1 || rec.entries[0].Type != tc.wantEntry):
				t.Errorf("journal entries = %+v, want one %s", rec.entries, tc.wantEntry)
			}
		})
	}
}
