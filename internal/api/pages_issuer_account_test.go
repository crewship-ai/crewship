package api

import (
	"net/http"
	"strings"
	"testing"
)

func TestPageGrantIssuerAccountCeiling(t *testing.T) {
	for _, state := range []string{"suspended", "empty-suspension", "guest-elsewhere"} {
		t.Run(state, func(t *testing.T) {
			h, _, workspace, owner, page := pagesGrantFixture(t, "")
			pagesSeedUser(t, h, workspace, "account-issuer", "issuer@example.test", "MEMBER")
			pagesSeedUser(t, h, workspace, "account-recipient", "recipient@example.test", "MEMBER")
			// A normal member may delegate its own page, without being ADMIN.
			execOrFatal(t, h.db, `UPDATE pages SET owner_user_id='account-issuer',owner_crew_id=NULL WHERE id=?`, page)
			granted := pagesGrantCall(t, h, "PUT", "/api/v1/pages/fleet-201/grants", workspace, "account-issuer", "MEMBER", "fleet-201", `{"subject_type":"user","subject":"recipient@example.test","level":"produce","panels":["sluzby"]}`)
			if granted.Code != 200 {
				t.Fatalf("grant %d %s", granted.Code, granted.Body.String())
			}
			if before := pagesPush(t, h, workspace, "account-recipient", "MEMBER", "fleet-201", "sluzby", pagesStatusPayload); before.Code != 200 {
				t.Fatalf("initial share %d %s", before.Code, before.Body.String())
			}
			switch state {
			case "suspended":
				execOrFatal(t, h.db, `UPDATE users SET suspended_at='2026-10-15T00:00:00Z' WHERE id='account-issuer'`)
			case "empty-suspension":
				execOrFatal(t, h.db, `UPDATE users SET suspended_at='' WHERE id='account-issuer'`)
			case "guest-elsewhere":
				execOrFatal(t, h.db, `INSERT INTO workspaces(id,name,slug) VALUES('issuer-other-workspace','Other','issuer-other-workspace')`)
				execOrFatal(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('issuer-other-membership','issuer-other-workspace','account-issuer','MEMBER','restricted')`)
			}

			if rows := pagesGrantRows(t, h, page); rows != 1 {
				t.Fatalf("grant metadata removed: %d", rows)
			}
			grants := pagesGrantList(t, h, workspace, owner, "OWNER", "fleet-201")
			if len(grants) != 1 || grants[0]["live"] != false {
				t.Fatalf("inert share hidden or live: %+v", grants)
			}
			after := pagesPush(t, h, workspace, "account-recipient", "MEMBER", "fleet-201", "sluzby", pagesStatusPayload)
			if after.Code != http.StatusNotFound {
				t.Fatalf("inert account still delegated Page authority: %d %s", after.Code, after.Body.String())
			}

			if reason, _ := grants[0]["inert_reason"].(string); !strings.Contains(reason, "account") {
				t.Fatalf("missing account reason: %+v", grants)
			}
		})
	}
}
