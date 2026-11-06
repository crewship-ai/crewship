package api

// Owner's decision on #5069: a grant ends when its issuer loses the authority
// needed to issue it. Restoring the role does not bring the old grant back, and
// a new grant is a new operation.
//
// Every cycle below runs with no read of the grant between its steps. A design
// that marks a grant when somebody looks at it would pass these tests only if a
// read happened to fall inside the window.

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

type grantRetirementRig struct {
	t                     *testing.T
	h                     *PageHandler
	spy                   *pagesJournalSpy
	wsID, ownerID, pageID string
}

func (r grantRetirementRig) exec(query string, args ...any) {
	r.t.Helper()
	if _, err := r.h.db.Exec(query, args...); err != nil {
		r.t.Fatalf("%s: %v", query, err)
	}
}

func (r grantRetirementRig) role(role string) {
	r.t.Helper()
	r.exec(`UPDATE workspace_members SET role=? WHERE workspace_id=? AND user_id='issuer'`, role, r.wsID)
}

func (r grantRetirementRig) otherWorkspace() {
	r.t.Helper()
	r.exec(`INSERT INTO workspaces(id,name,slug) VALUES('retire-other','Other','retire-other')`)
}

func (r grantRetirementRig) status(user string) int {
	r.t.Helper()
	req := pagesRequest(r.t, "GET", "/api/v1/pages/fleet-201", r.wsID, user, "MEMBER", "")
	req.SetPathValue("slug", "fleet-201")
	rr := httptest.NewRecorder()
	r.h.Get(rr, req)
	return rr.Code
}

func (r grantRetirementRig) canWrite() bool {
	r.t.Helper()
	page, err := r.h.loadPage(context.Background(), r.wsID, "fleet-201")
	if err != nil {
		r.t.Fatal(err)
	}
	return r.h.mayEditSpec(context.Background(), r.wsID, "grantee", "MEMBER", page)
}

func (r grantRetirementRig) issue(issuer, role, subject, level string) {
	r.t.Helper()
	rr := pagesGrantCall(r.t, r.h, "PUT", "/api/v1/pages/fleet-201/grants", r.wsID, issuer, role, "fleet-201",
		`{"subject_type":"user","subject":"`+subject+`@example.com","level":"`+level+`"}`)
	if rr.Code != http.StatusOK {
		r.t.Fatalf("issue %s to %s: %d %s", level, subject, rr.Code, rr.Body.String())
	}
}

func (r grantRetirementRig) journalled(entry journal.EntryType) int {
	n := 0
	for _, e := range r.spy.entries {
		if e.Type == entry {
			n++
		}
	}
	return n
}

// newGrantRetirementRig has "issuer" hold one write and one read grant it
// issued itself, on a Page it does not otherwise reach, and proves both work.
func newGrantRetirementRig(t *testing.T, owner, issuerRole string, arrange func(grantRetirementRig)) grantRetirementRig {
	t.Helper()
	h, spy, wsID, ownerID, pageID := pagesGrantFixture(t, owner)
	r := grantRetirementRig{t: t, h: h, spy: spy, wsID: wsID, ownerID: ownerID, pageID: pageID}
	pagesSeedUser(t, h, wsID, "issuer", "issuer@example.com", issuerRole)
	pagesSeedUser(t, h, wsID, "grantee", "grantee@example.com", "MEMBER")
	pagesSeedUser(t, h, wsID, "reader", "reader@example.com", "MEMBER")
	if arrange != nil {
		arrange(r)
	}
	r.issue("issuer", issuerRole, "grantee", "write")
	r.issue("issuer", issuerRole, "reader", "read")
	if !r.canWrite() || r.status("reader") != http.StatusOK {
		t.Fatalf("calibration: write=%v read=%d", r.canWrite(), r.status("reader"))
	}
	return r
}

func issuerInCrew(r grantRetirementRig) {
	r.exec(`INSERT INTO crew_members(id,crew_id,user_id) VALUES('cm-issuer','crew-lookout','issuer')`)
}

func issuerOwnsPage(r grantRetirementRig) {
	r.exec(`UPDATE pages SET owner_user_id='issuer',owner_crew_id=NULL WHERE id=?`, r.pageID)
}

func TestPageGrantStaysRetiredAcrossIssuerStandingCycles(t *testing.T) {
	for _, test := range []struct {
		name    string
		owner   string
		role    string
		arrange func(grantRetirementRig)
		cycle   func(grantRetirementRig)
	}{
		{name: "demoted then promoted", role: "ADMIN", cycle: func(r grantRetirementRig) {
			r.role("VIEWER")
			r.role("ADMIN")
		}},
		{name: "removed then restored with the same id", role: "ADMIN", cycle: func(r grantRetirementRig) {
			r.exec(`DELETE FROM workspace_members WHERE id='wm-issuer'`)
			r.exec(`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('wm-issuer',?,'issuer','ADMIN')`, r.wsID)
		}},
		{name: "removed then rejoined with a new id", role: "ADMIN", cycle: func(r grantRetirementRig) {
			r.exec(`DELETE FROM workspace_members WHERE id='wm-issuer'`)
			r.exec(`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('wm-issuer-2',?,'issuer','ADMIN')`, r.wsID)
		}},
		{name: "suspended then unsuspended", role: "ADMIN", cycle: func(r grantRetirementRig) {
			r.exec(`UPDATE users SET suspended_at='2026-11-06T10:00:00Z' WHERE id='issuer'`)
			r.exec(`UPDATE users SET suspended_at=NULL WHERE id='issuer'`)
		}},
		{name: "suspended with an empty value then unsuspended", role: "ADMIN", cycle: func(r grantRetirementRig) {
			r.exec(`UPDATE users SET suspended_at='' WHERE id='issuer'`)
			r.exec(`UPDATE users SET suspended_at=NULL WHERE id='issuer'`)
		}},
		{name: "restricted membership elsewhere added then ended", owner: "crew/lookout", role: "MEMBER", arrange: issuerInCrew, cycle: func(r grantRetirementRig) {
			r.otherWorkspace()
			r.exec(`INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('wm-else','retire-other','issuer','MEMBER','restricted')`)
			r.exec(`DELETE FROM workspace_members WHERE id='wm-else'`)
		}},
		{name: "membership elsewhere turned restricted then trusted", owner: "crew/lookout", role: "MEMBER", arrange: func(r grantRetirementRig) {
			issuerInCrew(r)
			r.otherWorkspace()
			r.exec(`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('wm-else','retire-other','issuer','MEMBER')`)
		}, cycle: func(r grantRetirementRig) {
			r.exec(`UPDATE workspace_members SET access_mode='restricted' WHERE id='wm-else'`)
			r.exec(`UPDATE workspace_members SET access_mode='trusted' WHERE id='wm-else'`)
		}},
		{name: "restricted here then trusted", owner: "crew/lookout", role: "MEMBER", arrange: issuerInCrew, cycle: func(r grantRetirementRig) {
			r.exec(`UPDATE workspace_members SET access_mode='restricted' WHERE id='wm-issuer'`)
			r.exec(`UPDATE workspace_members SET access_mode='trusted' WHERE id='wm-issuer'`)
		}},
		{name: "crew member out and back", owner: "crew/lookout", role: "MEMBER", arrange: issuerInCrew, cycle: func(r grantRetirementRig) {
			r.exec(`DELETE FROM crew_members WHERE id='cm-issuer'`)
			r.exec(`INSERT INTO crew_members(id,crew_id,user_id) VALUES('cm-issuer','crew-lookout','issuer')`)
		}},
		{name: "crew member moved to another crew and back", owner: "crew/lookout", role: "MEMBER", arrange: issuerInCrew, cycle: func(r grantRetirementRig) {
			r.exec(`UPDATE crew_members SET crew_id='crew-engine' WHERE id='cm-issuer'`)
			r.exec(`UPDATE crew_members SET crew_id='crew-lookout' WHERE id='cm-issuer'`)
		}},
		{name: "page moved to another crew and back", owner: "crew/lookout", role: "MEMBER", arrange: issuerInCrew, cycle: func(r grantRetirementRig) {
			r.exec(`UPDATE pages SET owner_crew_id='crew-engine' WHERE id=?`, r.pageID)
			r.exec(`UPDATE pages SET owner_crew_id='crew-lookout' WHERE id=?`, r.pageID)
		}},
		{name: "page ownership moved away and back", role: "MEMBER", arrange: issuerOwnsPage, cycle: func(r grantRetirementRig) {
			r.exec(`UPDATE pages SET owner_user_id=? WHERE id=?`, r.ownerID, r.pageID)
			r.exec(`UPDATE pages SET owner_user_id='issuer' WHERE id=?`, r.pageID)
		}},
		{name: "page handed to a crew and back", role: "MEMBER", arrange: issuerOwnsPage, cycle: func(r grantRetirementRig) {
			r.exec(`UPDATE pages SET owner_user_id=NULL,owner_crew_id='crew-lookout' WHERE id=?`, r.pageID)
			r.exec(`UPDATE pages SET owner_user_id='issuer',owner_crew_id=NULL WHERE id=?`, r.pageID)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := newGrantRetirementRig(t, test.owner, test.role, test.arrange)
			test.cycle(r)

			if r.canWrite() {
				t.Error("the old write grant authorized an edit after the issuer regained standing")
			}
			if got := r.status("reader"); got != http.StatusNotFound {
				t.Errorf("the old read grant opened the Page: status %d, want 404", got)
			}
			grants := pagesGrantList(t, r.h, r.wsID, r.ownerID, "OWNER", "fleet-201")
			if len(grants) != 2 {
				t.Fatalf("the list must keep retired grants as inactive metadata: %+v", grants)
			}
			for _, g := range grants {
				if live, _ := g["live"].(bool); live {
					t.Errorf("the list reports a retired grant as live: %+v", g)
				}
				if reason, _ := g["inert_reason"].(string); !strings.Contains(reason, "issued again") {
					t.Errorf("inert_reason = %q, want it to say the grant must be issued again", reason)
				}
			}
		})
	}
}

// Standing that only changes shape is not lost: the decision ends a grant when
// its issuer loses the authority to issue it, and nothing else.
func TestPageGrantSurvivesWhileIssuerKeepsStanding(t *testing.T) {
	for _, test := range []struct {
		name    string
		owner   string
		role    string
		arrange func(grantRetirementRig)
		change  func(grantRetirementRig)
	}{
		{name: "admin promoted to owner and back", role: "ADMIN", change: func(r grantRetirementRig) {
			r.role("OWNER")
			r.role("ADMIN")
		}},
		{name: "trusted membership elsewhere added and removed", role: "ADMIN", change: func(r grantRetirementRig) {
			r.otherWorkspace()
			r.exec(`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('wm-else','retire-other','issuer','OWNER')`)
			r.exec(`DELETE FROM workspace_members WHERE id='wm-else'`)
		}},
		{name: "capabilities change", role: "ADMIN", change: func(r grantRetirementRig) {
			r.exec(`UPDATE workspace_members SET capabilities='["x"]' WHERE id='wm-issuer'`)
		}},
		{name: "role written without a change", role: "ADMIN", change: func(r grantRetirementRig) {
			r.role("ADMIN")
		}},
		{name: "page owner demoted keeps the page", role: "ADMIN", arrange: issuerOwnsPage, change: func(r grantRetirementRig) {
			r.role("MEMBER")
		}},
		{name: "crew member demoted stays in the owning crew", owner: "crew/lookout", role: "ADMIN", arrange: issuerInCrew, change: func(r grantRetirementRig) {
			r.role("MEMBER")
		}},
		{name: "admin leaves the owning crew", owner: "crew/lookout", role: "ADMIN", arrange: issuerInCrew, change: func(r grantRetirementRig) {
			r.exec(`DELETE FROM crew_members WHERE id='cm-issuer'`)
		}},
		{name: "page changes owner under an admin issuer", role: "ADMIN", change: func(r grantRetirementRig) {
			r.exec(`UPDATE pages SET owner_user_id='grantee' WHERE id=?`, r.pageID)
			r.exec(`UPDATE pages SET owner_user_id=? WHERE id=?`, r.ownerID, r.pageID)
		}},
		{name: "page handed to a crew the issuer is in", role: "MEMBER", arrange: func(r grantRetirementRig) {
			issuerOwnsPage(r)
			issuerInCrew(r)
		}, change: func(r grantRetirementRig) {
			r.exec(`UPDATE pages SET owner_user_id=NULL,owner_crew_id='crew-lookout' WHERE id=?`, r.pageID)
		}},
		{name: "another member of the issuer's page crew leaves", owner: "crew/lookout", role: "MEMBER", arrange: func(r grantRetirementRig) {
			issuerInCrew(r)
			r.exec(`INSERT INTO crew_members(id,crew_id,user_id) VALUES('cm-reader','crew-lookout','reader')`)
		}, change: func(r grantRetirementRig) {
			r.exec(`DELETE FROM crew_members WHERE id='cm-reader'`)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := newGrantRetirementRig(t, test.owner, test.role, test.arrange)
			test.change(r)
			if !r.canWrite() {
				t.Error("a live grant stopped working although its issuer kept standing")
			}
			for _, g := range pagesGrantList(t, r.h, r.wsID, r.ownerID, "OWNER", "fleet-201") {
				if live, _ := g["live"].(bool); !live {
					t.Errorf("grant of an issuer with standing reported inactive: %+v", g)
				}
			}
		})
	}
}

// A new grant is a new operation: it works, it has its own journal entry, and it
// is exposed to the same rule again.
func TestPageGrantReissuedAfterRetirement(t *testing.T) {
	t.Run("by the issuer who regained standing", func(t *testing.T) {
		r := newGrantRetirementRig(t, "", "ADMIN", nil)
		r.role("VIEWER")
		r.role("ADMIN")
		before := r.journalled(journal.EntryPageGrantAdded)
		r.issue("issuer", "ADMIN", "grantee", "write")
		if !r.canWrite() {
			t.Fatal("the new grant does not work")
		}
		if got := r.journalled(journal.EntryPageGrantAdded); got != before+1 {
			t.Fatalf("journal entries = %d, want %d", got, before+1)
		}
		if got := r.status("reader"); got != http.StatusNotFound {
			t.Fatalf("reissuing one grant revived another: read status %d", got)
		}
		r.role("VIEWER")
		r.role("ADMIN")
		if r.canWrite() {
			t.Fatal("the new grant did not end with its issuer's next loss of standing")
		}
	})
	t.Run("by another human with standing", func(t *testing.T) {
		r := newGrantRetirementRig(t, "", "ADMIN", nil)
		r.role("VIEWER")
		r.issue(r.ownerID, "OWNER", "grantee", "write")
		if !r.canWrite() {
			t.Fatal("the new grant does not work")
		}
		r.role("ADMIN")
		r.role("VIEWER")
		if !r.canWrite() {
			t.Fatal("the new grant follows the old issuer's standing")
		}
	})
	t.Run("a retired grant can still be removed", func(t *testing.T) {
		r := newGrantRetirementRig(t, "", "ADMIN", nil)
		r.role("VIEWER")
		rr := pagesGrantCall(t, r.h, "DELETE", "/api/v1/pages/fleet-201/grants?subject_type=user&subject=grantee@example.com", r.wsID, r.ownerID, "OWNER", "fleet-201", "")
		if rr.Code != http.StatusOK {
			t.Fatalf("revoke: %d %s", rr.Code, rr.Body.String())
		}
		if n := pagesGrantRows(t, r.h, r.pageID); n != 1 {
			t.Fatalf("grant rows = %d, want the read grant alone", n)
		}
	})
}

// retired_at is a marker, so a new reader of page_grants that ignores it fails
// open. Every production statement either honours the marker or is on this list
// with the reason it does not authorize anything.
func TestEveryPageGrantReaderHonoursRetirement(t *testing.T) {
	notAuthorization := map[string]string{
		"pages_erase_identity.go": "erasure removes a person's rows, retired or not",
	}
	root := filepath.Join("..", "..")
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			if notAuthorization[filepath.Base(path)] != "" {
				return nil
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for i, line := range strings.Split(string(body), "\n") {
				if strings.Contains(line, "FROM page_grants") && !strings.Contains(line, "DELETE FROM") && !strings.Contains(line, "retired_at") {
					t.Errorf("%s:%d reads page_grants without retired_at", path, i+1)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
