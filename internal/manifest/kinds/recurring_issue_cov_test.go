package kinds

// Coverage-focused tests for recurring_issue.go. Reuses the scriptable
// covClient fake from routine_cov_test.go. recurring_issue_test.go owns
// the happy paths; this file pins the slug→id lookup branches, the
// template equality matrix, the Plan exec error paths, and the Export
// reverse-mapping error paths.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// ── tiny helpers ────────────────────────────────────────────────────

func TestRecurringIssueCov_ReadBodyForError(t *testing.T) {
	t.Parallel()

	if got := readBodyForError(nil); got != "(no body)" {
		t.Fatalf("nil resp: %q", got)
	}
	if got := readBodyForError(&internalapi.Response{StatusCode: 500}); got != "(no body)" {
		t.Fatalf("nil body: %q", got)
	}
	if got := readBodyForError(&internalapi.Response{StatusCode: 500, Body: strings.NewReader("  \n ")}); got != "(empty body)" {
		t.Fatalf("whitespace body: %q", got)
	}
	if got := readBodyForError(&internalapi.Response{StatusCode: 500, Body: strings.NewReader(" oops ")}); got != "oops" {
		t.Fatalf("body: %q", got)
	}
}

func TestRecurringIssueCov_ReadResponseBody(t *testing.T) {
	t.Parallel()

	b, err := readResponseBody(nil)
	if b != nil || err != nil {
		t.Fatalf("nil resp: got (%v,%v)", b, err)
	}
	b, err = readResponseBody(&internalapi.Response{StatusCode: 200})
	if b != nil || err != nil {
		t.Fatalf("nil body: got (%v,%v)", b, err)
	}
	_, err = readResponseBody(&internalapi.Response{StatusCode: 500, Body: strings.NewReader("broke")})
	if err == nil || !strings.Contains(err.Error(), "status 500") || !strings.Contains(err.Error(), "broke") {
		t.Fatalf("error status: got %v", err)
	}
	b, err = readResponseBody(&internalapi.Response{StatusCode: 200, Body: strings.NewReader("ok")})
	if err != nil || string(b) != "ok" {
		t.Fatalf("happy: got (%q,%v)", b, err)
	}
}

func TestRecurringIssueCov_MetaName(t *testing.T) {
	t.Parallel()

	if got := metaName(internalapi.Metadata{Name: "Daily", Slug: "daily"}); got != "Daily" {
		t.Fatalf("name set: %q", got)
	}
	if got := metaName(internalapi.Metadata{Slug: "daily"}); got != "daily" {
		t.Fatalf("slug fallback: %q", got)
	}
}

// ── lookupSlugID ────────────────────────────────────────────────────

func TestRecurringIssueCov_LookupSlugID(t *testing.T) {
	t.Parallel()

	t.Run("empty slug short-circuits", func(t *testing.T) {
		t.Parallel()
		id, err := lookupSlugID(context.Background(), newCovClient(nil), "/api/v1/crews", "")
		if id != "" || err != nil {
			t.Fatalf("got (%q,%v)", id, err)
		}
	})
	t.Run("transport error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {err: errors.New("down")}})
		_, err := lookupSlugID(context.Background(), c, "/api/v1/crews", "eng")
		if err == nil || !strings.Contains(err.Error(), "down") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("status error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {status: 500, body: "err"}})
		_, err := lookupSlugID(context.Background(), c, "/api/v1/crews", "eng")
		if err == nil || !strings.Contains(err.Error(), "status 500") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("flat match by slug", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {body: `[{"id":"c1","slug":"eng"}]`}})
		id, err := lookupSlugID(context.Background(), c, "/api/v1/crews", "eng")
		if err != nil || id != "c1" {
			t.Fatalf("got (%q,%v)", id, err)
		}
	})
	t.Run("flat match by name when slug empty", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/labels": {body: `[{"id":"l1","name":"bug"}]`}})
		id, err := lookupSlugID(context.Background(), c, "/api/v1/labels", "bug")
		if err != nil || id != "l1" {
			t.Fatalf("got (%q,%v)", id, err)
		}
	})
	t.Run("flat not found", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {body: `[{"id":"c1","slug":"other"}]`}})
		id, err := lookupSlugID(context.Background(), c, "/api/v1/crews", "eng")
		if err != nil || id != "" {
			t.Fatalf("got (%q,%v)", id, err)
		}
	})
	t.Run("wrapped items match", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {body: `{"items":[{"id":"c1","slug":"eng"}]}`}})
		id, err := lookupSlugID(context.Background(), c, "/api/v1/crews", "eng")
		if err != nil || id != "c1" {
			t.Fatalf("got (%q,%v)", id, err)
		}
	})
	t.Run("wrapped items match by name", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {body: `{"items":[{"id":"c2","name":"eng"}]}`}})
		id, err := lookupSlugID(context.Background(), c, "/api/v1/crews", "eng")
		if err != nil || id != "c2" {
			t.Fatalf("got (%q,%v)", id, err)
		}
	})
	t.Run("wrapped items not found", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {body: `{"items":[]}`}})
		id, err := lookupSlugID(context.Background(), c, "/api/v1/crews", "eng")
		if err != nil || id != "" {
			t.Fatalf("got (%q,%v)", id, err)
		}
	})
	t.Run("both decode shapes fail", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {body: `"oops"`}})
		_, err := lookupSlugID(context.Background(), c, "/api/v1/crews", "eng")
		if err == nil || !strings.Contains(err.Error(), "decode list at /api/v1/crews") {
			t.Fatalf("got %v", err)
		}
	})
}

// ── listLabels ──────────────────────────────────────────────────────

func TestRecurringIssueCov_ListLabels(t *testing.T) {
	t.Parallel()

	t.Run("transport error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/labels": {err: errors.New("down")}})
		if _, err := listLabels(context.Background(), c); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("status error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/labels": {status: 500}})
		if _, err := listLabels(context.Background(), c); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("decode error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/labels": {body: "x"}})
		_, err := listLabels(context.Background(), c)
		if err == nil || !strings.Contains(err.Error(), "decode labels list") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("keys on slug and name", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/labels": {body: `[{"id":"l1","slug":"bug-s","name":"bug"}]`}})
		m, err := listLabels(context.Background(), c)
		if err != nil {
			t.Fatalf("listLabels: %v", err)
		}
		if m["bug-s"] != "l1" || m["bug"] != "l1" {
			t.Fatalf("map = %v", m)
		}
	})
}

// ── templatesEqual ──────────────────────────────────────────────────

func TestRecurringIssueCov_TemplatesEqual(t *testing.T) {
	t.Parallel()

	base := func() *RecurringIssueRemoteTemplate {
		return &RecurringIssueRemoteTemplate{
			Title: "T", Description: "D", Priority: "high",
			CrewID: "c1", ProjectID: "p1", AssigneeAgentID: "a1",
			LabelIDs: []string{"l1", "l2"},
		}
	}

	if !templatesEqual(nil, nil) {
		t.Fatal("nil,nil should be equal")
	}
	if templatesEqual(base(), nil) || templatesEqual(nil, base()) {
		t.Fatal("nil vs non-nil should differ")
	}
	if !templatesEqual(base(), base()) {
		t.Fatal("identical should be equal")
	}

	// Label order must not matter.
	b := base()
	b.LabelIDs = []string{"l2", "l1"}
	if !templatesEqual(base(), b) {
		t.Fatal("label order must not produce drift")
	}

	mutations := []func(*RecurringIssueRemoteTemplate){
		func(x *RecurringIssueRemoteTemplate) { x.Title = "x" },
		func(x *RecurringIssueRemoteTemplate) { x.Description = "x" },
		func(x *RecurringIssueRemoteTemplate) { x.Priority = "low" },
		func(x *RecurringIssueRemoteTemplate) { x.CrewID = "x" },
		func(x *RecurringIssueRemoteTemplate) { x.ProjectID = "x" },
		func(x *RecurringIssueRemoteTemplate) { x.AssigneeAgentID = "x" },
		func(x *RecurringIssueRemoteTemplate) { x.LabelIDs = []string{"l1"} },
		func(x *RecurringIssueRemoteTemplate) { x.LabelIDs = []string{"l1", "l3"} },
	}
	for i, mutate := range mutations {
		m := base()
		mutate(m)
		if templatesEqual(base(), m) {
			t.Errorf("mutation %d should produce drift", i)
		}
	}
}

// ── resolveRecurringIssueTemplateToIDs ──────────────────────────────

func TestRecurringIssueCov_ResolveTemplate(t *testing.T) {
	t.Parallel()

	crews := covRoute{body: `[{"id":"c1","slug":"eng"}]`}

	t.Run("crew lookup error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {err: errors.New("down")}})
		_, err := resolveRecurringIssueTemplateToIDs(context.Background(), c, &RecurringIssueTemplate{CrewSlug: "eng"})
		if err == nil || !strings.Contains(err.Error(), `resolve crew "eng"`) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("crew not found", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {body: `[]`}})
		_, err := resolveRecurringIssueTemplateToIDs(context.Background(), c, &RecurringIssueTemplate{CrewSlug: "eng"})
		if err == nil || !strings.Contains(err.Error(), `crew "eng" not found`) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("project lookup error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/crews":    crews,
			"GET /api/v1/projects": {err: errors.New("down")},
		})
		_, err := resolveRecurringIssueTemplateToIDs(context.Background(), c,
			&RecurringIssueTemplate{CrewSlug: "eng", ProjectSlug: "p"})
		if err == nil || !strings.Contains(err.Error(), `resolve project "p"`) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("project not found", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/crews":    crews,
			"GET /api/v1/projects": {body: `[]`},
		})
		_, err := resolveRecurringIssueTemplateToIDs(context.Background(), c,
			&RecurringIssueTemplate{CrewSlug: "eng", ProjectSlug: "p"})
		if err == nil || !strings.Contains(err.Error(), `project "p" not found`) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("agent lookup error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/crews":  crews,
			"GET /api/v1/agents": {err: errors.New("down")},
		})
		_, err := resolveRecurringIssueTemplateToIDs(context.Background(), c,
			&RecurringIssueTemplate{CrewSlug: "eng", AssigneeAgentSlug: "eva"})
		if err == nil || !strings.Contains(err.Error(), `resolve agent "eva"`) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("agent not found", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/crews":  crews,
			"GET /api/v1/agents": {body: `[]`},
		})
		_, err := resolveRecurringIssueTemplateToIDs(context.Background(), c,
			&RecurringIssueTemplate{CrewSlug: "eng", AssigneeAgentSlug: "eva"})
		if err == nil || !strings.Contains(err.Error(), `agent "eva" not found`) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("labels list error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/crews":  crews,
			"GET /api/v1/labels": {err: errors.New("down")},
		})
		_, err := resolveRecurringIssueTemplateToIDs(context.Background(), c,
			&RecurringIssueTemplate{CrewSlug: "eng", Labels: []string{"bug"}})
		if err == nil || !strings.Contains(err.Error(), "list labels") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("label missing", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/crews":  crews,
			"GET /api/v1/labels": {body: `[{"id":"l1","name":"bug"}]`},
		})
		_, err := resolveRecurringIssueTemplateToIDs(context.Background(), c,
			&RecurringIssueTemplate{CrewSlug: "eng", Labels: []string{"bug", "ghost"}})
		if err == nil || !strings.Contains(err.Error(), `label "ghost" not found`) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("full resolution sorted labels", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/crews":    crews,
			"GET /api/v1/projects": {body: `[{"id":"p1","slug":"road"}]`},
			"GET /api/v1/agents":   {body: `[{"id":"a1","slug":"eva"}]`},
			"GET /api/v1/labels":   {body: `[{"id":"z2","name":"bug"},{"id":"a9","name":"ops"}]`},
		})
		out, err := resolveRecurringIssueTemplateToIDs(context.Background(), c, &RecurringIssueTemplate{
			Title: "T", Description: "D", Priority: "high",
			CrewSlug: "eng", ProjectSlug: "road", AssigneeAgentSlug: "eva",
			Labels: []string{"bug", "ops"},
		})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if out.CrewID != "c1" || out.ProjectID != "p1" || out.AssigneeAgentID != "a1" {
			t.Fatalf("ids = %+v", out)
		}
		if len(out.LabelIDs) != 2 || out.LabelIDs[0] != "a9" || out.LabelIDs[1] != "z2" {
			t.Fatalf("labels not sorted: %v", out.LabelIDs)
		}
	})
}

// ── buildRecurringIssueBody ─────────────────────────────────────────

func TestRecurringIssueCov_BuildBody(t *testing.T) {
	t.Parallel()

	t.Run("resolve failure propagates", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {body: `[]`}})
		_, err := buildRecurringIssueBody(context.Background(), c,
			internalapi.Metadata{Slug: "d"}, &RecurringIssueSpec{Template: RecurringIssueTemplate{CrewSlug: "eng"}})
		if err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("body includes description", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {body: `[{"id":"c1","slug":"eng"}]`}})
		body, err := buildRecurringIssueBody(context.Background(), c,
			internalapi.Metadata{Slug: "daily", Description: "checks"},
			&RecurringIssueSpec{Cron: "0 9 * * *", Timezone: "UTC", Template: RecurringIssueTemplate{Title: "T", CrewSlug: "eng"}})
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		if body["name"] != "daily" || body["slug"] != "daily" || body["description"] != "checks" {
			t.Fatalf("body = %v", body)
		}
		if body["enabled"] != true || body["cron"] != "0 9 * * *" || body["timezone"] != "UTC" {
			t.Fatalf("body = %v", body)
		}
		tmpl, _ := body["template_json"].(string)
		if !strings.Contains(tmpl, `"crew_id":"c1"`) {
			t.Fatalf("template_json = %q", tmpl)
		}
	})
}

// ── Plan exec error paths ───────────────────────────────────────────

func recurringIssueCovDoc() *RecurringIssueDocument {
	return &RecurringIssueDocument{
		APIVersion: "crewship/v1",
		Kind:       "RecurringIssue",
		Metadata:   internalapi.Metadata{Name: "Daily", Slug: "daily"},
		Spec: RecurringIssueSpec{
			Cron:     "0 9 * * *",
			Timezone: "UTC",
			Template: RecurringIssueTemplate{Title: "T", CrewSlug: "eng"},
		},
	}
}

func TestRecurringIssueCov_Plan_CreateExec(t *testing.T) {
	t.Parallel()

	crews := covRoute{body: `[{"id":"c1","slug":"eng"}]`}

	t.Run("body build fails inside exec", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {body: `[]`}})
		items, err := recurringIssueCovDoc().Plan(context.Background(), c, nil)
		if err != nil || len(items) != 1 || items[0].Action != internalapi.ActionCreate {
			t.Fatalf("plan: items=%v err=%v", items, err)
		}
		if execErr := items[0].Exec(context.Background(), c); execErr == nil || !strings.Contains(execErr.Error(), "build recurring_issue body") {
			t.Fatalf("exec: got %v", execErr)
		}
	})
	t.Run("post transport error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/crews":             crews,
			"POST /api/v1/recurring-issues": {err: errors.New("down")},
		})
		items, _ := recurringIssueCovDoc().Plan(context.Background(), c, nil)
		if execErr := items[0].Exec(context.Background(), c); execErr == nil || !strings.Contains(execErr.Error(), `create recurring_issue "daily"`) {
			t.Fatalf("exec: got %v", execErr)
		}
	})
	t.Run("post bad status", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/crews":             crews,
			"POST /api/v1/recurring-issues": {status: 422, body: "bad"},
		})
		items, _ := recurringIssueCovDoc().Plan(context.Background(), c, nil)
		if execErr := items[0].Exec(context.Background(), c); execErr == nil || !strings.Contains(execErr.Error(), "server returned 422") {
			t.Fatalf("exec: got %v", execErr)
		}
	})
}

func TestRecurringIssueCov_Plan_Update(t *testing.T) {
	t.Parallel()

	crews := covRoute{body: `[{"id":"c1","slug":"eng"}]`}

	remote := &RecurringIssueRemote{
		ID:           "r1",
		Name:         "Daily",
		Slug:         "daily",
		Enabled:      true,
		Cron:         "0 9 * * *",
		Timezone:     "UTC",
		TemplateJSON: `{"title":"OLD","crew_id":"c1"}`,
	}

	t.Run("template json drift triggers update", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/crews":                 crews,
			"PATCH /api/v1/recurring-issues/r1": {status: 200, body: "{}"},
		})
		items, err := recurringIssueCovDoc().Plan(context.Background(), c, remote)
		if err != nil || len(items) != 1 || items[0].Action != internalapi.ActionUpdate {
			t.Fatalf("plan: items=%v err=%v", items, err)
		}
		if execErr := items[0].Exec(context.Background(), c); execErr != nil {
			t.Fatalf("exec: %v", execErr)
		}
		if !c.sawCall("PATCH /api/v1/recurring-issues/r1") {
			t.Fatalf("calls = %v", c.calls)
		}
	})
	t.Run("patch transport error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/crews":                 crews,
			"PATCH /api/v1/recurring-issues/r1": {err: errors.New("down")},
		})
		items, _ := recurringIssueCovDoc().Plan(context.Background(), c, remote)
		if execErr := items[0].Exec(context.Background(), c); execErr == nil || !strings.Contains(execErr.Error(), `update recurring_issue "daily"`) {
			t.Fatalf("exec: got %v", execErr)
		}
	})
	t.Run("patch bad status", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/crews":                 crews,
			"PATCH /api/v1/recurring-issues/r1": {status: 500, body: "err"},
		})
		items, _ := recurringIssueCovDoc().Plan(context.Background(), c, remote)
		if execErr := items[0].Exec(context.Background(), c); execErr == nil || !strings.Contains(execErr.Error(), "server returned 500") {
			t.Fatalf("exec: got %v", execErr)
		}
	})
}

// ── Export ──────────────────────────────────────────────────────────

func TestRecurringIssueCov_Export(t *testing.T) {
	t.Parallel()

	listBody := `[{"id":"r1","name":"Daily","slug":"daily","enabled":true,"cron":"0 9 * * *","timezone":"UTC","template_json":"{\"title\":\"T\",\"crew_id\":\"c1\",\"project_id\":\"p1\",\"assignee_agent_id\":\"a1\",\"label_ids\":[\"l1\"]}"}]`

	t.Run("transport error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/recurring-issues": {err: errors.New("down")}})
		_, err := ExportRecurringIssues(context.Background(), c)
		if err == nil || !strings.Contains(err.Error(), "list recurring_issues") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("status error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/recurring-issues": {status: 500}})
		_, err := ExportRecurringIssues(context.Background(), c)
		if err == nil || !strings.Contains(err.Error(), "read recurring_issues list") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("null body returns nil", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/recurring-issues": {body: "null"}})
		docs, err := ExportRecurringIssues(context.Background(), c)
		if docs != nil || err != nil {
			t.Fatalf("got (%v,%v)", docs, err)
		}
	})
	t.Run("decode error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/recurring-issues": {body: "{"}})
		_, err := ExportRecurringIssues(context.Background(), c)
		if err == nil || !strings.Contains(err.Error(), "decode recurring_issues list") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("empty array returns nil", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/recurring-issues": {body: "[]"}})
		docs, err := ExportRecurringIssues(context.Background(), c)
		if docs != nil || err != nil {
			t.Fatalf("got (%v,%v)", docs, err)
		}
	})
	t.Run("crew lookup fails", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/recurring-issues": {body: listBody},
			"GET /api/v1/crews":            {err: errors.New("down")},
		})
		_, err := ExportRecurringIssues(context.Background(), c)
		if err == nil || !strings.Contains(err.Error(), "build crew lookup") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("project lookup fails", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/recurring-issues": {body: listBody},
			"GET /api/v1/crews":            {body: "[]"},
			"GET /api/v1/projects":         {status: 500},
		})
		_, err := ExportRecurringIssues(context.Background(), c)
		if err == nil || !strings.Contains(err.Error(), "build project lookup") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("agent lookup fails", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/recurring-issues": {body: listBody},
			"GET /api/v1/crews":            {body: "[]"},
			"GET /api/v1/projects":         {body: "[]"},
			"GET /api/v1/agents":           {body: "not json"},
		})
		_, err := ExportRecurringIssues(context.Background(), c)
		if err == nil || !strings.Contains(err.Error(), "build agent lookup") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("label lookup fails", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/recurring-issues": {body: listBody},
			"GET /api/v1/crews":            {body: "[]"},
			"GET /api/v1/projects":         {body: "[]"},
			"GET /api/v1/agents":           {body: "[]"},
			"GET /api/v1/labels":           {status: 500},
		})
		_, err := ExportRecurringIssues(context.Background(), c)
		if err == nil || !strings.Contains(err.Error(), "build label lookup") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("template decode error", func(t *testing.T) {
		t.Parallel()
		badTemplate := `[{"id":"r1","name":"Daily","slug":"daily","template_json":"{broken"}]`
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/recurring-issues": {body: badTemplate},
			"GET /api/v1/crews":            {body: "[]"},
			"GET /api/v1/projects":         {body: "[]"},
			"GET /api/v1/agents":           {body: "[]"},
			"GET /api/v1/labels":           {body: "[]"},
		})
		_, err := ExportRecurringIssues(context.Background(), c)
		if err == nil || !strings.Contains(err.Error(), `decode template_json for "daily"`) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("happy round-trip", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/recurring-issues": {body: listBody},
			"GET /api/v1/crews":            {body: `[{"id":"c1","slug":"eng"}]`},
			"GET /api/v1/projects":         {body: `[{"id":"p1","slug":"road"}]`},
			"GET /api/v1/agents":           {body: `[{"id":"a1","slug":"eva"}]`},
			"GET /api/v1/labels":           {body: `[{"id":"l1","name":"bug"}]`},
		})
		docs, err := ExportRecurringIssues(context.Background(), c)
		if err != nil || len(docs) != 1 {
			t.Fatalf("got (%v,%v)", docs, err)
		}
		d := docs[0]
		if d.Spec.Template.CrewSlug != "eng" || d.Spec.Template.ProjectSlug != "road" ||
			d.Spec.Template.AssigneeAgentSlug != "eva" {
			t.Fatalf("template = %+v", d.Spec.Template)
		}
		if len(d.Spec.Template.Labels) != 1 || d.Spec.Template.Labels[0] != "bug" {
			t.Fatalf("labels = %v", d.Spec.Template.Labels)
		}
		if d.Spec.Enabled == nil || !*d.Spec.Enabled {
			t.Fatalf("enabled = %v", d.Spec.Enabled)
		}
	})
}

// ── listIDToSlug ────────────────────────────────────────────────────

func TestRecurringIssueCov_ListIDToSlug(t *testing.T) {
	t.Parallel()

	t.Run("transport error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {err: errors.New("down")}})
		if _, err := listIDToSlug(context.Background(), c, "/api/v1/crews"); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("status error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {status: 500}})
		if _, err := listIDToSlug(context.Background(), c, "/api/v1/crews"); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("null body", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {body: "null"}})
		m, err := listIDToSlug(context.Background(), c, "/api/v1/crews")
		if err != nil || len(m) != 0 {
			t.Fatalf("got (%v,%v)", m, err)
		}
	})
	t.Run("decode error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {body: "7"}})
		_, err := listIDToSlug(context.Background(), c, "/api/v1/crews")
		if err == nil || !strings.Contains(err.Error(), "decode list at /api/v1/crews") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("slug preferred name fallback empty id skipped", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/crews": {body: `[{"id":"x1","slug":"s","name":"n"},{"id":"x2","name":"only-name"},{"slug":"no-id"}]`}})
		m, err := listIDToSlug(context.Background(), c, "/api/v1/crews")
		if err != nil {
			t.Fatalf("listIDToSlug: %v", err)
		}
		if m["x1"] != "s" || m["x2"] != "only-name" || len(m) != 2 {
			t.Fatalf("map = %v", m)
		}
	})
}
