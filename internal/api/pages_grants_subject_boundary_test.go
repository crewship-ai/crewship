package api

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
)

func TestPageGrantRejectsAmbiguousLiveSubjects(t *testing.T) {
	for _, kind := range []string{"crew", "agent", "user"} {
		for _, method := range []string{"PUT", "DELETE"} {
			t.Run(kind+"/"+method, func(t *testing.T) {
				f := newPageGrantAuditFixture(t)
				refs := []string{"amb-one", "amb-second"}
				ambiguous := "amb-reference"
				switch kind {
				case "crew":
					execOrFatal(t, f.h.db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('amb-reference',?,'One','amb-one'),('amb-second',?,'Two','amb-reference')`, f.ws, f.ws)
				case "agent":
					seedAgentRow(t, f.h.db, "amb-reference", f.ws, "crew-engine", "One", "amb-one", "AGENT")
					seedAgentRow(t, f.h.db, "amb-second", f.ws, "crew-engine", "Two", "amb-reference", "AGENT")
				case "user":
					execOrFatal(t, f.h.db, `INSERT INTO users(id,email) VALUES('amb-one','collision@example.test'),('amb-second','COLLISION@example.test')`)
					execOrFatal(t, f.h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('amb-member-one',?,'amb-one','MEMBER'),('amb-member-two',?,'amb-second','MEMBER')`, f.ws, f.ws)
					ambiguous = "collision@example.test"
				}
				for _, ref := range refs {
					body := fmt.Sprintf(`{"subject_type":%q,"subject":%q,"level":"read"}`, kind, ref)
					rr := pagesGrantCall(t, f.h, "PUT", "/grants", f.ws, f.actor, "OWNER", "fleet-201", body)
					if rr.Code != http.StatusOK {
						t.Fatalf("healthy unambiguous grant HTTP%d: %s", rr.Code, rr.Body.String())
					}
				}
				f.chain(t)
				snapshot := func() string {
					t.Helper()
					var v string
					if err := f.h.db.QueryRow(`SELECT json_group_array(json_array(subject_type,subject_id,level,panel_ids,granted_by_user_id,granted_at)) FROM (SELECT * FROM page_grants ORDER BY subject_type,subject_id,level)`).Scan(&v); err != nil {
						t.Fatal(err)
					}
					return v
				}
				before := snapshot()
				audits := f.count(t, "journal_entries", "1=1")
				f.published.Store(0)
				target := "/grants?subject_type=" + kind + "&subject=" + url.QueryEscape(ambiguous) + "&level=read"
				body := fmt.Sprintf(`{"subject_type":%q,"subject":%q,"level":"read"}`, kind, ambiguous)
				rr := pagesGrantCall(t, f.h, method, target, f.ws, f.actor, "OWNER", "fleet-201", body)
				if rr.Code != http.StatusBadRequest {
					t.Errorf("ambiguous live subject HTTP%d want400: %s", rr.Code, rr.Body.String())
				}
				if snapshot() != before || f.count(t, "journal_entries", "1=1") != audits || f.published.Load() != 0 {
					t.Error("ambiguous reference changed grant/audit/notification")
				}
				f.chain(t)
			})
		}
	}
}

func TestPageGrantPutAcknowledgesUnchangedOtherSubjects(t *testing.T) {
	f := newPageGrantAuditFixture(t)
	f.seed(t, "read")
	rr := pagesGrantCall(t, f.h, "PUT", "/grants", f.ws, f.actor, "OWNER", "fleet-201", `{"subject_type":"crew","subject":"lookout","level":"read"}`)
	if rr.Code != 200 {
		t.Fatalf("healthy unrelated grant HTTP%d", rr.Code)
	}
	f.chain(t)
	before := f.count(t, "journal_entries", "1=1")
	f.published.Store(0)
	execOrFatal(t, f.h.db, `CREATE TRIGGER change_other_subject AFTER INSERT ON journal_entries WHEN NEW.entry_type='page.grant_added' BEGIN UPDATE page_grants SET level='write' WHERE page_id='`+f.page+`' AND subject_type='crew' AND subject_id='crew-lookout' AND level='read'; END`)
	rr = f.call(t, "PUT", "read")
	if rr.Code != 500 {
		t.Errorf("foreign subject tamper HTTP%d want500", rr.Code)
	}
	if f.count(t, "page_grants", "subject_id='crew-lookout' AND level='read'") != 1 || f.count(t, "page_grants", "subject_id='crew-lookout' AND level='write'") != 0 || f.count(t, "journal_entries", "1=1") != before || f.published.Load() != 0 {
		t.Error("unrelated grant tamper escaped business/audit/notify rollback")
	}
	// The requested tuple and actor never changed; the historical native chain
	// remains valid even on the buggy implementation that accepts this tamper.
	if f.count(t, "page_grants", "subject_id='crew-engine' AND level='read'") != 1 {
		t.Error("requested grant changed")
	}
	f.chain(t)
}
