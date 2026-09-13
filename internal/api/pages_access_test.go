package api

// Pages — effective access (docs/prd/pages-collections-access-analysis
// §1/12–13, §4, §5/10, §8 A10; #2528).
//
// Two endpoints, one rendering of today's model. What is proved here:
//
//   1. The path set per subject is exact — owner, role, the owning crew, the
//      panel-owning crews in panel order, then the live grants by level — and
//      a subject with several standings gets all of them in that order.
//   2. The gate: the page endpoint answers the owner and the administrator and
//      refuses a grantee; the subject endpoint answers an administrator about
//      anyone and a member about themselves only.
//   3. A crew the caller cannot see is never named. The withheld fixture's
//      protected strings (pages_project_withheld_change_test.go) are the
//      standard, and the whole body is held to it.
//   4. The cost is constant: the same number of statements for 60 members as
//      for 5, and for 80 pages as for 3.
//   5. The cursor round-trips: walking the listing two rows at a time yields
//      exactly the whole listing once.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

// pagesAccessCall dispatches GET /api/v1/pages/{slug}/access.
func pagesAccessCall(t *testing.T, h *PageHandler, wsID, userID, role, slug, query string) *httptest.ResponseRecorder {
	t.Helper()
	target := "/api/v1/pages/" + slug + "/access"
	if query != "" {
		target += "?" + query
	}
	req := pagesRequest(t, "GET", target, wsID, userID, role, "")
	req.SetPathValue("slug", slug)
	rr := httptest.NewRecorder()
	h.PageAccess(rr, req)
	return rr
}

// pagesSubjectAccessCall dispatches GET /api/v1/pages/access.
func pagesSubjectAccessCall(t *testing.T, h *PageHandler, wsID, userID, role, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := pagesRequest(t, "GET", "/api/v1/pages/access?"+query, wsID, userID, role, "")
	rr := httptest.NewRecorder()
	h.SubjectAccess(rr, req)
	return rr
}

type pagesAccessSubject struct {
	SubjectType string   `json:"subject_type"`
	SubjectID   string   `json:"subject_id"`
	Label       string   `json:"label"`
	Paths       []string `json:"paths"`
}

type pagesAccessDoc struct {
	Page       string               `json:"page"`
	Subjects   []pagesAccessSubject `json:"subjects"`
	NextCursor string               `json:"next_cursor"`
}

func pagesAccessOf(t *testing.T, h *PageHandler, wsID, userID, role, slug string) pagesAccessDoc {
	t.Helper()
	rr := pagesAccessCall(t, h, wsID, userID, role, slug, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("access %s as %s: status = %d, body: %s", slug, userID, rr.Code, rr.Body.String())
	}
	var doc pagesAccessDoc
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("access response is not JSON: %v — %s", err, rr.Body.String())
	}
	return doc
}

// pagesAccessKey renders a subject row as "kind/label=[paths]" so a whole
// listing can be compared in one DeepEqual and a failure prints in full.
func pagesAccessKey(s pagesAccessSubject) string {
	label := s.Label
	if label == "" {
		label = "(" + s.SubjectID + ")"
	}
	return s.SubjectType + "/" + label + "=" + strings.Join(s.Paths, ",")
}

func pagesAccessKeys(doc pagesAccessDoc) []string {
	out := make([]string, 0, len(doc.Subjects))
	for _, s := range doc.Subjects {
		out = append(out, pagesAccessKey(s))
	}
	return out
}

// pagesAccessFixture is the reach test's workspace with an agent and a crew
// grant added, so every arm has a subject:
//
//	fleet-201  owned by alice; panels by crew/lookout then crew/engine
//	           grants: dave read; frank read + write; crew/engine read;
//	           agent watcher (crew/engine) produce
//	ops-board  owned by crew/engine; one panel by crew/lookout
//
// bob is in engine, carol in lookout, frank in both, erin is an ADMIN, and
// the fixture's OWNER user issues every grant.
func pagesAccessFixture(t *testing.T) (*PageHandler, string, string) {
	t.Helper()
	h, _, _, wsID, ownerID := newPagesFixture(t)
	if _, err := h.db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-engine', ?, 'Engine', 'engine')`, wsID); err != nil {
		t.Fatalf("insert crew: %v", err)
	}
	for _, u := range []struct{ id, role string }{
		{"alice", "MEMBER"}, {"bob", "MEMBER"}, {"carol", "MEMBER"},
		{"dave", "MEMBER"}, {"erin", "ADMIN"}, {"frank", "MEMBER"},
	} {
		pagesSeedUser(t, h, wsID, u.id, u.id+"@example.com", u.role)
	}
	pagesJoinCrew(t, h, "bob", "crew-engine")
	pagesJoinCrew(t, h, "carol", "crew-lookout")
	pagesJoinCrew(t, h, "frank", "crew-engine")
	pagesJoinCrew(t, h, "frank", "crew-lookout")
	pagesSeedAgent(t, h, wsID, "agent-watcher", "watcher", "crew-engine")

	fleet := pagesCreateWith(t, h, wsID, ownerID, pagesReachBody("fleet-201", "lookout", "engine"))
	pagesSetOwner(t, h, fleet, "alice", "")
	ops := pagesCreateWith(t, h, wsID, ownerID, pagesReachBody("ops-board", "lookout"))
	pagesSetOwner(t, h, ops, "", "crew-engine")

	pagesGrant(t, h, wsID, ownerID, "fleet-201", `{"subject_type":"user","subject":"dave","level":"read"}`)
	pagesGrant(t, h, wsID, ownerID, "fleet-201", `{"subject_type":"user","subject":"frank","level":"write"}`)
	pagesGrant(t, h, wsID, ownerID, "fleet-201", `{"subject_type":"user","subject":"frank","level":"read"}`)
	pagesGrant(t, h, wsID, ownerID, "fleet-201", `{"subject_type":"crew","subject":"engine","level":"read"}`)
	pagesGrant(t, h, wsID, ownerID, "fleet-201", `{"subject_type":"agent","subject":"watcher","level":"produce","panels":["p1"]}`)
	return h, wsID, ownerID
}

// ── 1. Exact path sets ─────────────────────────────────────────────────────

func TestPageAccess_ListsEverySubjectWithItsExactPaths(t *testing.T) {
	h, wsID, ownerID := pagesAccessFixture(t)

	t.Run("an administrator sees every subject and every path, in the fixed order", func(t *testing.T) {
		doc := pagesAccessOf(t, h, wsID, ownerID, "OWNER", "fleet-201")
		want := []string{
			"user/alice@example.com=owner",
			"user/bob@example.com=panel_crew:engine,grant:page:read",
			"user/carol@example.com=panel_crew:lookout",
			"user/dave@example.com=grant:page:read",
			"user/erin@example.com=role",
			"user/frank@example.com=panel_crew:lookout,panel_crew:engine,grant:page:read,grant:page:write",
			"user/test@example.com=role",
			"crew/engine=panel_crew:engine,grant:page:read",
			"crew/lookout=panel_crew:lookout",
			"agent/watcher=grant:page:produce",
		}
		if got := pagesAccessKeys(doc); !reflect.DeepEqual(got, want) {
			t.Errorf("fleet-201 access =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
		}
		if doc.Page != "fleet-201" || doc.NextCursor != "" {
			t.Errorf("envelope = page %q, next_cursor %q; want fleet-201 and no cursor", doc.Page, doc.NextCursor)
		}
	})

	t.Run("the owning crew's page names the crew as owner and its members through the crew", func(t *testing.T) {
		doc := pagesAccessOf(t, h, wsID, ownerID, "OWNER", "ops-board")
		want := []string{
			"user/bob@example.com=crew:engine",
			"user/carol@example.com=panel_crew:lookout",
			"user/erin@example.com=role",
			"user/frank@example.com=crew:engine,panel_crew:lookout",
			"user/test@example.com=role",
			"crew/engine=owner",
			"crew/lookout=panel_crew:lookout",
		}
		if got := pagesAccessKeys(doc); !reflect.DeepEqual(got, want) {
			t.Errorf("ops-board access =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
		}
	})

	t.Run("a page owner who is not in a panel's crew is shown that crew withheld, on one anonymous row", func(t *testing.T) {
		// alice owns fleet-201 and belongs to no crew: lookout and engine are
		// both hidden from her. carol's, bob's and frank's crew paths, and the
		// two crew rows, collapse into the single withheld row; what remains
		// on the named rows is what alice may know — ownership, roles, and the
		// grants she administers.
		doc := pagesAccessOf(t, h, wsID, "alice", "MEMBER", "fleet-201")
		want := []string{
			"user/alice@example.com=owner",
			"user/bob@example.com=grant:page:read",
			"user/dave@example.com=grant:page:read",
			"user/erin@example.com=role",
			"user/frank@example.com=grant:page:read,grant:page:write",
			"user/test@example.com=role",
			"crew/engine=grant:page:read",
			"agent/watcher=grant:page:produce",
			"crew/(withheld)=panel_crew:withheld",
		}
		if got := pagesAccessKeys(doc); !reflect.DeepEqual(got, want) {
			t.Errorf("fleet-201 access as alice =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
		}
	})

	t.Run("a member of the owning crew reads the crew's page and sees the other crew withheld", func(t *testing.T) {
		doc := pagesAccessOf(t, h, wsID, "bob", "MEMBER", "ops-board")
		want := []string{
			"user/bob@example.com=crew:engine",
			"user/erin@example.com=role",
			"user/frank@example.com=crew:engine",
			"user/test@example.com=role",
			"crew/engine=owner",
			"crew/(withheld)=panel_crew:withheld",
		}
		if got := pagesAccessKeys(doc); !reflect.DeepEqual(got, want) {
			t.Errorf("ops-board access as bob =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
		}
	})
}

// ── 2. The gate ────────────────────────────────────────────────────────────

func TestPageAccess_RefusesEveryoneButTheOwnerAndAdmins(t *testing.T) {
	h, wsID, _ := pagesAccessFixture(t)

	for _, tc := range []struct{ name, user, role, slug string }{
		{"a plain grantee", "dave", "MEMBER", "fleet-201"},
		{"a member of a panel's crew", "carol", "MEMBER", "fleet-201"},
		{"a write grantee", "frank", "MEMBER", "fleet-201"},
		{"the owning crew of another page", "bob", "MEMBER", "fleet-201"},
	} {
		t.Run(tc.name+" is refused", func(t *testing.T) {
			rr := pagesAccessCall(t, h, wsID, tc.user, tc.role, tc.slug, "")
			if rr.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403: %s", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), "page owner or a workspace admin") {
				t.Errorf("the refusal does not say who may read: %s", rr.Body.String())
			}
			// A refusal carries nothing of the answer.
			for _, leak := range []string{"subjects", "panel_crew", "grant:page"} {
				if strings.Contains(rr.Body.String(), leak) {
					t.Errorf("the refusal carries %q: %s", leak, rr.Body.String())
				}
			}
		})
	}

	t.Run("an unknown page is 404 before any gate", func(t *testing.T) {
		rr := pagesAccessCall(t, h, wsID, "dave", "MEMBER", "nope", "")
		if rr.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("a bad limit and a foreign cursor are 400", func(t *testing.T) {
		for _, q := range []string{"limit=0", "limit=x", "cursor=not-base64!", "cursor=" + url.QueryEscape("dGhpcyBpcyBub3Qgb3Vycw")} {
			rr := pagesAccessCall(t, h, wsID, "alice", "MEMBER", "fleet-201", q)
			if rr.Code != http.StatusBadRequest {
				t.Errorf("?%s: status = %d, want 400: %s", q, rr.Code, rr.Body.String())
			}
		}
	})
}

// ── 3. A crew the caller cannot see is never named ─────────────────────────

func TestPageAccess_WithheldCrewIsNeverNamed(t *testing.T) {
	h, ws, admin, publisher := withheldFixture(t)

	rr := pagesAccessCall(t, h, ws, publisher, "MEMBER", "health", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("the page owner was refused their own page's access: %d %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	withheldAssertBodyNeutral(t, "the publisher's access listing", body)
	if !strings.Contains(body, "panel_crew:withheld") {
		t.Errorf("the listing does not say a withheld crew reaches the page: %s", body)
	}
	if !strings.Contains(body, "panel_crew:engine") {
		t.Errorf("the listing does not name the publisher's own crew's panel: %s", body)
	}
	var doc pagesAccessDoc
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	for _, s := range doc.Subjects {
		if s.SubjectID == pageAccessWithheld && s.Label != "" {
			t.Errorf("the withheld row carries a label %q", s.Label)
		}
		if s.SubjectID != pageAccessWithheld {
			for _, p := range s.Paths {
				if strings.HasSuffix(p, ":"+pageAccessWithheld) {
					t.Errorf("a named row (%s) carries a withheld path %q; that says which crew owns the hidden panel", pagesAccessKey(s), p)
				}
			}
		}
	}

	// The administrator's view of the same page names the crew, which is
	// what proves the publisher's view withheld something rather than the
	// crew simply reaching nothing.
	adminBody := pagesAccessCall(t, h, ws, admin, "OWNER", "health", "").Body.String()
	if !strings.Contains(adminBody, "panel_crew:lookout") {
		t.Errorf("the administrator's listing does not name crew/lookout: %s", adminBody)
	}
	if strings.Contains(adminBody, pageAccessWithheld) {
		t.Errorf("the administrator's listing withholds something: %s", adminBody)
	}
}

// ── 4. The subject endpoint ────────────────────────────────────────────────

type pagesSubjectAccessDoc struct {
	Subject struct {
		SubjectType string `json:"subject_type"`
		SubjectID   string `json:"subject_id"`
		Label       string `json:"label"`
	} `json:"subject"`
	Pages []struct {
		Slug  string   `json:"slug"`
		Name  string   `json:"name"`
		Paths []string `json:"paths"`
	} `json:"pages"`
	NextCursor string `json:"next_cursor"`
}

func pagesSubjectAccessOf(t *testing.T, h *PageHandler, wsID, userID, role, query string) pagesSubjectAccessDoc {
	t.Helper()
	rr := pagesSubjectAccessCall(t, h, wsID, userID, role, query)
	if rr.Code != http.StatusOK {
		t.Fatalf("subject access ?%s as %s: status = %d, body: %s", query, userID, rr.Code, rr.Body.String())
	}
	var doc pagesSubjectAccessDoc
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("subject access response is not JSON: %v — %s", err, rr.Body.String())
	}
	return doc
}

func pagesSubjectAccessKeys(doc pagesSubjectAccessDoc) []string {
	out := make([]string, 0, len(doc.Pages))
	for _, p := range doc.Pages {
		out = append(out, p.Slug+"="+strings.Join(p.Paths, ","))
	}
	return out
}

func TestPageSubjectAccess_AnswersAdminsAboutAnyoneAndMembersAboutThemselves(t *testing.T) {
	h, wsID, _ := pagesAccessFixture(t)

	cases := []struct {
		name  string
		user  string
		role  string
		query string
		want  []string
		label string
	}{
		{"a member about themselves, by id", "dave", "MEMBER", "subject=user:dave",
			[]string{"fleet-201=grant:page:read"}, "dave@example.com"},
		{"a member about themselves, by email, in the two-parameter spelling", "frank", "MEMBER",
			"subject_type=user&subject=frank%40example.com",
			[]string{"fleet-201=panel_crew:lookout,panel_crew:engine,grant:page:read,grant:page:write", "ops-board=crew:engine,panel_crew:lookout"},
			"frank@example.com"},
		{"an administrator about a user", "erin", "ADMIN", "subject=user:alice",
			[]string{"fleet-201=owner"}, "alice@example.com"},
		{"an administrator about themselves", "erin", "ADMIN", "subject=user:erin",
			[]string{"fleet-201=role", "ops-board=role"}, "erin@example.com"},
		{"an administrator about a crew", "erin", "ADMIN", "subject=crew:engine",
			[]string{"fleet-201=panel_crew:engine,grant:page:read", "ops-board=owner"}, "engine"},
		{"an administrator about an agent", "erin", "ADMIN", "subject=agent:watcher",
			[]string{"fleet-201=grant:page:produce"}, "watcher"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := pagesSubjectAccessOf(t, h, wsID, tc.user, tc.role, tc.query)
			if got := pagesSubjectAccessKeys(doc); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("pages = %v, want %v", got, tc.want)
			}
			if doc.Subject.Label != tc.label {
				t.Errorf("subject label = %q, want %q", doc.Subject.Label, tc.label)
			}
		})
	}

	for _, tc := range []struct{ name, user, role, query string }{
		{"a member about another user", "dave", "MEMBER", "subject=user:alice"},
		{"a member about a crew they belong to", "bob", "MEMBER", "subject=crew:engine"},
		{"a member about an agent", "bob", "MEMBER", "subject=agent:watcher"},
		{"a member about an email that is nobody's", "dave", "MEMBER", "subject=user:ghost%40example.com"},
	} {
		t.Run(tc.name+" is refused", func(t *testing.T) {
			rr := pagesSubjectAccessCall(t, h, wsID, tc.user, tc.role, tc.query)
			if rr.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403: %s", rr.Code, rr.Body.String())
			}
			if strings.Contains(rr.Body.String(), `"pages"`) {
				t.Errorf("the refusal carries an answer: %s", rr.Body.String())
			}
		})
	}

	t.Run("an administrator asking about nobody gets 404", func(t *testing.T) {
		rr := pagesSubjectAccessCall(t, h, wsID, "erin", "ADMIN", "subject=user:ghost%40example.com")
		if rr.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404: %s", rr.Code, rr.Body.String())
		}
	})

	t.Run("a subject without a kind is 400", func(t *testing.T) {
		for _, q := range []string{"", "subject=alice", "subject=robot:alice", "subject_type=user"} {
			rr := pagesSubjectAccessCall(t, h, wsID, "erin", "ADMIN", q)
			if rr.Code != http.StatusBadRequest {
				t.Errorf("?%s: status = %d, want 400: %s", q, rr.Code, rr.Body.String())
			}
		}
	})
}

// ── 5. Constant cost ───────────────────────────────────────────────────────

func TestPageAccess_QueryCountDoesNotGrow(t *testing.T) {
	t.Run("with members on the page endpoint", func(t *testing.T) {
		countFor := func(t *testing.T, members int) int {
			t.Helper()
			h, counter, wsID, ownerID := newCountingPagesFixture(t)
			pagesCreateWith(t, h, wsID, ownerID, pagesReachBody("fleet-201", "lookout", "engine"))
			for i := 0; i < members; i++ {
				id := fmt.Sprintf("m%02d", i)
				pagesSeedUser(t, h, wsID, id, id+"@example.com", "MEMBER")
				pagesJoinCrew(t, h, id, "crew-engine")
				pagesGrant(t, h, wsID, ownerID, "fleet-201", `{"subject_type":"user","subject":"`+id+`","level":"read"}`)
			}
			counter.reset()
			doc := pagesAccessOf(t, h, wsID, ownerID, "OWNER", "fleet-201")
			n := counter.count()
			// members + the owner + the two crews
			if len(doc.Subjects) != members+3 {
				t.Fatalf("listed %d subjects, want %d", len(doc.Subjects), members+3)
			}
			return n
		}
		small := countFor(t, 5)
		large := countFor(t, 60)
		t.Logf("PageAccess ran %d statements for 5 members and %d for 60", small, large)
		if small == 0 {
			t.Fatal("the counter saw no statements at all — the handler is not running through the counted handle")
		}
		if large != small {
			t.Errorf("PageAccess ran %d statements for 60 members and %d for 5: statements are being spent per member", large, small)
		}
	})

	t.Run("with pages on the subject endpoint", func(t *testing.T) {
		countFor := func(t *testing.T, pageCount int) int {
			t.Helper()
			h, counter, wsID, ownerID := newCountingPagesFixture(t)
			pagesSeedUser(t, h, wsID, "dave", "dave@example.com", "MEMBER")
			for i := 0; i < pageCount; i++ {
				slug := fmt.Sprintf("page-%02d", i)
				pagesCreateWith(t, h, wsID, ownerID, pagesReachBody(slug, "lookout", "engine"))
				pagesGrant(t, h, wsID, ownerID, slug, `{"subject_type":"user","subject":"dave","level":"read"}`)
			}
			counter.reset()
			doc := pagesSubjectAccessOf(t, h, wsID, "dave", "MEMBER", "subject=user:dave")
			n := counter.count()
			if len(doc.Pages) != pageCount {
				t.Fatalf("listed %d pages, want %d", len(doc.Pages), pageCount)
			}
			return n
		}
		small := countFor(t, 3)
		large := countFor(t, 80)
		t.Logf("SubjectAccess ran %d statements for 3 pages and %d for 80", small, large)
		if small == 0 {
			t.Fatal("the counter saw no statements at all — the handler is not running through the counted handle")
		}
		if large != small {
			t.Errorf("SubjectAccess ran %d statements for 80 pages and %d for 3: statements are being spent per page", large, small)
		}
	})
}

// ── 6. The cursor round-trips ──────────────────────────────────────────────

func TestPageAccess_CursorRoundTrips(t *testing.T) {
	h, wsID, ownerID := pagesAccessFixture(t)

	t.Run("on the page endpoint", func(t *testing.T) {
		whole := pagesAccessKeys(pagesAccessOf(t, h, wsID, ownerID, "OWNER", "fleet-201"))
		var walked []string
		cursor := ""
		for pages := 0; ; pages++ {
			q := "limit=3"
			if cursor != "" {
				q += "&cursor=" + url.QueryEscape(cursor)
			}
			rr := pagesAccessCall(t, h, wsID, ownerID, "OWNER", "fleet-201", q)
			if rr.Code != http.StatusOK {
				t.Fatalf("page %d: status = %d: %s", pages, rr.Code, rr.Body.String())
			}
			var doc pagesAccessDoc
			if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
				t.Fatal(err)
			}
			if len(doc.Subjects) > 3 {
				t.Fatalf("page %d carries %d rows, limit was 3", pages, len(doc.Subjects))
			}
			walked = append(walked, pagesAccessKeys(doc)...)
			if doc.NextCursor == "" {
				break
			}
			cursor = doc.NextCursor
			if pages > 10 {
				t.Fatal("the cursor never ran out")
			}
		}
		if !reflect.DeepEqual(walked, whole) {
			t.Errorf("walked =\n  %s\nwhole =\n  %s", strings.Join(walked, "\n  "), strings.Join(whole, "\n  "))
		}
	})

	t.Run("on the subject endpoint", func(t *testing.T) {
		whole := pagesSubjectAccessKeys(pagesSubjectAccessOf(t, h, wsID, "erin", "ADMIN", "subject=user:erin"))
		if len(whole) != 2 {
			t.Fatalf("erin reaches %d pages, want 2 for a two-page walk", len(whole))
		}
		first := pagesSubjectAccessOf(t, h, wsID, "erin", "ADMIN", "subject=user:erin&limit=1")
		if len(first.Pages) != 1 || first.NextCursor == "" {
			t.Fatalf("first page = %v, cursor %q; want one row and a cursor", pagesSubjectAccessKeys(first), first.NextCursor)
		}
		second := pagesSubjectAccessOf(t, h, wsID, "erin", "ADMIN", "subject=user:erin&limit=1&cursor="+url.QueryEscape(first.NextCursor))
		if len(second.Pages) != 1 || second.NextCursor != "" {
			t.Fatalf("second page = %v, cursor %q; want one row and no cursor", pagesSubjectAccessKeys(second), second.NextCursor)
		}
		walked := append(pagesSubjectAccessKeys(first), pagesSubjectAccessKeys(second)...)
		if !reflect.DeepEqual(walked, whole) {
			t.Errorf("walked = %v, whole = %v", walked, whole)
		}
	})
}
