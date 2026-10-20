package api

import (
	"fmt"
	"net/http"
	"testing"
)

func TestPageGrantRevokeMatchesResolvedAndDepartedSubjects(t *testing.T) {
	for _, level := range []string{"", "read"} {
		t.Run("level="+level, func(t *testing.T) {
			f := newPageGrantAuditFixture(t)
			f.seed(t, "read", "write")
			// A departed crew's stored ID can be reused as another crew's slug.
			// Both references are revocable by the existing matching contract.
			if _, err := f.h.db.Exec(`INSERT INTO crews(id,workspace_id,name,slug) VALUES('engine',?,'Departed','departed')`, f.ws); err != nil {
				t.Fatal(err)
			}
			for _, subject := range []string{"departed", "lookout"} {
				for _, grant := range []string{"read", "write"} {
					body := fmt.Sprintf(`{"subject_type":"crew","subject":%q,"level":%q}`, subject, grant)
					rr := pagesGrantCall(t, f.h, "PUT", "/grants", f.ws, f.actor, "OWNER", "fleet-201", body)
					if rr.Code != http.StatusOK {
						t.Fatalf("healthy grant: %d", rr.Code)
					}
				}
			}
			f.chain(t)
			if _, err := f.h.db.Exec(`UPDATE crews SET deleted_at='2026-10-20' WHERE id='engine'`); err != nil {
				t.Fatal(err)
			}
			before := f.count(t, "journal_entries", "entry_type='page.grant_removed'")
			f.published.Store(0)
			rr := f.call(t, "DELETE", level)
			if rr.Code != http.StatusOK {
				t.Fatalf("multi-subject revoke status=%d want=200", rr.Code)
			}
			wantRemaining, wantRemoved := 0, 4
			if level != "" {
				wantRemaining, wantRemoved = 2, 2
			}
			if n := f.count(t, "page_grants", "subject_id IN ('crew-engine','engine')"); n != wantRemaining {
				t.Errorf("matching grants remaining=%d want=%d", n, wantRemaining)
			}
			if level != "" && f.count(t, "page_grants", "subject_id IN ('crew-engine','engine') AND level='write'") != 2 {
				t.Error("unselected levels changed")
			}
			if n := f.count(t, "page_grants", "subject_id='crew-lookout'"); n != 2 {
				t.Errorf("unrelated subject changed: %d", n)
			}
			if n := f.count(t, "journal_entries", "entry_type='page.grant_removed'"); n-before != wantRemoved {
				t.Errorf("removed events=%d want=%d", n-before, wantRemoved)
			}
			if f.published.Load() == 0 {
				t.Error("successful revoke did not notify")
			}
			f.chain(t)
		})
	}
}

func TestPageGrantCommitAuthorityLossIsForbidden(t *testing.T) {
	for _, method := range []string{"PUT", "DELETE"} {
		t.Run(method, func(t *testing.T) {
			f := newPageGrantAuditFixture(t)
			f.seed(t, "read")
			before := f.count(t, "journal_entries", "1=1")
			if _, err := f.h.db.Exec(`CREATE TRIGGER revoke_grant_issuer AFTER INSERT ON journal_entries WHEN NEW.entry_type IN ('page.grant_added','page.grant_removed') BEGIN UPDATE users SET suspended_at='2026-10-20' WHERE id=NEW.actor_id; END`); err != nil {
				t.Fatal(err)
			}
			rr := f.call(t, method, "read")
			if rr.Code != http.StatusForbidden {
				t.Errorf("commit authority loss status=%d want=403", rr.Code)
			}
			if f.count(t, "page_grants", "1=1") != 1 || f.count(t, "journal_entries", "1=1") != before || f.published.Load() != 0 {
				t.Error("authority failure did not roll back grant, audit and notification")
			}
			var active bool
			if err := f.h.db.QueryRow(`SELECT suspended_at IS NULL FROM users WHERE id=?`, f.actor).Scan(&active); err != nil || !active {
				t.Fatalf("trigger mutation did not roll back: active=%v err=%v", active, err)
			}
			f.chain(t)
		})
	}
}
