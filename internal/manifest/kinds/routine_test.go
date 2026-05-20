package kinds

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
	"gopkg.in/yaml.v3"
)

// routineFakeClient implements internalapi.Client against in-memory
// maps for pipelines, schedules, and webhooks. It records every call
// so tests can assert path + body. The compound nature of the Routine
// kind (one document → up to N+2 REST calls) means a fake that just
// returns canned JSON would obscure ordering bugs; recording every
// call instead lets tests pin the exact sequence.
type routineFakeClient struct {
	t                  *testing.T
	wsID               string
	pipelines          map[string]*RoutineRemote // keyed by slug
	schedules          map[string]*ScheduleRemote
	webhooks           map[string]*WebhookRemote
	crews              map[string]string // id -> slug
	calls              []routineFakeCall
	nextScheduleID     int
	nextWebhookID      int
	listSchedulesErr   error
	listWebhooksErr    error
	failOnSchedulePost bool
	failOnPipelineSave bool
}

type routineFakeCall struct {
	Method string
	Path   string
	Body   any
}

func newRoutineFakeClient(t *testing.T) *routineFakeClient {
	t.Helper()
	return &routineFakeClient{
		t:         t,
		wsID:      "ws_test",
		pipelines: map[string]*RoutineRemote{},
		schedules: map[string]*ScheduleRemote{},
		webhooks:  map[string]*WebhookRemote{},
		crews:     map[string]string{},
	}
}

func (f *routineFakeClient) WorkspaceID() string { return f.wsID }

func (f *routineFakeClient) record(method, path string, body any) {
	f.calls = append(f.calls, routineFakeCall{Method: method, Path: path, Body: body})
}

func (f *routineFakeClient) respond(status int, v any) *internalapi.Response {
	data, _ := json.Marshal(v)
	return &internalapi.Response{StatusCode: status, Body: bytes.NewReader(data)}
}

func (f *routineFakeClient) Get(_ context.Context, path string) (*internalapi.Response, error) {
	f.record("GET", path, nil)
	switch {
	case strings.HasSuffix(path, "/pipeline-schedules"):
		if f.listSchedulesErr != nil {
			return nil, f.listSchedulesErr
		}
		out := make([]ScheduleRemote, 0, len(f.schedules))
		for _, s := range f.schedules {
			out = append(out, *s)
		}
		return f.respond(200, out), nil
	case strings.HasSuffix(path, "/pipeline-webhooks"):
		if f.listWebhooksErr != nil {
			return nil, f.listWebhooksErr
		}
		out := make([]WebhookRemote, 0, len(f.webhooks))
		for _, w := range f.webhooks {
			out = append(out, *w)
		}
		return f.respond(200, out), nil
	case strings.HasSuffix(path, "/pipelines"):
		out := make([]RoutineRemote, 0, len(f.pipelines))
		for _, p := range f.pipelines {
			// List response omits definition (matches real handler).
			cp := *p
			cp.DefinitionJSON = nil
			out = append(out, cp)
		}
		return f.respond(200, out), nil
	case path == "/api/v1/crews":
		out := make([]map[string]string, 0, len(f.crews))
		for id, slug := range f.crews {
			out = append(out, map[string]string{"id": id, "slug": slug})
		}
		return f.respond(200, out), nil
	}
	// /pipelines/{slug}
	if strings.Contains(path, "/pipelines/") && !strings.Contains(path, "/save") {
		slug := path[strings.LastIndex(path, "/")+1:]
		if p, ok := f.pipelines[slug]; ok {
			return f.respond(200, p), nil
		}
		return f.respond(404, map[string]any{"error": "not found"}), nil
	}
	return f.respond(404, map[string]any{"error": "not found"}), nil
}

func (f *routineFakeClient) Post(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("POST", path, body)
	b, _ := body.(map[string]any)
	switch {
	case strings.HasSuffix(path, "/pipelines/save"):
		if f.failOnPipelineSave {
			return f.respond(500, map[string]any{"error": "boom"}), nil
		}
		slug, _ := b["slug"].(string)
		name, _ := b["name"].(string)
		desc, _ := b["description"].(string)
		defAny := b["definition"]
		defBytes, _ := json.Marshal(defAny)
		f.pipelines[slug] = &RoutineRemote{
			ID:             "pipe_" + slug,
			Slug:           slug,
			Name:           name,
			Description:    desc,
			DefinitionJSON: defBytes,
			AuthorCrewID:   "crew_default",
		}
		return f.respond(201, f.pipelines[slug]), nil
	case strings.HasSuffix(path, "/pipeline-schedules"):
		if f.failOnSchedulePost {
			return f.respond(500, map[string]any{"error": "boom"}), nil
		}
		f.nextScheduleID++
		name, _ := b["name"].(string)
		cronExpr, _ := b["cron_expr"].(string)
		tz, _ := b["timezone"].(string)
		enabled, _ := b["enabled"].(bool)
		slug, _ := b["target_pipeline_slug"].(string)
		inputs, _ := b["inputs"].(map[string]any)
		sched := &ScheduleRemote{
			ID:                 routineStringID("sched", f.nextScheduleID),
			Name:               name,
			TargetPipelineID:   "pipe_" + slug,
			TargetPipelineSlug: slug,
			CronExpr:           cronExpr,
			Timezone:           tz,
			Enabled:            enabled,
			Inputs:             inputs,
		}
		f.schedules[sched.ID] = sched
		return f.respond(201, sched), nil
	case strings.HasSuffix(path, "/pipeline-webhooks"):
		f.nextWebhookID++
		name, _ := b["name"].(string)
		slug, _ := b["target_pipeline_slug"].(string)
		enabled, _ := b["enabled"].(bool)
		wh := &WebhookRemote{
			ID:                 routineStringID("hook", f.nextWebhookID),
			Name:               name,
			TargetPipelineID:   "pipe_" + slug,
			TargetPipelineSlug: slug,
			Enabled:            enabled,
		}
		f.webhooks[wh.ID] = wh
		return f.respond(201, wh), nil
	}
	return f.respond(404, map[string]any{"error": "not found"}), nil
}

func (f *routineFakeClient) Patch(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("PATCH", path, body)
	return f.respond(200, body), nil
}
func (f *routineFakeClient) Put(_ context.Context, path string, body any) (*internalapi.Response, error) {
	f.record("PUT", path, body)
	return f.respond(200, body), nil
}
func (f *routineFakeClient) Delete(_ context.Context, path string) (*internalapi.Response, error) {
	f.record("DELETE", path, nil)
	// Drop the row from whichever map matches the path so subsequent
	// list calls reflect the deletion (used by Plan-then-Exec tests).
	if strings.Contains(path, "/pipeline-schedules/") {
		id := path[strings.LastIndex(path, "/")+1:]
		delete(f.schedules, id)
	}
	if strings.Contains(path, "/pipeline-webhooks/") {
		id := path[strings.LastIndex(path, "/")+1:]
		delete(f.webhooks, id)
	}
	return f.respond(204, nil), nil
}

func routineStringID(prefix string, n int) string {
	digits := ""
	if n == 0 {
		digits = "0"
	}
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	for len(digits) < 4 {
		digits = "0" + digits
	}
	return prefix + "_" + digits
}

// routineSampleDoc returns a fully-specified RoutineDocument so every
// test shallow-copies and tweaks instead of rebuilding from scratch.
// The shape matches the Discord-sync example from the spec.
func routineSampleDoc() RoutineDocument {
	enabled := true
	return RoutineDocument{
		APIVersion: "crewship/v1",
		Kind:       "Routine",
		Metadata: internalapi.Metadata{
			Name: "Discord hourly sync",
			Slug: "discord-sync",
			Labels: map[string]string{
				"crew": "uo-outlands",
			},
		},
		Spec: RoutineSpec{
			DSLVersion:  "1.0",
			Description: "Hourly Discord pull + LLM summary",
			Steps: []RoutineStep{
				{
					ID:        "summarize",
					Type:      "agent_run",
					AgentSlug: "trapper",
					Rest:      map[string]any{"prompt": "summarize"},
				},
			},
			Schedules: []RoutineSchedule{
				{
					Name:     "Hourly",
					Cron:     "0 * * * *",
					Timezone: "Europe/Prague",
					Enabled:  &enabled,
					Inputs:   map[string]any{"channels": "all"},
				},
			},
			Webhook: &RoutineWebhook{
				Enabled:     true,
				TokenEnvRef: "DISCORD_WEBHOOK_TOKEN",
			},
		},
	}
}

// routineSampleCtx returns a WorkspaceContext that satisfies the
// sample doc's FK references (parent crew + step agent).
func routineSampleCtx() internalapi.WorkspaceContext {
	return internalapi.WorkspaceContext{
		DeclaredCrews:  []internalapi.SlugLookup{{Slug: "uo-outlands", Name: "Ultima Outlands"}},
		DeclaredAgents: []internalapi.SlugLookup{{Slug: "trapper", Name: "Trapper"}},
	}
}

// ── 1. Parse round-trip ──────────────────────────────────────────────

func TestRoutine_ParseRoundTrip(t *testing.T) {
	yamlIn := `apiVersion: crewship/v1
kind: Routine
metadata:
  name: Discord hourly sync
  slug: discord-sync
  labels:
    crew: uo-outlands
spec:
  dsl_version: "1.0"
  description: Hourly Discord pull + LLM summary
  steps:
    - id: summarize
      type: agent_run
      agent_slug: trapper
      prompt: summarize
  schedules:
    - name: Hourly
      cron: "0 * * * *"
      timezone: Europe/Prague
      enabled: true
      inputs:
        channels: all
  webhook:
    enabled: true
    token_env_ref: DISCORD_WEBHOOK_TOKEN
`
	var doc RoutineDocument
	if err := yaml.Unmarshal([]byte(yamlIn), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Metadata.Slug != "discord-sync" {
		t.Errorf("slug = %q", doc.Metadata.Slug)
	}
	if doc.Metadata.Labels["crew"] != "uo-outlands" {
		t.Errorf("crew label = %q", doc.Metadata.Labels["crew"])
	}
	if len(doc.Spec.Steps) != 1 || doc.Spec.Steps[0].AgentSlug != "trapper" {
		t.Errorf("steps = %+v", doc.Spec.Steps)
	}
	if len(doc.Spec.Schedules) != 1 || doc.Spec.Schedules[0].Cron != "0 * * * *" {
		t.Errorf("schedules = %+v", doc.Spec.Schedules)
	}
	if doc.Spec.Webhook == nil || !doc.Spec.Webhook.Enabled {
		t.Errorf("webhook = %+v", doc.Spec.Webhook)
	}

	out, err := yaml.Marshal(&doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var rt RoutineDocument
	if err := yaml.Unmarshal(out, &rt); err != nil {
		t.Fatalf("re-unmarshal: %v", err)
	}
	if rt.Metadata.Slug != doc.Metadata.Slug ||
		len(rt.Spec.Steps) != len(doc.Spec.Steps) ||
		len(rt.Spec.Schedules) != len(doc.Spec.Schedules) {
		t.Errorf("round-trip mismatch:\n  before=%+v\n  after =%+v", doc, rt)
	}
}

// ── 2. Validate happy path ───────────────────────────────────────────

func TestRoutine_Validate_HappyPath(t *testing.T) {
	doc := routineSampleDoc()
	if err := doc.Validate(routineSampleCtx()); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

// ── 3. Validate error paths ──────────────────────────────────────────

func TestRoutine_Validate_Errors(t *testing.T) {
	enabled := true
	cases := []struct {
		name        string
		mutate      func(*RoutineDocument)
		ctx         internalapi.WorkspaceContext
		wantContain string
	}{
		{
			name:        "missing crew label",
			mutate:      func(d *RoutineDocument) { delete(d.Metadata.Labels, "crew") },
			ctx:         routineSampleCtx(),
			wantContain: "metadata.labels.crew required",
		},
		{
			name:        "crew not in workspace",
			mutate:      func(d *RoutineDocument) { d.Metadata.Labels["crew"] = "ghost-crew" },
			ctx:         routineSampleCtx(),
			wantContain: `"ghost-crew" not found`,
		},
		{
			name:        "step agent_slug unknown",
			mutate:      func(d *RoutineDocument) { d.Spec.Steps[0].AgentSlug = "ghost-agent" },
			ctx:         routineSampleCtx(),
			wantContain: `"ghost-agent" not found`,
		},
		{
			name:        "missing dsl_version",
			mutate:      func(d *RoutineDocument) { d.Spec.DSLVersion = "" },
			ctx:         routineSampleCtx(),
			wantContain: "spec.dsl_version required",
		},
		{
			name:        "no steps",
			mutate:      func(d *RoutineDocument) { d.Spec.Steps = nil },
			ctx:         routineSampleCtx(),
			wantContain: "at least one step",
		},
		{
			name: "bad cron",
			mutate: func(d *RoutineDocument) {
				d.Spec.Schedules[0].Cron = "not a cron"
			},
			ctx:         routineSampleCtx(),
			wantContain: "cron invalid",
		},
		{
			name: "bad timezone",
			mutate: func(d *RoutineDocument) {
				d.Spec.Schedules[0].Timezone = "Mars/Olympus_Mons"
			},
			ctx:         routineSampleCtx(),
			wantContain: "timezone invalid",
		},
		{
			name: "duplicate schedule names",
			mutate: func(d *RoutineDocument) {
				d.Spec.Schedules = append(d.Spec.Schedules, RoutineSchedule{
					Name: "Hourly", Cron: "0 * * * *", Timezone: "UTC", Enabled: &enabled,
				})
			},
			ctx:         routineSampleCtx(),
			wantContain: "duplicates schedules",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := routineSampleDoc()
			tc.mutate(&doc)
			err := doc.Validate(tc.ctx)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantContain) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantContain)
			}
		})
	}
}

// ── 4. Plan: create (no remote) ──────────────────────────────────────

func TestRoutine_Plan_Create(t *testing.T) {
	doc := routineSampleDoc()
	fake := newRoutineFakeClient(t)

	items, err := doc.Plan(context.Background(), fake, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	// Expect 3 items: routine create + schedule create + webhook create.
	wantActions := []internalapi.PlanAction{
		internalapi.ActionCreate, // routine
		internalapi.ActionCreate, // schedule
		internalapi.ActionCreate, // webhook
	}
	if len(items) != len(wantActions) {
		t.Fatalf("expected %d items, got %d: %+v", len(wantActions), len(items), items)
	}
	for i, want := range wantActions {
		if items[i].Action != want {
			t.Errorf("item[%d].Action = %v, want %v (%s)", i, items[i].Action, want, items[i].Kind)
		}
		if items[i].Exec == nil {
			t.Errorf("item[%d] missing Exec (Action=%v)", i, items[i].Action)
		}
	}
	if items[0].Kind != "routine" || items[1].Kind != "schedule" || items[2].Kind != "webhook" {
		t.Errorf("kind ordering wrong: %s %s %s", items[0].Kind, items[1].Kind, items[2].Kind)
	}

	// Run them in order and verify the POST bodies hitting the right
	// endpoints.
	for _, it := range items {
		if err := it.Exec(context.Background(), fake); err != nil {
			t.Fatalf("Exec %s: %v", it.Slug, err)
		}
	}
	if len(fake.pipelines) != 1 || fake.pipelines["discord-sync"] == nil {
		t.Errorf("pipeline not created: %+v", fake.pipelines)
	}
	if len(fake.schedules) != 1 {
		t.Errorf("schedule not created: %+v", fake.schedules)
	}
	if len(fake.webhooks) != 1 {
		t.Errorf("webhook not created: %+v", fake.webhooks)
	}

	// Verify the pipeline POST body has the slug + name injected.
	var savedBody map[string]any
	for _, c := range fake.calls {
		if c.Method == "POST" && strings.HasSuffix(c.Path, "/pipelines/save") {
			savedBody, _ = c.Body.(map[string]any)
		}
	}
	if savedBody == nil {
		t.Fatal("no pipelines/save POST recorded")
	}
	if savedBody["slug"] != "discord-sync" || savedBody["name"] != "Discord hourly sync" {
		t.Errorf("save body bad: %+v", savedBody)
	}
	def, ok := savedBody["definition"].(map[string]any)
	if !ok {
		t.Fatalf("definition missing or wrong type: %T", savedBody["definition"])
	}
	if def["name"] != "discord-sync" {
		t.Errorf("definition.name should equal slug, got %v", def["name"])
	}
	if _, hasSchedules := def["schedules"]; hasSchedules {
		t.Error("DSL definition must NOT include schedules (those go to /pipeline-schedules)")
	}
	if _, hasWebhook := def["webhook"]; hasWebhook {
		t.Error("DSL definition must NOT include webhook (those go to /pipeline-webhooks)")
	}
}

// ── 5. Plan: update (drifted remote) ─────────────────────────────────

func TestRoutine_Plan_Update(t *testing.T) {
	doc := routineSampleDoc()
	doc.Spec.Description = "Hourly Discord pull + LLM summary"

	fake := newRoutineFakeClient(t)
	// Seed a drifted remote: same slug + name, but the definition's
	// description is stale.
	staleDef := map[string]any{
		"dsl_version": "1.0",
		"name":        "discord-sync",
		"description": "STALE — different from manifest",
		"steps": []any{
			map[string]any{
				"id": "summarize", "type": "agent_run",
				"agent_slug": "trapper", "prompt": "summarize",
			},
		},
	}
	defBytes, _ := json.Marshal(staleDef)
	remote := &RoutineRemote{
		ID:             "pipe_discord-sync",
		Slug:           "discord-sync",
		Name:           "Discord hourly sync",
		Description:    "STALE — different from manifest",
		DefinitionJSON: defBytes,
	}

	items, err := doc.Plan(context.Background(), fake, remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(items) == 0 || items[0].Kind != "routine" {
		t.Fatalf("first item should be routine, got %+v", items)
	}
	if items[0].Action != internalapi.ActionUpdate {
		t.Errorf("routine action = %v, want Update", items[0].Action)
	}
}

// ── 6. Plan: unchanged ───────────────────────────────────────────────

func TestRoutine_Plan_Unchanged(t *testing.T) {
	doc := routineSampleDoc()

	// Seed remote so the routine + schedule + webhook all match.
	fake := newRoutineFakeClient(t)
	defBytes, _ := json.Marshal(definitionJSONShape(&doc))
	fake.pipelines["discord-sync"] = &RoutineRemote{
		ID:             "pipe_discord-sync",
		Slug:           "discord-sync",
		Name:           doc.Metadata.Name,
		Description:    doc.Spec.Description,
		DefinitionJSON: defBytes,
	}
	fake.schedules["sched_0001"] = &ScheduleRemote{
		ID:                 "sched_0001",
		Name:               "Hourly",
		TargetPipelineSlug: "discord-sync",
		CronExpr:           "0 * * * *",
		Timezone:           "Europe/Prague",
		Enabled:            true,
		Inputs:             map[string]any{"channels": "all"},
	}
	fake.webhooks["hook_0001"] = &WebhookRemote{
		ID:                 "hook_0001",
		Name:               "discord-sync",
		TargetPipelineSlug: "discord-sync",
		Enabled:            true,
	}

	remote := fake.pipelines["discord-sync"]
	items, err := doc.Plan(context.Background(), fake, remote)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for _, it := range items {
		if it.Action != internalapi.ActionUnchanged {
			t.Errorf("%s.%s = %v, want Unchanged", it.Kind, it.Slug, it.Action)
		}
		if it.Exec != nil {
			t.Errorf("%s.%s Unchanged item must have nil Exec", it.Kind, it.Slug)
		}
	}
}

// ── 7. Plan: drops missing schedules ────────────────────────────────
//
// Specific to compound kinds: when the manifest declares fewer
// schedules than the remote has, the missing ones should be
// Action=Delete.

func TestRoutine_Plan_DropsMissingSchedule(t *testing.T) {
	doc := routineSampleDoc()
	// Manifest declares the Hourly schedule only. Remote has Hourly +
	// a stale "Nightly" schedule that should be dropped.

	fake := newRoutineFakeClient(t)
	defBytes, _ := json.Marshal(definitionJSONShape(&doc))
	fake.pipelines["discord-sync"] = &RoutineRemote{
		ID:             "pipe_discord-sync",
		Slug:           "discord-sync",
		Name:           doc.Metadata.Name,
		Description:    doc.Spec.Description,
		DefinitionJSON: defBytes,
	}
	fake.schedules["sched_0001"] = &ScheduleRemote{
		ID: "sched_0001", Name: "Hourly",
		TargetPipelineSlug: "discord-sync",
		CronExpr:           "0 * * * *",
		Timezone:           "Europe/Prague",
		Enabled:            true,
		Inputs:             map[string]any{"channels": "all"},
	}
	fake.schedules["sched_0002"] = &ScheduleRemote{
		ID: "sched_0002", Name: "Nightly",
		TargetPipelineSlug: "discord-sync",
		CronExpr:           "0 3 * * *",
		Timezone:           "Europe/Prague",
		Enabled:            true,
		Inputs:             map[string]any{},
	}
	fake.webhooks["hook_0001"] = &WebhookRemote{
		ID: "hook_0001", Name: "discord-sync",
		TargetPipelineSlug: "discord-sync", Enabled: true,
	}

	items, err := doc.Plan(context.Background(), fake, fake.pipelines["discord-sync"])
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	var sawDelete bool
	for _, it := range items {
		if it.Kind == "schedule" && it.Action == internalapi.ActionDelete {
			sawDelete = true
			if !strings.Contains(it.Slug, "Nightly") {
				t.Errorf("delete slug = %q, want suffix Nightly", it.Slug)
			}
		}
	}
	if !sawDelete {
		t.Errorf("expected one schedule delete plan item, got %+v", items)
	}
}

// ── 8. Plan: compound create — routine + 2 schedules + 1 webhook ────
//
// The flagship test: one document, four PlanItems, all Create. This
// exercises the multi-emit shape that makes Routine the biggest kind
// in the system.

func TestRoutine_Plan_CompoundCreate(t *testing.T) {
	doc := routineSampleDoc()
	enabled := true
	// Add a second schedule so we get the multi-schedule compound shape.
	doc.Spec.Schedules = append(doc.Spec.Schedules, RoutineSchedule{
		Name: "Nightly", Cron: "0 3 * * *", Timezone: "Europe/Prague",
		Enabled: &enabled,
	})

	fake := newRoutineFakeClient(t)
	items, err := doc.Plan(context.Background(), fake, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	// 1 routine + 2 schedules + 1 webhook = 4 plan items.
	if len(items) != 4 {
		t.Fatalf("expected 4 items, got %d:\n%+v", len(items), items)
	}
	kinds := make([]string, len(items))
	for i, it := range items {
		kinds[i] = it.Kind
		if it.Action != internalapi.ActionCreate {
			t.Errorf("item %d (%s.%s) action = %v, want Create", i, it.Kind, it.Slug, it.Action)
		}
	}
	// Routine must come first (schedules + webhook FK back to it).
	if kinds[0] != "routine" {
		t.Errorf("first item must be routine, got order: %v", kinds)
	}
	// All schedule items group together, webhook is last.
	if kinds[3] != "webhook" {
		t.Errorf("webhook must come after schedules, got order: %v", kinds)
	}

	// Exec them all and confirm the right side-effects happened.
	for _, it := range items {
		if err := it.Exec(context.Background(), fake); err != nil {
			t.Fatalf("Exec %s.%s: %v", it.Kind, it.Slug, err)
		}
	}
	if len(fake.schedules) != 2 {
		t.Errorf("expected 2 schedules created, got %d", len(fake.schedules))
	}
	if len(fake.webhooks) != 1 {
		t.Errorf("expected 1 webhook created, got %d", len(fake.webhooks))
	}
}

// ── 9. Plan: schedule diff drops missing — already covered above; this
//     case verifies the inverse direction: declared but drifted cron
//     produces an Update on the schedule, not a delete+recreate.

func TestRoutine_Plan_ScheduleCronDrift(t *testing.T) {
	doc := routineSampleDoc()

	fake := newRoutineFakeClient(t)
	defBytes, _ := json.Marshal(definitionJSONShape(&doc))
	fake.pipelines["discord-sync"] = &RoutineRemote{
		ID: "pipe_discord-sync", Slug: "discord-sync",
		Name: doc.Metadata.Name, Description: doc.Spec.Description,
		DefinitionJSON: defBytes,
	}
	// Remote schedule has the SAME name but a different cron — the
	// manifest is authoritative so this is a drift → Update.
	fake.schedules["sched_0001"] = &ScheduleRemote{
		ID: "sched_0001", Name: "Hourly",
		TargetPipelineSlug: "discord-sync",
		CronExpr:           "*/15 * * * *", // drifted
		Timezone:           "Europe/Prague",
		Enabled:            true,
		Inputs:             map[string]any{"channels": "all"},
	}
	fake.webhooks["hook_0001"] = &WebhookRemote{
		ID: "hook_0001", Name: "discord-sync",
		TargetPipelineSlug: "discord-sync", Enabled: true,
	}

	items, err := doc.Plan(context.Background(), fake, fake.pipelines["discord-sync"])
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	var schedItem *internalapi.PlanItem
	for i, it := range items {
		if it.Kind == "schedule" && strings.HasSuffix(it.Slug, "Hourly") {
			schedItem = &items[i]
		}
	}
	if schedItem == nil {
		t.Fatalf("no schedule item for Hourly in %+v", items)
	}
	if schedItem.Action != internalapi.ActionUpdate {
		t.Errorf("schedule action = %v, want Update", schedItem.Action)
	}

	// Exec the update and confirm a PATCH hit the right ID.
	if err := schedItem.Exec(context.Background(), fake); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	var sawPatch bool
	for _, c := range fake.calls {
		if c.Method == "PATCH" && strings.Contains(c.Path, "pipeline-schedules/sched_0001") {
			sawPatch = true
		}
	}
	if !sawPatch {
		t.Error("expected PATCH to /pipeline-schedules/sched_0001")
	}
}

// ── 10. Export round-trip ────────────────────────────────────────────

func TestRoutine_Export_RoundTrip(t *testing.T) {
	fake := newRoutineFakeClient(t)
	fake.crews["crew_default"] = "uo-outlands"

	// Build the same DSL shape the manifest would produce on save, so
	// the round-trip is content-equivalent.
	doc := routineSampleDoc()
	defBytes, _ := json.Marshal(definitionJSONShape(&doc))
	fake.pipelines["discord-sync"] = &RoutineRemote{
		ID:             "pipe_discord-sync",
		Slug:           "discord-sync",
		Name:           doc.Metadata.Name,
		Description:    doc.Spec.Description,
		DefinitionJSON: defBytes,
		AuthorCrewID:   "crew_default",
	}
	fake.schedules["sched_0001"] = &ScheduleRemote{
		ID: "sched_0001", Name: "Hourly",
		TargetPipelineSlug: "discord-sync",
		CronExpr:           "0 * * * *",
		Timezone:           "Europe/Prague",
		Enabled:            true,
		Inputs:             map[string]any{"channels": "all"},
	}
	fake.webhooks["hook_0001"] = &WebhookRemote{
		ID: "hook_0001", Name: "discord-sync",
		TargetPipelineSlug: "discord-sync", Enabled: true,
	}

	docs, err := ExportRoutines(context.Background(), fake)
	if err != nil {
		t.Fatalf("ExportRoutines: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 doc, got %d", len(docs))
	}
	got := docs[0]
	if got.Metadata.Slug != "discord-sync" {
		t.Errorf("exported slug = %q", got.Metadata.Slug)
	}
	if got.Metadata.Labels["crew"] != "uo-outlands" {
		t.Errorf("exported crew label = %q", got.Metadata.Labels["crew"])
	}
	if len(got.Spec.Steps) == 0 || got.Spec.Steps[0].AgentSlug != "trapper" {
		t.Errorf("exported steps lost agent_slug: %+v", got.Spec.Steps)
	}
	if len(got.Spec.Schedules) != 1 || got.Spec.Schedules[0].Cron != "0 * * * *" {
		t.Errorf("exported schedules wrong: %+v", got.Spec.Schedules)
	}
	if got.Spec.Webhook == nil || !got.Spec.Webhook.Enabled {
		t.Errorf("exported webhook wrong: %+v", got.Spec.Webhook)
	}

	// YAML round-trip on the exported document — the result must still
	// pass Validate against a context that knows the parent crew +
	// agent.
	out, err := yaml.Marshal(got)
	if err != nil {
		t.Fatalf("marshal exported doc: %v", err)
	}
	var rt RoutineDocument
	if err := yaml.Unmarshal(out, &rt); err != nil {
		t.Fatalf("unmarshal exported yaml: %v", err)
	}
	if err := rt.Validate(routineSampleCtx()); err != nil {
		t.Errorf("exported doc failed Validate: %v", err)
	}
}
