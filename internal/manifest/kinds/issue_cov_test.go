package kinds

// Coverage-focused tests for issue.go. Reuses the scriptable covClient
// fake defined in routine_cov_test.go. The existing issue_test.go covers
// the happy paths; this file pins the error branches of the list/resolve
// helpers, the Plan FK-resolution failures, the Exec closures, and the
// Export error paths.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// ── issueCheckStatus / issueReadAll ─────────────────────────────────

func TestIssueCov_CheckStatus(t *testing.T) {
	t.Parallel()

	if err := issueCheckStatus(nil, "op"); err == nil || !strings.Contains(err.Error(), "nil response") {
		t.Fatalf("nil response: got %v", err)
	}
	if err := issueCheckStatus(&internalapi.Response{StatusCode: 204}, "op"); err != nil {
		t.Fatalf("2xx must pass, got %v", err)
	}
	err := issueCheckStatus(&internalapi.Response{StatusCode: 500, Body: strings.NewReader("boom")}, "op")
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("500 with body: got %v", err)
	}
	err = issueCheckStatus(&internalapi.Response{StatusCode: 404}, "op")
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("404 nil body: got %v", err)
	}
}

func TestIssueCov_ReadAll(t *testing.T) {
	t.Parallel()

	b, err := issueReadAll(nil)
	if b != nil || err != nil {
		t.Fatalf("nil reader: want (nil,nil), got (%v,%v)", b, err)
	}
	b, err = issueReadAll(strings.NewReader("xy"))
	if err != nil || string(b) != "xy" {
		t.Fatalf("read: got (%q,%v)", b, err)
	}
}

// ── list helpers error paths ────────────────────────────────────────

func TestIssueCov_ListHelpers_Errors(t *testing.T) {
	t.Parallel()

	transportErr := errors.New("conn refused")

	cases := []struct {
		name    string
		path    string
		call    func(c internalapi.Client) error
		wantErr string
		route   covRoute
	}{
		{
			name: "crews transport error",
			path: "/api/v1/crews",
			call: func(c internalapi.Client) error {
				_, err := issueListCrews(context.Background(), c)
				return err
			},
			route:   covRoute{err: transportErr},
			wantErr: "GET /api/v1/crews",
		},
		{
			name: "crews bad status",
			path: "/api/v1/crews",
			call: func(c internalapi.Client) error {
				_, err := issueListCrews(context.Background(), c)
				return err
			},
			route:   covRoute{status: 500, body: "oops"},
			wantErr: "HTTP 500",
		},
		{
			name: "crews body read failure",
			path: "/api/v1/crews",
			call: func(c internalapi.Client) error {
				_, err := issueListCrews(context.Background(), c)
				return err
			},
			route:   covRoute{badBody: true},
			wantErr: "read /api/v1/crews body",
		},
		{
			name: "crews decode failure",
			path: "/api/v1/crews",
			call: func(c internalapi.Client) error {
				_, err := issueListCrews(context.Background(), c)
				return err
			},
			route:   covRoute{body: "{not json"},
			wantErr: "decode /api/v1/crews",
		},
		{
			name: "projects transport error",
			path: "/api/v1/projects",
			call: func(c internalapi.Client) error {
				_, err := issueListProjects(context.Background(), c)
				return err
			},
			route:   covRoute{err: transportErr},
			wantErr: "GET /api/v1/projects",
		},
		{
			name: "projects bad status",
			path: "/api/v1/projects",
			call: func(c internalapi.Client) error {
				_, err := issueListProjects(context.Background(), c)
				return err
			},
			route:   covRoute{status: 403},
			wantErr: "HTTP 403",
		},
		{
			name: "projects body read failure",
			path: "/api/v1/projects",
			call: func(c internalapi.Client) error {
				_, err := issueListProjects(context.Background(), c)
				return err
			},
			route:   covRoute{badBody: true},
			wantErr: "read /api/v1/projects body",
		},
		{
			name: "projects decode failure",
			path: "/api/v1/projects",
			call: func(c internalapi.Client) error {
				_, err := issueListProjects(context.Background(), c)
				return err
			},
			route:   covRoute{body: "????"},
			wantErr: "decode /api/v1/projects",
		},
		{
			name: "agents transport error",
			path: "/api/v1/agents",
			call: func(c internalapi.Client) error {
				_, err := issueListAgents(context.Background(), c)
				return err
			},
			route:   covRoute{err: transportErr},
			wantErr: "GET /api/v1/agents",
		},
		{
			name: "agents bad status",
			path: "/api/v1/agents",
			call: func(c internalapi.Client) error {
				_, err := issueListAgents(context.Background(), c)
				return err
			},
			route:   covRoute{status: 502},
			wantErr: "HTTP 502",
		},
		{
			name: "agents body read failure",
			path: "/api/v1/agents",
			call: func(c internalapi.Client) error {
				_, err := issueListAgents(context.Background(), c)
				return err
			},
			route:   covRoute{badBody: true},
			wantErr: "read /api/v1/agents body",
		},
		{
			name: "agents decode failure",
			path: "/api/v1/agents",
			call: func(c internalapi.Client) error {
				_, err := issueListAgents(context.Background(), c)
				return err
			},
			route:   covRoute{body: "nope"},
			wantErr: "decode /api/v1/agents",
		},
		{
			name: "labels transport error",
			path: "/api/v1/labels",
			call: func(c internalapi.Client) error {
				_, err := issueListLabels(context.Background(), c)
				return err
			},
			route:   covRoute{err: transportErr},
			wantErr: "GET /api/v1/labels",
		},
		{
			name: "labels bad status",
			path: "/api/v1/labels",
			call: func(c internalapi.Client) error {
				_, err := issueListLabels(context.Background(), c)
				return err
			},
			route:   covRoute{status: 500},
			wantErr: "HTTP 500",
		},
		{
			name: "labels body read failure",
			path: "/api/v1/labels",
			call: func(c internalapi.Client) error {
				_, err := issueListLabels(context.Background(), c)
				return err
			},
			route:   covRoute{badBody: true},
			wantErr: "read /api/v1/labels body",
		},
		{
			name: "labels decode failure",
			path: "/api/v1/labels",
			call: func(c internalapi.Client) error {
				_, err := issueListLabels(context.Background(), c)
				return err
			},
			route:   covRoute{body: "x"},
			wantErr: "decode /api/v1/labels",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newCovClient(map[string]covRoute{"GET " + tc.path: tc.route})
			err := tc.call(c)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestIssueCov_ListHelpers_EmptyBody(t *testing.T) {
	t.Parallel()

	c := newCovClient(map[string]covRoute{
		"GET /api/v1/crews":    {body: ""},
		"GET /api/v1/projects": {body: ""},
		"GET /api/v1/agents":   {body: ""},
		"GET /api/v1/labels":   {body: ""},
	})
	if rows, err := issueListCrews(context.Background(), c); err != nil || rows != nil {
		t.Fatalf("crews empty body: got (%v,%v)", rows, err)
	}
	if rows, err := issueListProjects(context.Background(), c); err != nil || rows != nil {
		t.Fatalf("projects empty body: got (%v,%v)", rows, err)
	}
	if rows, err := issueListAgents(context.Background(), c); err != nil || rows != nil {
		t.Fatalf("agents empty body: got (%v,%v)", rows, err)
	}
	if rows, err := issueListLabels(context.Background(), c); err != nil || rows != nil {
		t.Fatalf("labels empty body: got (%v,%v)", rows, err)
	}
}

// ── issueListForCrew ────────────────────────────────────────────────

func TestIssueCov_ListForCrew_Pagination(t *testing.T) {
	t.Parallel()

	// First page: exactly 100 rows (full page) → fetch continues.
	// Second page: 1 row (short page) → loop stops.
	var page0 strings.Builder
	page0.WriteString("[")
	for i := 0; i < 100; i++ {
		if i > 0 {
			page0.WriteString(",")
		}
		fmt.Fprintf(&page0, `{"id":"i%d","title":"t%d","crew_id":"c1"}`, i, i)
	}
	page0.WriteString("]")

	c := newCovClient(map[string]covRoute{
		"GET /api/v1/issues?crew_id=c1&limit=100&offset=0":   {body: page0.String()},
		"GET /api/v1/issues?crew_id=c1&limit=100&offset=100": {body: `[{"id":"i100","title":"tail","crew_id":"c1"}]`},
	})
	rows, err := issueListForCrew(context.Background(), c, "c1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 101 {
		t.Fatalf("want 101 rows across two pages, got %d", len(rows))
	}
	if rows[100].Title != "tail" {
		t.Fatalf("last row title = %q, want %q", rows[100].Title, "tail")
	}
	if !c.sawCall("GET /api/v1/issues?crew_id=c1&limit=100&offset=100") {
		t.Fatalf("second page never requested; calls: %v", c.calls)
	}
}

func TestIssueCov_ListForCrew_Errors(t *testing.T) {
	t.Parallel()

	base := "GET /api/v1/issues?crew_id=c1&limit=100&offset=0"

	t.Run("transport error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{base: {err: errors.New("down")}})
		if _, err := issueListForCrew(context.Background(), c, "c1"); err == nil || !strings.Contains(err.Error(), "down") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("bad status", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{base: {status: 500}})
		if _, err := issueListForCrew(context.Background(), c, "c1"); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("body read failure", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{base: {badBody: true}})
		if _, err := issueListForCrew(context.Background(), c, "c1"); err == nil || !strings.Contains(err.Error(), "read") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("decode failure", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{base: {body: "{{{"}})
		if _, err := issueListForCrew(context.Background(), c, "c1"); err == nil || !strings.Contains(err.Error(), "decode") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("empty body terminates", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{base: {body: ""}})
		rows, err := issueListForCrew(context.Background(), c, "c1")
		if err != nil || len(rows) != 0 {
			t.Fatalf("got (%v,%v)", rows, err)
		}
	})
	t.Run("empty array terminates", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{base: {body: "[]"}})
		rows, err := issueListForCrew(context.Background(), c, "c1")
		if err != nil || len(rows) != 0 {
			t.Fatalf("got (%v,%v)", rows, err)
		}
	})
}

// ── resolvers ───────────────────────────────────────────────────────

func TestIssueCov_LookupCrewIDBySlug(t *testing.T) {
	t.Parallel()

	t.Run("empty slug", func(t *testing.T) {
		t.Parallel()
		_, err := issueLookupCrewIDBySlug(context.Background(), newCovClient(nil), "  ")
		if err == nil || !strings.Contains(err.Error(), "crew slug is empty") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("list error propagates", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {err: errors.New("boom")}})
		_, err := issueLookupCrewIDBySlug(context.Background(), c, "eng")
		if err == nil || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {body: `[{"id":"c1","slug":"other"}]`}})
		_, err := issueLookupCrewIDBySlug(context.Background(), c, "eng")
		if err == nil || !strings.Contains(err.Error(), `crew with slug "eng" not found`) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("found", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {body: `[{"id":"c1","slug":"eng"}]`}})
		id, err := issueLookupCrewIDBySlug(context.Background(), c, "eng")
		if err != nil || id != "c1" {
			t.Fatalf("got (%q,%v)", id, err)
		}
	})
}

func TestIssueCov_ResolveOptionalProjectID(t *testing.T) {
	t.Parallel()

	t.Run("empty slug is no-op", func(t *testing.T) {
		t.Parallel()
		id, err := issueResolveOptionalProjectID(context.Background(), newCovClient(nil), "")
		if id != "" || err != nil {
			t.Fatalf("got (%q,%v)", id, err)
		}
	})
	t.Run("list error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/projects": {err: errors.New("boom")}})
		if _, err := issueResolveOptionalProjectID(context.Background(), c, "roadmap"); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/projects": {body: `[]`}})
		_, err := issueResolveOptionalProjectID(context.Background(), c, "roadmap")
		if err == nil || !strings.Contains(err.Error(), `project with slug "roadmap" not found`) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("found", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/projects": {body: `[{"id":"p1","slug":"roadmap"}]`}})
		id, err := issueResolveOptionalProjectID(context.Background(), c, "roadmap")
		if err != nil || id != "p1" {
			t.Fatalf("got (%q,%v)", id, err)
		}
	})
}

func TestIssueCov_ResolveOptionalAgentID(t *testing.T) {
	t.Parallel()

	t.Run("empty slug is no-op", func(t *testing.T) {
		t.Parallel()
		id, err := issueResolveOptionalAgentID(context.Background(), newCovClient(nil), " ")
		if id != "" || err != nil {
			t.Fatalf("got (%q,%v)", id, err)
		}
	})
	t.Run("list error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/agents": {err: errors.New("boom")}})
		if _, err := issueResolveOptionalAgentID(context.Background(), c, "eva"); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("not found", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/agents": {body: `[]`}})
		_, err := issueResolveOptionalAgentID(context.Background(), c, "eva")
		if err == nil || !strings.Contains(err.Error(), `agent with slug "eva" not found`) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("found", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/agents": {body: `[{"id":"a1","slug":"eva"}]`}})
		id, err := issueResolveOptionalAgentID(context.Background(), c, "eva")
		if err != nil || id != "a1" {
			t.Fatalf("got (%q,%v)", id, err)
		}
	})
}

func TestIssueCov_ResolveLabelIDs(t *testing.T) {
	t.Parallel()

	t.Run("empty input no-op", func(t *testing.T) {
		t.Parallel()
		ids, err := issueResolveLabelIDs(context.Background(), newCovClient(nil), nil)
		if ids != nil || err != nil {
			t.Fatalf("got (%v,%v)", ids, err)
		}
	})
	t.Run("list error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/labels": {err: errors.New("boom")}})
		if _, err := issueResolveLabelIDs(context.Background(), c, []string{"bug"}); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("missing label", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/labels": {body: `[{"id":"l1","name":"bug"}]`}})
		_, err := issueResolveLabelIDs(context.Background(), c, []string{"bug", "feature"})
		if err == nil || !strings.Contains(err.Error(), `label "feature" not found`) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("resolved and sorted", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/labels": {body: `[{"id":"z9","name":"bug"},{"id":"a1","name":"feature"}]`}})
		ids, err := issueResolveLabelIDs(context.Background(), c, []string{"bug", "feature"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(ids) != 2 || ids[0] != "a1" || ids[1] != "z9" {
			t.Fatalf("want sorted [a1 z9], got %v", ids)
		}
	})
}

// ── Plan error and exec paths ───────────────────────────────────────

func issueCovDoc() *IssueDocument {
	return &IssueDocument{
		APIVersion: "crewship/v1",
		Kind:       "Issue",
		Metadata:   internalapi.Metadata{Name: "Ping check", Slug: "eng--ping-check"},
		Spec: IssueSpec{
			CrewSlug: "eng",
			Title:    "Ping check",
		},
	}
}

func TestIssueCov_Plan_CrewResolveFails(t *testing.T) {
	t.Parallel()

	c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {body: `[]`}})
	_, err := issueCovDoc().Plan(context.Background(), c, nil)
	if err == nil || !strings.Contains(err.Error(), "resolve crew_slug") {
		t.Fatalf("got %v", err)
	}
}

func TestIssueCov_Plan_CreateResolveFailures(t *testing.T) {
	t.Parallel()

	crews := covRoute{body: `[{"id":"c1","slug":"eng"}]`}

	t.Run("project resolve fails", func(t *testing.T) {
		t.Parallel()
		doc := issueCovDoc()
		doc.Spec.ProjectSlug = "ghost"
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/crews":    crews,
			"GET /api/v1/projects": {body: `[]`},
		})
		_, err := doc.Plan(context.Background(), c, nil)
		if err == nil || !strings.Contains(err.Error(), "resolve project_slug") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("assignee resolve fails", func(t *testing.T) {
		t.Parallel()
		doc := issueCovDoc()
		doc.Spec.AssigneeSlug = "ghost"
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/crews":  crews,
			"GET /api/v1/agents": {body: `[]`},
		})
		_, err := doc.Plan(context.Background(), c, nil)
		if err == nil || !strings.Contains(err.Error(), "resolve assignee_slug") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("labels resolve fails", func(t *testing.T) {
		t.Parallel()
		doc := issueCovDoc()
		doc.Spec.Labels = []string{"ghost"}
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/crews":  crews,
			"GET /api/v1/labels": {body: `[]`},
		})
		_, err := doc.Plan(context.Background(), c, nil)
		if err == nil || !strings.Contains(err.Error(), "resolve labels") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestIssueCov_Plan_CreateExec(t *testing.T) {
	t.Parallel()

	t.Run("post transport error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/crews":            {body: `[{"id":"c1","slug":"eng"}]`},
			"POST /api/v1/crews/c1/issues": {err: errors.New("down")},
		})
		items, err := issueCovDoc().Plan(context.Background(), c, nil)
		if err != nil || len(items) != 1 || items[0].Action != internalapi.ActionCreate {
			t.Fatalf("plan: items=%v err=%v", items, err)
		}
		if execErr := items[0].Exec(context.Background(), c); execErr == nil || !strings.Contains(execErr.Error(), "POST /api/v1/crews/c1/issues") {
			t.Fatalf("exec: got %v", execErr)
		}
	})
	t.Run("post bad status", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/crews":            {body: `[{"id":"c1","slug":"eng"}]`},
			"POST /api/v1/crews/c1/issues": {status: 422, body: "bad payload"},
		})
		items, err := issueCovDoc().Plan(context.Background(), c, nil)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if execErr := items[0].Exec(context.Background(), c); execErr == nil || !strings.Contains(execErr.Error(), "HTTP 422") {
			t.Fatalf("exec: got %v", execErr)
		}
	})
}

func TestIssueCov_Plan_Update(t *testing.T) {
	t.Parallel()

	crews := covRoute{body: `[{"id":"c1","slug":"eng"}]`}
	ident := "ENG-1"

	remote := func() *IssueRemote {
		return &IssueRemote{
			ID:         "i1",
			CrewID:     "c1",
			Identifier: &ident,
			Title:      "Ping check",
			Status:     "BACKLOG",
			Priority:   "none",
		}
	}

	t.Run("update project resolve fails", func(t *testing.T) {
		t.Parallel()
		doc := issueCovDoc()
		doc.Spec.ProjectSlug = "ghost"
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/crews":    crews,
			"GET /api/v1/projects": {body: `[]`},
		})
		_, err := doc.Plan(context.Background(), c, remote())
		if err == nil || !strings.Contains(err.Error(), "resolve project_slug") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("update assignee resolve fails", func(t *testing.T) {
		t.Parallel()
		doc := issueCovDoc()
		doc.Spec.AssigneeSlug = "ghost"
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/crews":  crews,
			"GET /api/v1/agents": {body: `[]`},
		})
		_, err := doc.Plan(context.Background(), c, remote())
		if err == nil || !strings.Contains(err.Error(), "resolve assignee_slug") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("labels changed but resolution fails", func(t *testing.T) {
		t.Parallel()
		doc := issueCovDoc()
		doc.Spec.Labels = []string{"ghost"}
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/crews":  crews,
			"GET /api/v1/labels": {body: `[]`},
		})
		_, err := doc.Plan(context.Background(), c, remote())
		if err == nil || !strings.Contains(err.Error(), "resolve labels") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("clearing labels sends empty array", func(t *testing.T) {
		t.Parallel()
		doc := issueCovDoc()
		// declared no labels; remote has one → labelsChanged.
		r := remote()
		r.Labels = []issueRemoteLabel{{ID: "l1", Name: "bug"}}
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/crews":                   crews,
			"PATCH /api/v1/crews/c1/issues/ENG-1": {status: 200, body: "{}"},
		})
		items, err := doc.Plan(context.Background(), c, r)
		if err != nil || len(items) != 1 || items[0].Action != internalapi.ActionUpdate {
			t.Fatalf("plan: items=%v err=%v", items, err)
		}
		if execErr := items[0].Exec(context.Background(), c); execErr != nil {
			t.Fatalf("exec: %v", execErr)
		}
		if !c.sawCall("PATCH /api/v1/crews/c1/issues/ENG-1") {
			t.Fatalf("PATCH never issued; calls: %v", c.calls)
		}
	})
	t.Run("missing identifier fails exec", func(t *testing.T) {
		t.Parallel()
		doc := issueCovDoc()
		doc.Spec.Priority = "high"
		r := remote()
		r.Identifier = nil
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews": crews})
		items, err := doc.Plan(context.Background(), c, r)
		if err != nil || len(items) != 1 {
			t.Fatalf("plan: items=%v err=%v", items, err)
		}
		if execErr := items[0].Exec(context.Background(), c); execErr == nil || !strings.Contains(execErr.Error(), "no identifier") {
			t.Fatalf("exec: got %v", execErr)
		}
	})
	t.Run("patch transport error", func(t *testing.T) {
		t.Parallel()
		doc := issueCovDoc()
		doc.Spec.Priority = "high"
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/crews":                   crews,
			"PATCH /api/v1/crews/c1/issues/ENG-1": {err: errors.New("down")},
		})
		items, err := doc.Plan(context.Background(), c, remote())
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if execErr := items[0].Exec(context.Background(), c); execErr == nil || !strings.Contains(execErr.Error(), "PATCH /api/v1/crews/c1/issues/ENG-1") {
			t.Fatalf("exec: got %v", execErr)
		}
	})
	t.Run("remote crew id fallback", func(t *testing.T) {
		t.Parallel()
		doc := issueCovDoc()
		doc.Spec.Priority = "high"
		r := remote()
		r.CrewID = "" // force the declared-slug fallback
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/crews":                   crews,
			"PATCH /api/v1/crews/c1/issues/ENG-1": {status: 200, body: "{}"},
		})
		items, err := doc.Plan(context.Background(), c, r)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if execErr := items[0].Exec(context.Background(), c); execErr != nil {
			t.Fatalf("exec: %v", execErr)
		}
		if !c.sawCall("PATCH /api/v1/crews/c1/issues/ENG-1") {
			t.Fatalf("fallback crew id not used; calls: %v", c.calls)
		}
	})
}

// ── diffPatch branches ──────────────────────────────────────────────

func TestIssueCov_DiffPatch(t *testing.T) {
	t.Parallel()

	desc := "old desc"
	aType := "agent"
	aID := "a1"
	pID := "p1"
	remote := &IssueRemote{
		Title:        "Old title",
		Description:  &desc,
		Status:       "BACKLOG",
		Priority:     "none",
		AssigneeType: &aType,
		AssigneeID:   &aID,
		ProjectID:    &pID,
	}

	doc := &IssueDocument{
		Metadata: internalapi.Metadata{Slug: "s", Name: "New title"},
		Spec: IssueSpec{
			CrewSlug:    "eng",
			Description: "new desc",
			Priority:    "urgent",
			Status:      "done",
		},
	}
	patch, labelsChanged, err := doc.diffPatch(remote, "p2", "a2")
	if err != nil {
		t.Fatalf("diffPatch: %v", err)
	}
	if patch["title"] != "New title" {
		t.Fatalf("title patch missing: %v", patch)
	}
	if patch["description"] != "new desc" {
		t.Fatalf("description patch missing: %v", patch)
	}
	if patch["priority"] != "urgent" {
		t.Fatalf("priority patch missing: %v", patch)
	}
	if patch["status"] != "DONE" {
		t.Fatalf("status should be canonicalised to DONE: %v", patch)
	}
	if patch["assignee_id"] != "a2" || patch["assignee_type"] != "agent" {
		t.Fatalf("assignee patch missing: %v", patch)
	}
	if patch["project_id"] != "p2" {
		t.Fatalf("project patch missing: %v", patch)
	}
	if labelsChanged {
		t.Fatal("labels did not drift (both empty)")
	}

	// No drift when everything matches.
	same := &IssueDocument{
		Metadata: internalapi.Metadata{Slug: "s", Name: "Old title"},
		Spec:     IssueSpec{CrewSlug: "eng", Description: "old desc", Priority: "none", Status: "backlog"},
	}
	patch, labelsChanged, err = same.diffPatch(remote, "p1", "a1")
	if err != nil {
		t.Fatalf("diffPatch: %v", err)
	}
	if len(patch) != 0 || labelsChanged {
		t.Fatalf("want empty patch, got %v (labelsChanged=%t)", patch, labelsChanged)
	}
}

// ── ExportIssues ────────────────────────────────────────────────────

func TestIssueCov_ExportIssues_Errors(t *testing.T) {
	t.Parallel()

	crews := covRoute{body: `[{"id":"c1","slug":"eng"}]`}
	projects := covRoute{body: `[{"id":"p1","slug":"roadmap"}]`}
	agents := covRoute{body: `[{"id":"a1","slug":"eva"}]`}

	t.Run("crews list fails", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {err: errors.New("boom")}})
		_, err := ExportIssues(context.Background(), c)
		if err == nil || !strings.Contains(err.Error(), "export issues: list crews") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("projects list fails", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/crews":    crews,
			"GET /api/v1/projects": {err: errors.New("boom")},
		})
		_, err := ExportIssues(context.Background(), c)
		if err == nil || !strings.Contains(err.Error(), "export issues: list projects") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("agents list fails", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/crews":    crews,
			"GET /api/v1/projects": projects,
			"GET /api/v1/agents":   {err: errors.New("boom")},
		})
		_, err := ExportIssues(context.Background(), c)
		if err == nil || !strings.Contains(err.Error(), "export issues: list agents") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("per-crew issue list fails", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/crews":                                crews,
			"GET /api/v1/projects":                             projects,
			"GET /api/v1/agents":                               agents,
			"GET /api/v1/issues?crew_id=c1&limit=100&offset=0": {status: 500},
		})
		_, err := ExportIssues(context.Background(), c)
		if err == nil || !strings.Contains(err.Error(), `export issues for crew "eng"`) {
			t.Fatalf("got %v", err)
		}
	})
}

func TestIssueCov_ExportIssues_RoundTrip(t *testing.T) {
	t.Parallel()

	issueRow := `[{
		"id":"i1","crew_id":"c1","identifier":"ENG-1","title":"Fix login",
		"description":"broken","status":"TODO","priority":"high",
		"assignee_type":"agent","assignee_id":"a1","project_id":"p1",
		"labels":[{"id":"l1","name":"bug"},{"id":"l2","name":"auth"}]
	}]`
	c := newCovClient(map[string]covRoute{
		"GET /api/v1/crews":                                {body: `[{"id":"c1","slug":"eng"}]`},
		"GET /api/v1/projects":                             {body: `[{"id":"p1","slug":"roadmap"}]`},
		"GET /api/v1/agents":                               {body: `[{"id":"a1","slug":"eva"}]`},
		"GET /api/v1/issues?crew_id=c1&limit=100&offset=0": {body: issueRow},
	})
	docs, err := ExportIssues(context.Background(), c)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("want 1 doc, got %d", len(docs))
	}
	d := docs[0]
	if d.Metadata.Slug != "eng--fix-login" {
		t.Fatalf("slug = %q", d.Metadata.Slug)
	}
	if d.Spec.ProjectSlug != "roadmap" || d.Spec.AssigneeSlug != "eva" {
		t.Fatalf("FK slugs not resolved: %+v", d.Spec)
	}
	if len(d.Spec.Labels) != 2 || d.Spec.Labels[0] != "auth" || d.Spec.Labels[1] != "bug" {
		t.Fatalf("labels = %v, want sorted [auth bug]", d.Spec.Labels)
	}
	if d.Spec.Description != "broken" || d.Spec.Priority != "high" || d.Spec.Status != "TODO" {
		t.Fatalf("spec fields wrong: %+v", d.Spec)
	}
}

// ── issueSlugFromTitle ──────────────────────────────────────────────

func TestIssueCov_SlugFromTitle(t *testing.T) {
	t.Parallel()

	ident := "ENG-7"
	cases := []struct {
		title string
		ident *string
		want  string
	}{
		{"Fix the login.flow", nil, "fix-the-login-flow"},
		{"  Already-Kebab ", nil, "already-kebab"},
		{"🔥🔥🔥", &ident, "eng-7"},
		{"!!!", nil, "issue"},
		{"trailing dots...", nil, "trailing-dots"},
	}
	for _, tc := range cases {
		if got := issueSlugFromTitle(tc.title, tc.ident); got != tc.want {
			t.Errorf("issueSlugFromTitle(%q) = %q, want %q", tc.title, got, tc.want)
		}
	}
}
