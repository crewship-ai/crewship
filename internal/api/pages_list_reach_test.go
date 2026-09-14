package api

// Pages — the list's `reach` field (docs/prd/pages-collections-access-analysis
// §7 P0b, #2524).
//
// Every row the index returns names the paths by which the CALLER reaches that
// page, and nothing else: it is the caller's own standing rendered back to
// them, never a view of the ACL. Two things are proved here, and they are
// different things:
//
//   1. The set is exact per caller — an owner sees `owner`, a member of a
//      panel's crew sees that crew, a grantee sees `grant`, an administrator
//      sees `role` — and a page the caller cannot reach is absent, not listed
//      with an empty set.
//   2. Computing it costs nothing per page: the listing runs the same number
//      of statements for 80 pages as for 3, because reach is derived from
//      what the index already loads in bulk. A per-page lookup would turn the
//      permission check into the slow part of the listing, and a permission
//      check that is the slow part is one somebody deletes later.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// pagesReachBody is a page whose panels are owned by the named crews, in
// order. Ordering matters: `panel_crew:` entries follow the spec's panel order.
func pagesReachBody(slug string, owners ...string) string {
	panels := make([]string, 0, len(owners))
	for i, owner := range owners {
		panels = append(panels, fmt.Sprintf(
			`{"id": "p%d", "schema": "status.v1", "title": "Panel %d", "owner": "crew/%s", "producer": "script/watch.sh", "sla_seconds": 30, "span": 4}`,
			i, i, owner))
	}
	return `{"slug": "` + slug + `", "name": "Reach ` + slug + `", "panels": [` + strings.Join(panels, ",") + `]}`
}

func pagesCreateWith(t *testing.T, h *PageHandler, wsID, userID, body string) string {
	t.Helper()
	req := pagesRequest(t, "POST", "/api/v1/pages", wsID, userID, "OWNER", body)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create page: status = %d, want 201, body: %s", rr.Code, rr.Body.String())
	}
	var doc struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &doc); err != nil {
		t.Fatalf("create response is not JSON: %v", err)
	}
	return doc.ID
}

// pagesSetOwner rewrites ownership directly. The handler's transfer endpoint
// has its own rules and its own tests; this file is about who reaches what
// once ownership is whatever it is.
func pagesSetOwner(t *testing.T, h *PageHandler, pageID, ownerUser, ownerCrew string) {
	t.Helper()
	var userArg, crewArg any
	if ownerUser != "" {
		userArg = ownerUser
	}
	if ownerCrew != "" {
		crewArg = ownerCrew
	}
	if _, err := h.db.Exec(`UPDATE pages SET owner_user_id = ?, owner_crew_id = ? WHERE id = ?`, userArg, crewArg, pageID); err != nil {
		t.Fatalf("set owner of %s: %v", pageID, err)
	}
}

func pagesJoinCrew(t *testing.T, h *PageHandler, userID, crewID string) {
	t.Helper()
	if _, err := h.db.Exec(`INSERT INTO crew_members (id, crew_id, user_id, role) VALUES (?, ?, ?, 'MEMBER')`,
		"cm-"+userID+"-"+crewID, crewID, userID); err != nil {
		t.Fatalf("add %s to %s: %v", userID, crewID, err)
	}
}

// pagesListRows lists as the given caller and indexes the rows by slug.
func pagesListRows(t *testing.T, h *PageHandler, wsID, userID, role string) map[string]map[string]any {
	t.Helper()
	req := pagesRequest(t, "GET", "/api/v1/pages", wsID, userID, role, "")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list as %s: status = %d, body: %s", userID, rr.Code, rr.Body.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatalf("list is not a JSON array: %v (%s)", err, rr.Body.String())
	}
	out := map[string]map[string]any{}
	for _, row := range rows {
		slug, _ := row["slug"].(string)
		out[slug] = row
	}
	return out
}

func pagesReachOf(t *testing.T, row map[string]any) []string {
	t.Helper()
	raw, present := row["reach"]
	if !present {
		t.Fatalf("row %v carries no `reach`; the field is sent on every row, never omitted", row["slug"])
	}
	list, ok := raw.([]any)
	if !ok {
		t.Fatalf("reach is %T, want an array: %s", raw, mustPagesJSON(t, row))
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		s, _ := item.(string)
		out = append(out, s)
	}
	return out
}

// TestPagesList_ReachNamesOnlyTheCallersPaths — one caller per arm, three
// pages, and for each caller the exact reach on every page they can see and
// the absence of every page they cannot.
//
// The pages:
//
//	fleet-201  owned by alice; panels by crew/lookout then crew/engine
//	ops-board  owned by crew/engine; one panel by crew/lookout
//	mine       owned by the fixture's OWNER user; one panel by crew/lookout
//
// dave and frank hold a `read` grant on fleet-201 and nothing else.
func TestPagesList_ReachNamesOnlyTheCallersPaths(t *testing.T) {
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

	fleet := pagesCreateWith(t, h, wsID, ownerID, pagesReachBody("fleet-201", "lookout", "engine"))
	pagesSetOwner(t, h, fleet, "alice", "")
	ops := pagesCreateWith(t, h, wsID, ownerID, pagesReachBody("ops-board", "lookout"))
	pagesSetOwner(t, h, ops, "", "crew-engine")
	pagesCreateWith(t, h, wsID, ownerID, pagesReachBody("mine", "lookout"))

	for _, grantee := range []string{"dave", "frank"} {
		pagesGrant(t, h, wsID, ownerID, "fleet-201", `{"subject_type":"user","subject":"`+grantee+`","level":"read"}`)
	}

	cases := []struct {
		name string
		user string
		role string
		// want maps slug → reach; a slug absent here must be absent from the
		// listing.
		want map[string][]string
	}{
		{
			name: "the owner of a page reaches it as owner and nothing else",
			user: "alice", role: "MEMBER",
			want: map[string][]string{"fleet-201": {"owner"}},
		},
		{
			name: "a member of the owning crew reaches through the crew, and through a panel elsewhere",
			user: "bob", role: "MEMBER",
			want: map[string][]string{"ops-board": {"crew:engine"}, "fleet-201": {"panel_crew:engine"}},
		},
		{
			name: "a member of a crew owning one panel reaches through that panel alone",
			user: "carol", role: "MEMBER",
			want: map[string][]string{
				"fleet-201": {"panel_crew:lookout"},
				"ops-board": {"panel_crew:lookout"},
				"mine":      {"panel_crew:lookout"},
			},
		},
		{
			name: "a plain grantee reaches through the grant alone",
			user: "dave", role: "MEMBER",
			want: map[string][]string{"fleet-201": {"grant"}},
		},
		{
			name: "an administrator reaches every page through the role, and the role is not padded with memberships they do not hold",
			user: "erin", role: "ADMIN",
			want: map[string][]string{"fleet-201": {"role"}, "ops-board": {"role"}, "mine": {"role"}},
		},
		{
			name: "several paths are listed in the fixed order: crew, panel crews in spec order, grant",
			user: "frank", role: "MEMBER",
			want: map[string][]string{
				"fleet-201": {"panel_crew:lookout", "panel_crew:engine", "grant"},
				"ops-board": {"crew:engine", "panel_crew:lookout"},
				"mine":      {"panel_crew:lookout"},
			},
		},
		{
			name: "owner precedes role for the workspace OWNER on their own page",
			user: ownerID, role: "OWNER",
			want: map[string][]string{"mine": {"owner", "role"}, "fleet-201": {"role"}, "ops-board": {"role"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows := pagesListRows(t, h, wsID, tc.user, tc.role)
			for slug, want := range tc.want {
				row, ok := rows[slug]
				if !ok {
					t.Errorf("%s: page %q is missing from the listing", tc.user, slug)
					continue
				}
				if got := pagesReachOf(t, row); !reflect.DeepEqual(got, want) {
					t.Errorf("%s on %q: reach = %v, want %v", tc.user, slug, got, want)
				}
			}
			for slug, row := range rows {
				if _, expected := tc.want[slug]; !expected {
					t.Errorf("%s: page %q is listed with reach %v, but this caller has no path to it", tc.user, slug, row["reach"])
				}
				// The row is about the caller and nobody else: no subject list,
				// no grant table, no issuer.
				for key := range row {
					if strings.Contains(key, "grant") || strings.Contains(key, "subject") {
						t.Errorf("%s: row %q carries %q — the index must not describe the ACL", tc.user, slug, key)
					}
				}
			}
		})
	}
}

// TestPagesList_QueryCountDoesNotGrowWithPages — the cost half of the contract.
//
// Each page has two panels owned by different crews and one grant, so every
// arm of reach has something to look at, and the caller is the grantee so the
// grant arm is the one deciding. The count is taken for 3 pages and for 80,
// and they must be EQUAL: a per-page statement of any kind — a crew slug, a
// grant check, a panel scan — would show up as 77 extra.
func TestPagesList_QueryCountDoesNotGrowWithPages(t *testing.T) {
	countFor := func(t *testing.T, pageCount int) int {
		t.Helper()
		h, counter, wsID, ownerID := newCountingPagesFixture(t)
		pagesSeedUser(t, h, wsID, "dave", "dave@example.com", "MEMBER")
		for i := 0; i < pageCount; i++ {
			slug := fmt.Sprintf("page-%02d", i)
			id := pagesCreateWith(t, h, wsID, ownerID, pagesReachBody(slug, "lookout", "engine"))
			pagesSetOwner(t, h, id, "", "crew-engine")
			pagesGrant(t, h, wsID, ownerID, slug, `{"subject_type":"user","subject":"dave","level":"read"}`)
		}
		counter.reset()
		rows := pagesListRows(t, h, wsID, "dave", "MEMBER")
		n := counter.count()
		if len(rows) != pageCount {
			t.Fatalf("listed %d pages, want %d", len(rows), pageCount)
		}
		for slug, row := range rows {
			if got := pagesReachOf(t, row); !reflect.DeepEqual(got, []string{"grant"}) {
				t.Fatalf("%s: reach = %v, want [grant]", slug, got)
			}
		}
		return n
	}

	small := countFor(t, 3)
	large := countFor(t, 80)
	t.Logf("List ran %d statements for 3 pages and %d for 80", small, large)
	if small == 0 {
		t.Fatal("the counter saw no statements at all — the handler is not running through the counted handle")
	}
	if large != small {
		t.Errorf("List ran %d statements for 80 pages and %d for 3: %d statements are being spent per page",
			large, small, (large-small)/77)
	}
}
