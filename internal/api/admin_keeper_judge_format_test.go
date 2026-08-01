package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/keepercfg"
)

// The probe's job is to answer "will credential decisions work against this
// model", and it can only answer that if it asks the way the credential path
// asks. think:false was the first half of that (see the _think_ file); the
// verdict schema is the second.
//
// Without it the probe measures a harder question than production poses: the
// model has to volunteer well-formed JSON from a prose instruction. A model that
// answers "Sure — {"decision":"ALLOW"}" fails stage 3 here while judging fine in
// production, and the operator is told to go find a larger model.
//
// These pin the request shape at all three sites that probe a judge, not the
// verdict — the verdict belongs to the model.

// assertVerdictSchema checks that a captured Ollama request body carries the
// verdict schema top-level, in the shape the gatekeeper's parser expects back.
func assertVerdictSchema(t *testing.T, raw []byte) {
	t.Helper()
	if len(raw) == 0 {
		t.Fatal("the probe never called /api/chat")
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode probe body: %v\n%s", err, raw)
	}
	format, present := got["format"]
	if !present {
		t.Fatalf("probe sent no format schema, so it measures a harder question than the judge poses: %s", raw)
	}
	schema, ok := format.(map[string]any)
	if !ok {
		t.Fatalf("format = %T, want the verdict schema object: %s", format, raw)
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("format carries no properties: %s", raw)
	}
	for _, field := range []string{"decision", "reason", "risk"} {
		if _, ok := props[field]; !ok {
			t.Errorf("schema is missing %q, which the gatekeeper's parser reads: %s", field, raw)
		}
	}
	decision, _ := props["decision"].(map[string]any)
	enum, _ := decision["enum"].([]any)
	if len(enum) != 3 {
		t.Fatalf("decision has no three-way enum, so the model may still invent a verdict: %s", raw)
	}
	want := map[string]bool{"ALLOW": true, "DENY": true, "ESCALATE": true}
	for _, v := range enum {
		s, _ := v.(string)
		if !want[s] {
			t.Errorf("decision enum contains %q, which NormalizeRawResponse maps to DENY", s)
		}
		delete(want, s)
	}
	if len(want) != 0 {
		t.Errorf("decision enum is missing %v — a verdict the judge can reach must be reachable here: %s", want, raw)
	}
	// Top-level, not buried in options where Ollama ignores it and the probe
	// would silently go back to measuring the harder question.
	if opts, ok := got["options"].(map[string]any); ok {
		if _, leaked := opts["format"]; leaked {
			t.Errorf("format landed in options, where it is ignored: %s", raw)
		}
	}
}

// The local four-stage check (POST /admin/keeper/judge/test).
func TestJudgeTest_ProbeConstrainsTheVerdictSchema(t *testing.T) {
	srv, lastBody := recordingOllama(t, []string{"qwen3.5:9b"},
		`{"decision":"ALLOW","reason":"routine release publish","risk":2}`)

	resp := runJudgeTest(t, `{"judge_endpoint_url":"`+srv.URL+`","judge_model":"qwen3.5:9b"}`)
	if !resp.OK {
		t.Fatalf("probe not OK: %+v", resp.Stages)
	}
	assertVerdictSchema(t, lastBody())
}

// The hosted check (POST /admin/keeper/judge/test-hosted). An "ollama" provider
// with no credential resolves to the server's default judge endpoint, which is
// the one hosted path this test can point at a recording server — the schema is
// set on the request, not on the provider, so the site is the thing under test.
func TestJudgeTestHosted_ProbeConstrainsTheVerdictSchema(t *testing.T) {
	srv, lastBody := recordingOllama(t, []string{"qwen3.5:9b"},
		`{"decision":"ALLOW","reason":"routine release publish","risk":2}`)
	db := setupTestDB(t)
	h := NewAdminKeeperJudgeHandler(nil, newTestLogger()).
		WithGovJudge(NewGovModelResolver(db, nil, newTestLogger(), srv.URL, "qwen3.5:9b"))

	req := hostedTestReq(`{"provider":"ollama","model":"qwen3.5:9b"}`)
	req = req.WithContext(withWorkspace(req.Context(), "ws1", "OWNER"))
	rr := httptest.NewRecorder()
	h.TestHosted(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rr.Code, rr.Body.String())
	}
	assertVerdictSchema(t, lastBody())
}

// The per-evaluator probe behind the Judge models card (ProbeModel). Same bar:
// an evaluator slot is judged by whether it answers the way the judge asks.
func TestProbeModel_ConstrainsTheVerdictSchema(t *testing.T) {
	srv, lastBody := recordingOllama(t, []string{"qwen3.5:9b"},
		`{"decision":"DENY","reason":"no bound credential","risk":8}`)
	h := NewAdminKeeperJudgeHandler(
		keepercfg.New(nil, keepercfg.Defaults{EndpointURL: srv.URL, Model: "qwen3.5:9b"}),
		newTestLogger())

	req := httptest.NewRequest("POST", "/api/v1/admin/keeper/aux/probe", nil)
	req = req.WithContext(withWorkspace(req.Context(), "ws1", "OWNER"))
	rr := httptest.NewRecorder()
	h.ProbeModel(rr, req, keepercfg.ProviderOllama, "qwen3.5:9b")
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rr.Code, rr.Body.String())
	}
	assertVerdictSchema(t, lastBody())
}
