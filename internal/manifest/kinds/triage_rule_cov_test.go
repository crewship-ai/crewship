package kinds

// Coverage-focused tests for triage_rule.go. Reuses the scriptable
// covClient fake from routine_cov_test.go. triage_rule_test.go owns
// the broad Validate/Plan flows; this file pins buildPostBody shape,
// the equalsRemote matrix, Plan exec error paths, Export decode
// failures, and the drain/read helpers.

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

func triageCovDoc() *TriageRuleDocument {
	return &TriageRuleDocument{
		APIVersion: "crewship/v1",
		Kind:       "TriageRule",
		Metadata:   internalapi.Metadata{Name: "Route bugs", Slug: "route-bugs"},
		Spec: TriageRuleSpec{
			Enabled:  true,
			Priority: 0, // exercise the effectivePriority default
			Match:    TriageMatch{TitleContains: []string{"bug"}},
			Actions:  TriageActions{SetPriority: "high"},
		},
	}
}

// ── buildPostBody ───────────────────────────────────────────────────

func TestTriageRuleCov_BuildPostBody(t *testing.T) {
	t.Parallel()

	body, err := triageCovDoc().buildPostBody()
	if err != nil {
		t.Fatalf("buildPostBody: %v", err)
	}
	if body["name"] != "Route bugs" || body["slug"] != "route-bugs" {
		t.Fatalf("body = %v", body)
	}
	if body["enabled"] != true {
		t.Fatalf("enabled = %v", body["enabled"])
	}
	if body["priority"] != 100 {
		t.Fatalf("zero priority must default to 100, got %v", body["priority"])
	}
	mj, _ := body["match_json"].(string)
	if !strings.Contains(mj, `"title_contains":["bug"]`) {
		t.Fatalf("match_json = %q", mj)
	}
	aj, _ := body["actions_json"].(string)
	if !strings.Contains(aj, `"set_priority":"high"`) {
		t.Fatalf("actions_json = %q", aj)
	}

	doc := triageCovDoc()
	doc.Spec.Priority = 7
	body, err = doc.buildPostBody()
	if err != nil || body["priority"] != 7 {
		t.Fatalf("explicit priority: got (%v,%v)", body["priority"], err)
	}
}

// ── equalsRemote ────────────────────────────────────────────────────

func TestTriageRuleCov_EqualsRemote(t *testing.T) {
	t.Parallel()

	matching := func() *TriageRuleRemote {
		return &TriageRuleRemote{
			ID:          "tr1",
			Name:        "Route bugs",
			Enabled:     true,
			Priority:    100,
			MatchJSON:   `{"title_contains":["bug"]}`,
			ActionsJSON: `{"set_priority":"high"}`,
		}
	}

	doc := triageCovDoc()

	t.Run("nil remote", func(t *testing.T) {
		t.Parallel()
		eq, err := doc.equalsRemote(nil)
		if eq || err != nil {
			t.Fatalf("got (%t,%v)", eq, err)
		}
	})
	t.Run("equal", func(t *testing.T) {
		t.Parallel()
		eq, err := doc.equalsRemote(matching())
		if !eq || err != nil {
			t.Fatalf("got (%t,%v)", eq, err)
		}
	})
	t.Run("empty remote json treated as zero values", func(t *testing.T) {
		t.Parallel()
		r := matching()
		r.MatchJSON = "  "
		eq, err := doc.equalsRemote(r)
		if eq || err != nil {
			t.Fatalf("zero match vs declared match must drift: got (%t,%v)", eq, err)
		}
	})

	mutations := map[string]func(*TriageRuleRemote){
		"name differs":            func(r *TriageRuleRemote) { r.Name = "Other" },
		"enabled differs":         func(r *TriageRuleRemote) { r.Enabled = false },
		"priority differs":        func(r *TriageRuleRemote) { r.Priority = 5 },
		"corrupt match json":      func(r *TriageRuleRemote) { r.MatchJSON = "{broken" },
		"corrupt actions json":    func(r *TriageRuleRemote) { r.ActionsJSON = "{broken" },
		"match content differs":   func(r *TriageRuleRemote) { r.MatchJSON = `{"title_contains":["feature"]}` },
		"actions content differs": func(r *TriageRuleRemote) { r.ActionsJSON = `{"set_priority":"low"}` },
	}
	for name, mutate := range mutations {
		mutate := mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := matching()
			mutate(r)
			eq, err := doc.equalsRemote(r)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if eq {
				t.Fatal("mutation must produce drift")
			}
		})
	}
}

// ── Plan exec paths ─────────────────────────────────────────────────

func TestTriageRuleCov_Plan_Exec(t *testing.T) {
	t.Parallel()

	t.Run("create transport error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"POST /api/v1/triage-rules": {err: errors.New("down")}})
		items, err := triageCovDoc().Plan(context.Background(), c, nil)
		if err != nil || len(items) != 1 || items[0].Action != internalapi.ActionCreate {
			t.Fatalf("plan: items=%v err=%v", items, err)
		}
		if execErr := items[0].Exec(context.Background(), c); execErr == nil || !strings.Contains(execErr.Error(), "create triage rule") {
			t.Fatalf("exec: got %v", execErr)
		}
	})
	t.Run("create accepts 201", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"POST /api/v1/triage-rules": {status: 201, body: "{}"}})
		items, _ := triageCovDoc().Plan(context.Background(), c, nil)
		if execErr := items[0].Exec(context.Background(), c); execErr != nil {
			t.Fatalf("exec: %v", execErr)
		}
	})
	t.Run("create rejects 400", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"POST /api/v1/triage-rules": {status: 400, body: "bad"}})
		items, _ := triageCovDoc().Plan(context.Background(), c, nil)
		if execErr := items[0].Exec(context.Background(), c); execErr == nil || !strings.Contains(execErr.Error(), "unexpected status 400") {
			t.Fatalf("exec: got %v", execErr)
		}
	})
	t.Run("update transport error", func(t *testing.T) {
		t.Parallel()
		remote := &TriageRuleRemote{ID: "tr1", Name: "Route bugs", Enabled: true, Priority: 5}
		c := newCovClient(map[string]covRoute{"PATCH /api/v1/triage-rules/tr1": {err: errors.New("down")}})
		items, err := triageCovDoc().Plan(context.Background(), c, remote)
		if err != nil || len(items) != 1 || items[0].Action != internalapi.ActionUpdate {
			t.Fatalf("plan: items=%v err=%v", items, err)
		}
		if execErr := items[0].Exec(context.Background(), c); execErr == nil || !strings.Contains(execErr.Error(), "update triage rule") {
			t.Fatalf("exec: got %v", execErr)
		}
	})
	t.Run("update ok", func(t *testing.T) {
		t.Parallel()
		remote := &TriageRuleRemote{ID: "tr1", Name: "Route bugs", Enabled: true, Priority: 5}
		c := newCovClient(map[string]covRoute{"PATCH /api/v1/triage-rules/tr1": {status: 200, body: "{}"}})
		items, _ := triageCovDoc().Plan(context.Background(), c, remote)
		if execErr := items[0].Exec(context.Background(), c); execErr != nil {
			t.Fatalf("exec: %v", execErr)
		}
		if !c.sawCall("PATCH /api/v1/triage-rules/tr1") {
			t.Fatalf("calls = %v", c.calls)
		}
	})
}

// ── Export ──────────────────────────────────────────────────────────

func TestTriageRuleCov_Export(t *testing.T) {
	t.Parallel()

	t.Run("transport error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/triage-rules": {err: errors.New("down")}})
		_, err := ExportTriageRules(context.Background(), c)
		if err == nil || !strings.Contains(err.Error(), "list triage rules") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("status error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/triage-rules": {status: 500, body: "boom"}})
		_, err := ExportTriageRules(context.Background(), c)
		if err == nil || !strings.Contains(err.Error(), "unexpected status 500") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("decode error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{"GET /api/v1/triage-rules": {body: "{bad"}})
		_, err := ExportTriageRules(context.Background(), c)
		if err == nil || !strings.Contains(err.Error(), "decode triage rules list") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("malformed match and actions tolerated", func(t *testing.T) {
		t.Parallel()
		body := `[{"id":"tr1","name":"Weird Rule!","enabled":true,"priority":3,"match_json":"{broken","actions_json":"{broken"}]`
		c := newCovClient(map[string]covRoute{"GET /api/v1/triage-rules": {body: body}})
		docs, err := ExportTriageRules(context.Background(), c)
		if err != nil || len(docs) != 1 {
			t.Fatalf("got (%v,%v)", docs, err)
		}
		if !docs[0].Spec.Match.IsEmpty() {
			t.Fatalf("corrupt match must stay zero: %+v", docs[0].Spec.Match)
		}
		if docs[0].Metadata.Slug != "weird-rule" {
			t.Fatalf("slug = %q", docs[0].Metadata.Slug)
		}
	})
	t.Run("happy round-trip", func(t *testing.T) {
		t.Parallel()
		body := `[{"id":"tr1","name":"Route bugs","enabled":true,"priority":100,"match_json":"{\"title_contains\":[\"bug\"]}","actions_json":"{\"set_priority\":\"high\"}"}]`
		c := newCovClient(map[string]covRoute{"GET /api/v1/triage-rules": {body: body}})
		docs, err := ExportTriageRules(context.Background(), c)
		if err != nil || len(docs) != 1 {
			t.Fatalf("got (%v,%v)", docs, err)
		}
		d := docs[0]
		if len(d.Spec.Match.TitleContains) != 1 || d.Spec.Match.TitleContains[0] != "bug" {
			t.Fatalf("match = %+v", d.Spec.Match)
		}
		if d.Spec.Actions.SetPriority != "high" {
			t.Fatalf("actions = %+v", d.Spec.Actions)
		}
	})
}

// ── helpers ─────────────────────────────────────────────────────────

// triageCovClosingBody wraps a Reader and records Close calls so the
// drain helpers' Closer branch is observable.
type triageCovClosingBody struct {
	io.Reader
	closed bool
}

func (b *triageCovClosingBody) Close() error {
	b.closed = true
	return nil
}

func TestTriageRuleCov_DrainAndCheck(t *testing.T) {
	t.Parallel()

	if err := triageRuleDrainAndCheck(nil, 200); err == nil || !strings.Contains(err.Error(), "nil response") {
		t.Fatalf("nil: got %v", err)
	}

	body := &triageCovClosingBody{Reader: strings.NewReader("payload")}
	if err := triageRuleDrainAndCheck(&internalapi.Response{StatusCode: 200, Body: body}, 200, 201); err != nil {
		t.Fatalf("accepted: got %v", err)
	}
	if !body.closed {
		t.Fatal("closer body must be closed")
	}

	err := triageRuleDrainAndCheck(&internalapi.Response{StatusCode: 500, Body: strings.NewReader("x")}, 200)
	if err == nil || !strings.Contains(err.Error(), "unexpected status 500") {
		t.Fatalf("rejected: got %v", err)
	}
}

func TestTriageRuleCov_ReadAllAndCheck(t *testing.T) {
	t.Parallel()

	if _, err := triageRuleReadAllAndCheck(nil, 200); err == nil || !strings.Contains(err.Error(), "nil response") {
		t.Fatalf("nil: got %v", err)
	}

	if _, err := triageRuleReadAllAndCheck(&internalapi.Response{StatusCode: 200, Body: covErrReader{}}, 200); err == nil || !strings.Contains(err.Error(), "read body") {
		t.Fatalf("read failure: got %v", err)
	}

	body := &triageCovClosingBody{Reader: strings.NewReader(`[{"id":"x"}]`)}
	b, err := triageRuleReadAllAndCheck(&internalapi.Response{StatusCode: 200, Body: body}, 200)
	if err != nil || string(b) != `[{"id":"x"}]` {
		t.Fatalf("happy: got (%q,%v)", b, err)
	}
	if !body.closed {
		t.Fatal("closer body must be closed")
	}

	long := strings.Repeat("e", 300)
	b, err = triageRuleReadAllAndCheck(&internalapi.Response{StatusCode: 500, Body: strings.NewReader(long)}, 200)
	if err == nil || !strings.Contains(err.Error(), "unexpected status 500") || !strings.Contains(err.Error(), "...") {
		t.Fatalf("status error: got %v", err)
	}
	if string(b) != long {
		t.Fatalf("body must be returned alongside error (got %d bytes)", len(b))
	}
}

func TestTriageRuleCov_FirstBytes(t *testing.T) {
	t.Parallel()

	if got := triageRuleFirstBytes([]byte("short"), 200); got != "short" {
		t.Fatalf("short: %q", got)
	}
	long := strings.Repeat("a", 250)
	got := triageRuleFirstBytes([]byte(long), 200)
	if len(got) != 203 || !strings.HasSuffix(got, "...") {
		t.Fatalf("long: len=%d suffix=%q", len(got), got[len(got)-5:])
	}
}

// ── Validate joined problems ────────────────────────────────────────

func TestTriageRuleCov_Validate_JoinsAllProblems(t *testing.T) {
	t.Parallel()

	doc := &TriageRuleDocument{
		Metadata: internalapi.Metadata{}, // missing slug + name
		Spec: TriageRuleSpec{
			Match: TriageMatch{FromAgentSlug: "ghost-agent", FromCrewSlug: "ghost-crew"},
			Actions: TriageActions{
				AddLabels:           []string{"", "ghost-label"},
				AssignToProjectSlug: "ghost-project",
				AssignToAgentSlug:   "ghost-agent",
			},
		},
	}
	err := doc.Validate(internalapi.WorkspaceContext{})
	if err == nil {
		t.Fatal("want error")
	}
	for _, fragment := range []string{
		"metadata.slug is required",
		"metadata.name is required",
		"add_labels contains an empty slug",
		`unknown label slug "ghost-label"`,
		`unknown project slug "ghost-project"`,
		`assign_to_agent_slug references unknown agent slug "ghost-agent"`,
		`from_agent_slug references unknown agent slug "ghost-agent"`,
		`from_crew_slug references unknown crew slug "ghost-crew"`,
	} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("error missing %q:\n%v", fragment, err)
		}
	}
}
