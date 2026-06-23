package kinds

// Coverage-focused tests for routine.go plus the shared scriptable
// fake client (covClient) reused by the other *_cov_test.go files in
// this package. The existing routine_test.go covers the happy paths;
// this file pins the error branches: transport failures, non-2xx
// statuses, malformed bodies, and the wrapped/flat response shapes.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// ── shared scriptable fake ───────────────────────────────────────────

// covRoute scripts one response for covClient keyed on "METHOD path".
type covRoute struct {
	status  int    // default 200
	body    string // raw response body
	err     error  // transport-level error (returned instead of a response)
	nilResp bool   // return (nil, nil)
	badBody bool   // body reader that fails mid-read
}

type covErrReader struct{}

func (covErrReader) Read([]byte) (int, error) {
	return 0, fmt.Errorf("simulated body read failure")
}

// covClient is a scriptable internalapi.Client. Unmatched routes get a
// 404 with a JSON error body, mirroring the real server's behaviour.
type covClient struct {
	ws     string
	routes map[string]covRoute
	calls  []string
}

func newCovClient(routes map[string]covRoute) *covClient {
	if routes == nil {
		routes = map[string]covRoute{}
	}
	return &covClient{ws: "ws_cov", routes: routes}
}

func (c *covClient) WorkspaceID() string { return c.ws }

func (c *covClient) do(method, path string) (*internalapi.Response, error) {
	key := method + " " + path
	c.calls = append(c.calls, key)
	r, ok := c.routes[key]
	if !ok {
		return &internalapi.Response{StatusCode: 404, Body: strings.NewReader(`{"error":"not found"}`)}, nil
	}
	if r.err != nil {
		return nil, r.err
	}
	if r.nilResp {
		return nil, nil
	}
	status := r.status
	if status == 0 {
		status = 200
	}
	// Body is always non-nil, mirroring net/http semantics (the real
	// adapter never hands kinds a nil Body). Nil-body tolerance is
	// tested directly against hand-built Response values instead.
	var body io.Reader = strings.NewReader(r.body)
	if r.badBody {
		body = covErrReader{}
	}
	return &internalapi.Response{StatusCode: status, Body: body}, nil
}

func (c *covClient) Get(_ context.Context, path string) (*internalapi.Response, error) {
	return c.do("GET", path)
}

func (c *covClient) Post(_ context.Context, path string, _ any) (*internalapi.Response, error) {
	return c.do("POST", path)
}

func (c *covClient) Patch(_ context.Context, path string, _ any) (*internalapi.Response, error) {
	return c.do("PATCH", path)
}

func (c *covClient) Put(_ context.Context, path string, _ any) (*internalapi.Response, error) {
	return c.do("PUT", path)
}

func (c *covClient) Delete(_ context.Context, path string) (*internalapi.Response, error) {
	return c.do("DELETE", path)
}

func (c *covClient) sawCall(key string) bool {
	for _, got := range c.calls {
		if got == key {
			return true
		}
	}
	return false
}

// ── defaults helpers ────────────────────────────────────────────────

func TestRoutineCov_WebhookRequireTokenOrDefault(t *testing.T) {
	t.Parallel()
	w := &RoutineWebhook{}
	if !w.RequireTokenOrDefault() {
		t.Error("nil RequireToken should default to true")
	}
	f := false
	w.RequireToken = &f
	if w.RequireTokenOrDefault() {
		t.Error("explicit false should return false")
	}
	tr := true
	w.RequireToken = &tr
	if !w.RequireTokenOrDefault() {
		t.Error("explicit true should return true")
	}
}

func TestRoutineCov_ScheduleEnabledOrDefault(t *testing.T) {
	t.Parallel()
	s := &RoutineSchedule{}
	if !s.EnabledOrDefault() {
		t.Error("nil Enabled should default to true")
	}
	f := false
	s.Enabled = &f
	if s.EnabledOrDefault() {
		t.Error("explicit false should return false")
	}
}

func TestRoutineCov_StepUnmarshalInvalidJSON(t *testing.T) {
	t.Parallel()
	var s RoutineStep
	if err := s.UnmarshalJSON([]byte(`{not json`)); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// ── readBodyChecked / jsonPost / jsonPatch / jsonDelete ─────────────

func TestRoutineCov_ReadBodyChecked(t *testing.T) {
	t.Parallel()

	if _, err := readBodyChecked(nil); err == nil || !strings.Contains(err.Error(), "nil response") {
		t.Errorf("nil response: got %v", err)
	}

	// 200 with nil body → no data, no error.
	data, err := readBodyChecked(&internalapi.Response{StatusCode: 200})
	if err != nil || data != nil {
		t.Errorf("200/nil body: data=%q err=%v", data, err)
	}

	// 500 with body → error carries status and body.
	_, err = readBodyChecked(&internalapi.Response{
		StatusCode: 500, Body: strings.NewReader("boom"),
	})
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("500: got %v", err)
	}

	// body read failure.
	_, err = readBodyChecked(&internalapi.Response{StatusCode: 200, Body: covErrReader{}})
	if err == nil || !strings.Contains(err.Error(), "read body") {
		t.Errorf("bad body: got %v", err)
	}

	// ReadCloser body gets closed without error.
	data, err = readBodyChecked(&internalapi.Response{
		StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true}`)),
	})
	if err != nil || string(data) != `{"ok":true}` {
		t.Errorf("ReadCloser: data=%q err=%v", data, err)
	}
}

func TestRoutineCov_JSONVerbHelpers(t *testing.T) {
	t.Parallel()
	transportErr := errors.New("conn refused")

	c := newCovClient(map[string]covRoute{
		"POST /p":     {err: transportErr},
		"PATCH /p":    {err: transportErr},
		"DELETE /ok":  {status: 204},
		"DELETE /bad": {status: 500, body: "delete denied"},
		"DELETE /err": {err: transportErr},
	})

	if _, err := jsonPost(context.Background(), c, "/p", nil); !errors.Is(err, transportErr) {
		t.Errorf("jsonPost transport error: got %v", err)
	}
	if _, err := jsonPatch(context.Background(), c, "/p", nil); !errors.Is(err, transportErr) {
		t.Errorf("jsonPatch transport error: got %v", err)
	}
	if err := jsonDelete(context.Background(), c, "/ok"); err != nil {
		t.Errorf("jsonDelete 204: got %v", err)
	}
	if err := jsonDelete(context.Background(), c, "/bad"); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("jsonDelete 500: got %v", err)
	}
	if err := jsonDelete(context.Background(), c, "/err"); !errors.Is(err, transportErr) {
		t.Errorf("jsonDelete transport error: got %v", err)
	}
}

// ── getRoutine / listRoutines ───────────────────────────────────────

func TestRoutineCov_GetRoutine(t *testing.T) {
	t.Parallel()
	base := "/api/v1/workspaces/ws_cov/pipelines/"
	c := newCovClient(map[string]covRoute{
		"GET " + base + "missing": {status: 404, body: `{"error":"nope"}`},
		"GET " + base + "broken":  {status: 500, body: "kaput"},
		"GET " + base + "garbage": {body: `not json`},
		"GET " + base + "good":    {body: `{"id":"p1","slug":"good","name":"Good"}`},
		"GET " + base + "noresp":  {err: errors.New("dial fail")},
	})

	if r, err := getRoutine(context.Background(), c, "ws_cov", "missing"); r != nil || err != nil {
		t.Errorf("404: r=%v err=%v, want nil/nil", r, err)
	}
	if _, err := getRoutine(context.Background(), c, "ws_cov", "broken"); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("500: got %v", err)
	}
	if _, err := getRoutine(context.Background(), c, "ws_cov", "garbage"); err == nil || !strings.Contains(err.Error(), "decode routine") {
		t.Errorf("garbage: got %v", err)
	}
	if _, err := getRoutine(context.Background(), c, "ws_cov", "noresp"); err == nil {
		t.Error("transport error: want non-nil error")
	}
	r, err := getRoutine(context.Background(), c, "ws_cov", "good")
	if err != nil || r == nil || r.Slug != "good" || r.Name != "Good" {
		t.Errorf("good: r=%+v err=%v", r, err)
	}
}

func TestRoutineCov_ListRoutines(t *testing.T) {
	t.Parallel()
	pipes := "/api/v1/workspaces/ws_cov/pipelines"

	t.Run("transport error", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + pipes: {err: errors.New("down")}})
		if _, err := listRoutines(context.Background(), c, "ws_cov"); err == nil {
			t.Fatal("want error")
		}
	})

	t.Run("status error", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + pipes: {status: 503, body: "busy"}})
		if _, err := listRoutines(context.Background(), c, "ws_cov"); err == nil || !strings.Contains(err.Error(), "HTTP 503") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("empty body", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + pipes: {body: ""}})
		out, err := listRoutines(context.Background(), c, "ws_cov")
		if err != nil || out != nil {
			t.Fatalf("out=%v err=%v", out, err)
		}
	})

	t.Run("wrapped shape with per-slug fetch", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{
			"GET " + pipes:         {body: `{"pipelines":[{"slug":"r1"}]}`},
			"GET " + pipes + "/r1": {body: `{"id":"p1","slug":"r1","name":"R1","definition":{"dsl_version":"1.0"}}`},
		})
		out, err := listRoutines(context.Background(), c, "ws_cov")
		if err != nil {
			t.Fatalf("err=%v", err)
		}
		if len(out) != 1 || out[0].Slug != "r1" || len(out[0].DefinitionJSON) == 0 {
			t.Fatalf("out=%+v", out)
		}
	})

	t.Run("wrapped shape get error", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{
			"GET " + pipes:         {body: `{"pipelines":[{"slug":"r1"}]}`},
			"GET " + pipes + "/r1": {err: errors.New("down")},
		})
		_, err := listRoutines(context.Background(), c, "ws_cov")
		if err == nil || !strings.Contains(err.Error(), "get routine r1") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("flat shape get error", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{
			"GET " + pipes:         {body: `[{"slug":"r1"}]`},
			"GET " + pipes + "/r1": {status: 500, body: "boom"},
		})
		_, err := listRoutines(context.Background(), c, "ws_cov")
		if err == nil || !strings.Contains(err.Error(), "get routine r1") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("flat shape per-slug 404 skipped", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{
			"GET " + pipes:         {body: `[{"slug":"r1"}]`},
			"GET " + pipes + "/r1": {status: 404, body: `{}`},
		})
		out, err := listRoutines(context.Background(), c, "ws_cov")
		if err != nil || len(out) != 0 {
			t.Fatalf("out=%v err=%v", out, err)
		}
	})

	t.Run("undecodable both shapes", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + pipes: {body: `{"pipelines":5}`}})
		_, err := listRoutines(context.Background(), c, "ws_cov")
		if err == nil || !strings.Contains(err.Error(), "decode pipelines list") {
			t.Fatalf("got %v", err)
		}
	})
}

// ── grouping helpers ────────────────────────────────────────────────

func TestRoutineCov_GroupSchedulesByPipeline(t *testing.T) {
	t.Parallel()
	path := "/api/v1/workspaces/ws_cov/pipeline-schedules"

	t.Run("404 → empty map", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {status: 404, body: `{}`}})
		out, err := groupSchedulesByPipeline(context.Background(), c, "ws_cov")
		if err != nil || len(out) != 0 {
			t.Fatalf("out=%v err=%v", out, err)
		}
	})
	t.Run("empty body → empty map", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {body: ""}})
		out, err := groupSchedulesByPipeline(context.Background(), c, "ws_cov")
		if err != nil || len(out) != 0 {
			t.Fatalf("out=%v err=%v", out, err)
		}
	})
	t.Run("decode error", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {body: `nope`}})
		if _, err := groupSchedulesByPipeline(context.Background(), c, "ws_cov"); err == nil || !strings.Contains(err.Error(), "decode schedules") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("500 error", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {status: 500, body: "x"}})
		if _, err := groupSchedulesByPipeline(context.Background(), c, "ws_cov"); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("transport error", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {err: errors.New("down")}})
		if _, err := groupSchedulesByPipeline(context.Background(), c, "ws_cov"); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("groups and sorts by name", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {body: `[
			{"id":"s2","name":"Zulu","target_pipeline_slug":"r1"},
			{"id":"s1","name":"Alpha","target_pipeline_slug":"r1"},
			{"id":"s3","name":"Solo","target_pipeline_slug":"r2"}
		]`}})
		out, err := groupSchedulesByPipeline(context.Background(), c, "ws_cov")
		if err != nil {
			t.Fatalf("err=%v", err)
		}
		if len(out["r1"]) != 2 || len(out["r2"]) != 1 {
			t.Fatalf("out=%v", out)
		}
		if out["r1"][0].Name != "Alpha" || out["r1"][1].Name != "Zulu" {
			t.Errorf("not sorted: %v", out["r1"])
		}
	})
}

func TestRoutineCov_GroupWebhooksByPipeline(t *testing.T) {
	t.Parallel()
	path := "/api/v1/workspaces/ws_cov/pipeline-webhooks"

	t.Run("404 → empty map", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {status: 404, body: `{}`}})
		out, err := groupWebhooksByPipeline(context.Background(), c, "ws_cov")
		if err != nil || len(out) != 0 {
			t.Fatalf("out=%v err=%v", out, err)
		}
	})
	t.Run("empty body → empty map", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {body: ""}})
		out, err := groupWebhooksByPipeline(context.Background(), c, "ws_cov")
		if err != nil || len(out) != 0 {
			t.Fatalf("out=%v err=%v", out, err)
		}
	})
	t.Run("decode error", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {body: `nope`}})
		if _, err := groupWebhooksByPipeline(context.Background(), c, "ws_cov"); err == nil || !strings.Contains(err.Error(), "decode webhooks") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("transport error", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {err: errors.New("down")}})
		if _, err := groupWebhooksByPipeline(context.Background(), c, "ws_cov"); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("last write wins per pipeline", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {body: `[
			{"id":"w1","target_pipeline_slug":"r1","enabled":false},
			{"id":"w2","target_pipeline_slug":"r1","enabled":true}
		]`}})
		out, err := groupWebhooksByPipeline(context.Background(), c, "ws_cov")
		if err != nil || len(out) != 1 || out["r1"].ID != "w2" {
			t.Fatalf("out=%v err=%v", out, err)
		}
	})
}

func TestRoutineCov_BuildCrewSlugMap(t *testing.T) {
	t.Parallel()
	path := "/api/v1/crews"

	t.Run("flat", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {body: `[{"id":"c1","slug":"alpha"}]`}})
		out, err := buildCrewSlugMap(context.Background(), c)
		if err != nil || out["c1"] != "alpha" {
			t.Fatalf("out=%v err=%v", out, err)
		}
	})
	t.Run("wrapped", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {body: `{"crews":[{"id":"c1","slug":"alpha"}]}`}})
		out, err := buildCrewSlugMap(context.Background(), c)
		if err != nil || out["c1"] != "alpha" {
			t.Fatalf("out=%v err=%v", out, err)
		}
	})
	t.Run("404 → empty", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {status: 404, body: `{}`}})
		out, err := buildCrewSlugMap(context.Background(), c)
		if err != nil || len(out) != 0 {
			t.Fatalf("out=%v err=%v", out, err)
		}
	})
	t.Run("empty body → empty", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {body: ""}})
		out, err := buildCrewSlugMap(context.Background(), c)
		if err != nil || len(out) != 0 {
			t.Fatalf("out=%v err=%v", out, err)
		}
	})
	t.Run("500 error", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {status: 500, body: "x"}})
		if _, err := buildCrewSlugMap(context.Background(), c); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("transport error", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {err: errors.New("down")}})
		if _, err := buildCrewSlugMap(context.Background(), c); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("undecodable both shapes", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {body: `{"crews":5}`}})
		if _, err := buildCrewSlugMap(context.Background(), c); err == nil || !strings.Contains(err.Error(), "decode crews") {
			t.Fatalf("got %v", err)
		}
	})
}

// ── ExportRoutines error branches ───────────────────────────────────

func TestRoutineCov_ExportRoutines_Errors(t *testing.T) {
	t.Parallel()
	pipes := "/api/v1/workspaces/ws_cov/pipelines"
	scheds := "/api/v1/workspaces/ws_cov/pipeline-schedules"
	hooks := "/api/v1/workspaces/ws_cov/pipeline-webhooks"

	t.Run("no workspace id", func(t *testing.T) {
		c := newCovClient(nil)
		c.ws = ""
		if _, err := ExportRoutines(context.Background(), c); err == nil || !strings.Contains(err.Error(), "workspace_id not set") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("list pipelines error", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + pipes: {status: 500, body: "x"}})
		if _, err := ExportRoutines(context.Background(), c); err == nil {
			t.Fatal("want error")
		}
	})

	t.Run("no pipelines → nil docs", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + pipes: {body: `[]`}})
		docs, err := ExportRoutines(context.Background(), c)
		if err != nil || docs != nil {
			t.Fatalf("docs=%v err=%v", docs, err)
		}
	})

	routes := func() map[string]covRoute {
		return map[string]covRoute{
			"GET " + pipes:         {body: `[{"slug":"r1"}]`},
			"GET " + pipes + "/r1": {body: `{"id":"p1","slug":"r1","name":"R1","description":"d","definition":{"dsl_version":"1.0","steps":[]},"author_crew_id":"c1"}`},
			"GET " + scheds:        {body: `[]`},
			"GET " + hooks:         {body: `[]`},
			"GET /api/v1/crews":    {body: `[{"id":"c1","slug":"alpha"}]`},
		}
	}

	t.Run("schedule list error", func(t *testing.T) {
		r := routes()
		r["GET "+scheds] = covRoute{err: errors.New("down")}
		c := newCovClient(r)
		if _, err := ExportRoutines(context.Background(), c); err == nil || !strings.Contains(err.Error(), "list schedules") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("webhook list error", func(t *testing.T) {
		r := routes()
		r["GET "+hooks] = covRoute{err: errors.New("down")}
		c := newCovClient(r)
		if _, err := ExportRoutines(context.Background(), c); err == nil || !strings.Contains(err.Error(), "list webhooks") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("crew list error", func(t *testing.T) {
		r := routes()
		r["GET /api/v1/crews"] = covRoute{err: errors.New("down")}
		c := newCovClient(r)
		if _, err := ExportRoutines(context.Background(), c); err == nil || !strings.Contains(err.Error(), "list crews") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("definition decode error", func(t *testing.T) {
		r := routes()
		r["GET "+pipes+"/r1"] = covRoute{body: `{"id":"p1","slug":"r1","name":"R1","definition":123}`}
		c := newCovClient(r)
		if _, err := ExportRoutines(context.Background(), c); err == nil || !strings.Contains(err.Error(), "decode routine r1 definition") {
			t.Fatalf("got %v", err)
		}
	})
}

// ── Plan error + webhook drift branches ─────────────────────────────

func TestRoutineCov_Plan_NoWorkspaceID(t *testing.T) {
	t.Parallel()
	doc := routineSampleDoc()
	c := newCovClient(nil)
	c.ws = ""
	if _, err := doc.Plan(context.Background(), c, nil); err == nil || !strings.Contains(err.Error(), "workspace_id not set") {
		t.Fatalf("got %v", err)
	}
}

func TestRoutineCov_Plan_ListSchedulesError(t *testing.T) {
	doc := routineSampleDoc()
	fake := newRoutineFakeClient(t)
	fake.listSchedulesErr = errors.New("schedules down")
	if _, err := doc.Plan(context.Background(), fake, nil); err == nil || !strings.Contains(err.Error(), "list schedules") {
		t.Fatalf("got %v", err)
	}
}

func TestRoutineCov_Plan_WebhookLoadError(t *testing.T) {
	doc := routineSampleDoc()
	fake := newRoutineFakeClient(t)
	fake.listWebhooksErr = errors.New("webhooks down")
	if _, err := doc.Plan(context.Background(), fake, nil); err == nil || !strings.Contains(err.Error(), "load webhook") {
		t.Fatalf("got %v", err)
	}
}

// Manifest no longer declares a webhook but the remote has one — Plan
// must emit a Delete item whose Exec actually removes the row.
func TestRoutineCov_Plan_WebhookDelete(t *testing.T) {
	doc := routineSampleDoc()
	doc.Spec.Webhook = nil

	fake := newRoutineFakeClient(t)
	defBytes, _ := json.Marshal(definitionJSONShape(&doc))
	fake.pipelines["discord-sync"] = &RoutineRemote{
		ID: "pipe_discord-sync", Slug: "discord-sync",
		Name: doc.Metadata.Name, Description: doc.Spec.Description,
		DefinitionJSON: defBytes,
	}
	fake.schedules["sched_0001"] = &ScheduleRemote{
		ID: "sched_0001", Name: "Hourly",
		TargetPipelineSlug: "discord-sync",
		CronExpr:           "0 * * * *", Timezone: "Europe/Prague",
		Enabled: true, Inputs: map[string]any{"channels": "all"},
	}
	fake.webhooks["hook_0001"] = &WebhookRemote{
		ID: "hook_0001", Name: "discord-sync",
		TargetPipelineSlug: "discord-sync", Enabled: true,
	}

	items, err := doc.Plan(context.Background(), fake, fake.pipelines["discord-sync"])
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	var del *internalapi.PlanItem
	for i := range items {
		if items[i].Kind == "webhook" && items[i].Action == internalapi.ActionDelete {
			del = &items[i]
		}
	}
	if del == nil {
		t.Fatalf("no webhook delete item in %+v", items)
	}
	if err := del.Exec(context.Background(), fake); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if len(fake.webhooks) != 0 {
		t.Errorf("webhook not deleted: %+v", fake.webhooks)
	}
}

// Declared webhook enabled, remote disabled — drift → Update whose
// Exec deletes then recreates the webhook.
func TestRoutineCov_Plan_WebhookUpdate(t *testing.T) {
	doc := routineSampleDoc()

	fake := newRoutineFakeClient(t)
	defBytes, _ := json.Marshal(definitionJSONShape(&doc))
	fake.pipelines["discord-sync"] = &RoutineRemote{
		ID: "pipe_discord-sync", Slug: "discord-sync",
		Name: doc.Metadata.Name, Description: doc.Spec.Description,
		DefinitionJSON: defBytes,
	}
	fake.schedules["sched_0001"] = &ScheduleRemote{
		ID: "sched_0001", Name: "Hourly",
		TargetPipelineSlug: "discord-sync",
		CronExpr:           "0 * * * *", Timezone: "Europe/Prague",
		Enabled: true, Inputs: map[string]any{"channels": "all"},
	}
	fake.webhooks["hook_9999"] = &WebhookRemote{
		ID: "hook_9999", Name: "discord-sync",
		TargetPipelineSlug: "discord-sync", Enabled: false, // drifted
	}

	items, err := doc.Plan(context.Background(), fake, fake.pipelines["discord-sync"])
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	var upd *internalapi.PlanItem
	for i := range items {
		if items[i].Kind == "webhook" {
			upd = &items[i]
		}
	}
	if upd == nil || upd.Action != internalapi.ActionUpdate {
		t.Fatalf("webhook item = %+v, want Update", upd)
	}
	if err := upd.Exec(context.Background(), fake); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	// Delete-then-recreate: old id gone, one webhook present, enabled.
	if _, stale := fake.webhooks["hook_9999"]; stale {
		t.Error("old webhook row should be deleted")
	}
	if len(fake.webhooks) != 1 {
		t.Fatalf("want exactly 1 webhook after update, got %d", len(fake.webhooks))
	}
	for _, w := range fake.webhooks {
		if !w.Enabled {
			t.Errorf("recreated webhook should be enabled: %+v", w)
		}
	}
}

// ── diff helpers ────────────────────────────────────────────────────

func TestRoutineCov_RoutineDiffers(t *testing.T) {
	t.Parallel()
	doc := routineSampleDoc()
	defBytes, _ := json.Marshal(definitionJSONShape(&doc))
	base := RoutineRemote{
		Name:           doc.Metadata.Name,
		Description:    doc.Spec.Description,
		DefinitionJSON: defBytes,
	}

	same := base
	if routineDiffers(&doc, &same) {
		t.Error("identical remote should not differ")
	}

	nameDrift := base
	nameDrift.Name = "Other"
	if !routineDiffers(&doc, &nameDrift) {
		t.Error("name drift should differ")
	}

	descDrift := base
	descDrift.Description = "stale"
	if !routineDiffers(&doc, &descDrift) {
		t.Error("description drift should differ")
	}

	emptyDef := base
	emptyDef.DefinitionJSON = nil
	if !routineDiffers(&doc, &emptyDef) {
		t.Error("empty remote definition should differ")
	}
}

func TestRoutineCov_ScheduleDiffers(t *testing.T) {
	t.Parallel()
	enabled := true
	declared := RoutineSchedule{
		Name: "Hourly", Cron: "0 * * * *", Timezone: "UTC",
		Enabled: &enabled, Inputs: map[string]any{"k": "v"},
	}
	remote := ScheduleRemote{
		Name: "Hourly", CronExpr: "0 * * * *", Timezone: "UTC",
		Enabled: true, Inputs: map[string]any{"k": "v"},
	}
	if scheduleDiffers(&declared, &remote) {
		t.Error("identical schedule should not differ")
	}

	tz := remote
	tz.Timezone = "Europe/Prague"
	if !scheduleDiffers(&declared, &tz) {
		t.Error("timezone drift should differ")
	}

	en := remote
	en.Enabled = false
	if !scheduleDiffers(&declared, &en) {
		t.Error("enabled drift should differ")
	}

	in := remote
	in.Inputs = map[string]any{"k": "other"}
	if !scheduleDiffers(&declared, &in) {
		t.Error("inputs drift should differ")
	}

	// nil inputs on both sides normalise to empty maps.
	d2 := declared
	d2.Inputs = nil
	r2 := remote
	r2.Inputs = nil
	if scheduleDiffers(&d2, &r2) {
		t.Error("nil inputs on both sides should be equal")
	}
}

func TestRoutineCov_WebhookDiffers(t *testing.T) {
	t.Parallel()
	if webhookDiffers(&RoutineWebhook{Enabled: true}, &WebhookRemote{Enabled: true}) {
		t.Error("same enabled state should not differ")
	}
	if !webhookDiffers(&RoutineWebhook{Enabled: true}, &WebhookRemote{Enabled: false}) {
		t.Error("enabled drift should differ")
	}
}

func TestRoutineCov_DefinitionJSONShape_AllOptionalFields(t *testing.T) {
	t.Parallel()
	doc := routineSampleDoc()
	doc.Spec.Inputs = []any{map[string]any{"name": "channels"}}
	doc.Spec.CredentialsRequired = []any{"DISCORD_TOKEN"}
	doc.Spec.EstimatedCostUSD = 0.5
	doc.Spec.EstimatedDurationSeconds = 60
	doc.Spec.MaxCostUSD = 2.0
	doc.Spec.EgressTargets = []string{"discord.com"}

	out := definitionJSONShape(&doc)
	for _, key := range []string{
		"inputs", "credentials_required", "estimated_cost_usd",
		"estimated_duration_seconds", "max_cost_usd", "egress_targets",
		"description", "dsl_version", "name", "steps",
	} {
		if _, ok := out[key]; !ok {
			t.Errorf("definition shape missing %q: %v", key, out)
		}
	}
}

func TestRoutineCov_JSONEqual_Invalid(t *testing.T) {
	t.Parallel()
	if jsonEqual([]byte(`not json`), []byte(`{}`)) {
		t.Error("invalid a should be unequal")
	}
	if jsonEqual([]byte(`{}`), []byte(`not json`)) {
		t.Error("invalid b should be unequal")
	}
	if !jsonEqual([]byte(`{"a":1,"b":2}`), []byte(`{"b":2,"a":1}`)) {
		t.Error("key order should not matter")
	}
}
