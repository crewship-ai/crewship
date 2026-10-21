package api

import (
	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/journal"
	"net/http/httptest"
	"testing"
)

func TestVerifiedTextRestoreCannotDeclassifyMissingSource(t *testing.T) {
	d := newDecisionTestRig(t)
	if err := d.router.queryHandler.journal.(*journal.Writer).Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	var inboxID, journalID string
	if err := d.f.h.db.QueryRow(`SELECT id FROM inbox_items WHERE source_id=? AND kind='escalation'`, d.id).Scan(&inboxID); err != nil {
		t.Fatal(err)
	}
	if err := d.f.h.db.QueryRow(`SELECT id FROM journal_entries WHERE entry_type='peer.escalation' AND json_extract(refs,'$.escalation_id')=?`, d.id).Scan(&journalID); err != nil {
		t.Fatal(err)
	}
	dump, err := backup.DumpWorkspace(t.Context(), d.f.h.db, d.f.workspace)
	if err != nil {
		t.Fatal(err)
	}
	target := setupTestDB(t)
	if err = backup.RestoreDump(t.Context(), target, dump); err != nil {
		t.Fatal(err)
	}
	var sourceCount int
	if err = target.QueryRow(`SELECT COUNT(*) FROM escalations WHERE id=?`, d.id).Scan(&sourceCount); err != nil || sourceCount != 0 {
		t.Fatalf("expected existing ephemeral-source restore contract, count%d %v", sourceCount, err)
	}
	for _, tc := range []struct{ name, id string }{{"inbox", inboxID}, {"journal", journalID}} {
		req := withWorkspaceUser(httptest.NewRequest("GET", "/", nil), d.f.user, d.f.workspace, "OWNER")
		req.SetPathValue("id", tc.id)
		rr := httptest.NewRecorder()
		if tc.name == "inbox" {
			NewInboxHandler(target, newTestLogger(), nil).Get(rr, req)
		} else {
			NewJournalHandler(target, newTestLogger(), nil).Get(rr, req)
		}
		if rr.Code != 404 {
			t.Errorf("restore declassified verified TEXT %s%d %s", tc.name, rr.Code, rr.Body.String())
		}
	}
}
