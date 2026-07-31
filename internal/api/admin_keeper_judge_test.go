package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/ratelimitcfg"
)

// A fake Ollama, because the point of the three-stage check is what it says when
// things are WRONG, and the wrong cases are the ones a live box cannot be asked
// to reproduce on demand: no models pulled, the configured model missing, a model
// that answers in prose. Each stage's failure has to name the fix.

type fakeOllamaOpts struct {
	// models is what /api/tags advertises.
	models []string
	// chatAnswer is the assistant content /api/chat returns.
	chatAnswer string
	// tagsStatus / chatStatus override the HTTP status (0 = 200).
	tagsStatus, chatStatus int
}

func fakeOllama(t *testing.T, opts fakeOllamaOpts) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, r *http.Request) {
		if opts.tagsStatus != 0 {
			w.WriteHeader(opts.tagsStatus)
			return
		}
		type m struct {
			Name string `json:"name"`
		}
		list := make([]m, 0, len(opts.models))
		for _, name := range opts.models {
			list = append(list, m{Name: name})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"models": list})
	})
	mux.HandleFunc("/api/chat", func(w http.ResponseWriter, r *http.Request) {
		if opts.chatStatus != 0 {
			w.WriteHeader(opts.chatStatus)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":   "fake",
			"message": map[string]any{"role": "assistant", "content": opts.chatAnswer},
			"done":    true,
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func judgeReq(t *testing.T, body string) *http.Request {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest("POST", "/api/v1/admin/keeper/judge/test", nil)
	} else {
		r = httptest.NewRequest("POST", "/api/v1/admin/keeper/judge/test", strings.NewReader(body))
	}
	ctx := context.WithValue(r.Context(), ctxRole, "OWNER")
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: "u-admin"})
	return r.WithContext(ctx)
}

func runJudgeTest(t *testing.T, body string) judgeTestResponse {
	t.Helper()
	h := NewAdminKeeperJudgeHandler(nil, newTestLogger())
	rr := httptest.NewRecorder()
	h.Test(rr, judgeReq(t, body))
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rr.Code, rr.Body.String())
	}
	var resp judgeTestResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v\n%s", err, rr.Body.String())
	}
	return resp
}

func stageByName(t *testing.T, resp judgeTestResponse, name string) judgeStage {
	t.Helper()
	for _, s := range resp.Stages {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("stage %q missing from %+v", name, resp.Stages)
	return judgeStage{}
}

func TestJudgeTest_AllThreeStagesPass(t *testing.T) {
	srv := fakeOllama(t, fakeOllamaOpts{
		models:     []string{"qwen2.5:7b", "nomic-embed-text:latest"},
		chatAnswer: `{"decision":"ALLOW","reason":"routine release publish","risk":2}`,
	})
	resp := runJudgeTest(t, `{"judge_endpoint_url":"`+srv.URL+`","judge_model":"qwen2.5:7b"}`)

	if !resp.OK {
		t.Fatalf("test not OK: %+v", resp.Stages)
	}
	for _, name := range []string{"reach", "model", "verdict"} {
		if s := stageByName(t, resp, name); !s.OK {
			t.Errorf("stage %s failed: %s", name, s.Detail)
		}
	}
	if resp.Decision != "ALLOW" {
		t.Errorf("decision = %q, want ALLOW", resp.Decision)
	}
	// The model list travels with the result so the UI can offer a picker from
	// the same round trip.
	if len(resp.Models) != 2 {
		t.Errorf("models = %v, want both advertised models", resp.Models)
	}
}

// The most common real failure: endpoint up, model never pulled. It has to be
// distinguishable from "endpoint down", and it has to name the command.
func TestJudgeTest_ModelNotPulled(t *testing.T) {
	srv := fakeOllama(t, fakeOllamaOpts{models: []string{"llama3:8b"}})
	resp := runJudgeTest(t, `{"judge_endpoint_url":"`+srv.URL+`","judge_model":"qwen2.5:7b"}`)

	if resp.OK {
		t.Fatal("reported OK with the configured model missing")
	}
	if s := stageByName(t, resp, "reach"); !s.OK {
		t.Errorf("stage reach should pass — the endpoint answered: %s", s.Detail)
	}
	model := stageByName(t, resp, "model")
	if model.OK {
		t.Error("stage model passed for a model the endpoint does not have")
	}
	if !strings.Contains(model.Detail, "ollama pull qwen2.5:7b") {
		t.Errorf("detail does not name the fix: %q", model.Detail)
	}
	// Stage 3 must be SKIPPED, not failed: there is nothing to smoke-test, and
	// reporting it as a failure would send the operator after the wrong problem.
	if v := stageByName(t, resp, "verdict"); !v.Skipped {
		t.Errorf("stage verdict should be skipped, got %+v", v)
	}
	// And the endpoint's real models are offered, so the answer is one click away.
	if len(resp.Models) != 1 || resp.Models[0] != "llama3:8b" {
		t.Errorf("models = %v, want the endpoint's own list", resp.Models)
	}
}

// The check that a ping cannot make: the model answers, but not with a verdict.
// In production that model DENYs every credential request.
func TestJudgeTest_ModelAnswersInProse(t *testing.T) {
	srv := fakeOllama(t, fakeOllamaOpts{
		models:     []string{"tiny:0.5b"},
		chatAnswer: "Sure! I think you should probably allow this request because it seems fine.",
	})
	resp := runJudgeTest(t, `{"judge_endpoint_url":"`+srv.URL+`","judge_model":"tiny:0.5b"}`)

	if resp.OK {
		t.Fatal("reported OK for a model that cannot produce a verdict")
	}
	verdict := stageByName(t, resp, "verdict")
	if verdict.OK || verdict.Skipped {
		t.Errorf("stage verdict should FAIL (not skip): %+v", verdict)
	}
	if !strings.Contains(strings.ToLower(verdict.Detail), "deny every request") {
		t.Errorf("detail does not explain the consequence: %q", verdict.Detail)
	}
}

func TestJudgeTest_EndpointUnreachable(t *testing.T) {
	// Port 1 on loopback: nothing listens, and the fence allows loopback so the
	// error is a real connection refusal rather than a policy block.
	resp := runJudgeTest(t, `{"judge_endpoint_url":"http://127.0.0.1:1","judge_model":"qwen2.5:7b"}`)

	if resp.OK {
		t.Fatal("reported OK against a dead port")
	}
	reach := stageByName(t, resp, "reach")
	if reach.OK {
		t.Error("stage reach passed against a dead port")
	}
	if !strings.Contains(reach.Detail, "OLLAMA_HOST") {
		t.Errorf("a refused connection should mention the bind address: %q", reach.Detail)
	}
	for _, name := range []string{"model", "verdict"} {
		if s := stageByName(t, resp, name); !s.Skipped {
			t.Errorf("stage %s should be skipped when nothing answered: %+v", name, s)
		}
	}
}

// The vantage-point trap from the docs: the same endpoint value serves the agent
// path, where host.docker.internal is correct, and the judge, which dials from
// the host where it is not.
func TestJudgeTest_ContainerHostnameGetsItsOwnMessage(t *testing.T) {
	resp := runJudgeTest(t, `{"judge_endpoint_url":"http://host.docker.internal:11434","judge_model":"x"}`)

	reach := stageByName(t, resp, "reach")
	if reach.OK {
		t.Fatalf("a container-only hostname must be refused before it is dialled: %+v", reach)
	}
	if !strings.Contains(reach.Detail, "only resolves inside containers") {
		t.Errorf("detail does not explain the vantage point: %q", reach.Detail)
	}
}

func TestJudgeTest_NoEndpointConfigured(t *testing.T) {
	resp := runJudgeTest(t, `{}`)

	if resp.OK {
		t.Fatal("reported OK with nothing configured")
	}
	if len(resp.Stages) != 1 || resp.Stages[0].Name != "reach" {
		t.Fatalf("want a single failed reach stage, got %+v", resp.Stages)
	}
	if !strings.Contains(resp.Stages[0].Detail, "no judge endpoint") {
		t.Errorf("detail = %q", resp.Stages[0].Detail)
	}
}

// The bucket is what keeps a configuration tool from doubling as a network
// scanner, so it has to actually bite — and say why when it does.
func TestJudgeTest_ProbesAreRateLimited(t *testing.T) {
	h := NewAdminKeeperJudgeHandler(nil, newTestLogger())
	perMin := ratelimitcfg.Int(ratelimitcfg.KeyKeeperJudgeProbe)

	// Drain the burst. Every call here fails at stage 1 (nothing configured),
	// which is fine: the limiter is consumed before any of that.
	for i := 0; i < perMin; i++ {
		rr := httptest.NewRecorder()
		h.Test(rr, judgeReq(t, `{}`))
		if rr.Code != http.StatusOK {
			t.Fatalf("probe %d got %d, want 200", i+1, rr.Code)
		}
	}
	rr := httptest.NewRecorder()
	h.Test(rr, judgeReq(t, `{}`))
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("probe %d got %d, want 429", perMin+1, rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "rate limited") {
		t.Errorf("the refusal does not explain itself: %s", rr.Body.String())
	}
}

// Discovery shares the bucket: two routes, one outbound-dial capability.
func TestJudgeModels_SharesTheProbeBudget(t *testing.T) {
	h := NewAdminKeeperJudgeHandler(nil, newTestLogger())
	perMin := ratelimitcfg.Int(ratelimitcfg.KeyKeeperJudgeProbe)

	for i := 0; i < perMin; i++ {
		rr := httptest.NewRecorder()
		h.Test(rr, judgeReq(t, `{}`))
		if rr.Code != http.StatusOK {
			t.Fatalf("probe %d got %d", i+1, rr.Code)
		}
	}
	req := httptest.NewRequest("GET", "/api/v1/admin/keeper/judge/models", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxRole, "OWNER"))
	rr := httptest.NewRecorder()
	h.Models(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Errorf("discovery got %d after the test budget was spent, want 429", rr.Code)
	}
}

func TestJudgeTest_RequiresManageRole(t *testing.T) {
	h := NewAdminKeeperJudgeHandler(nil, newTestLogger())
	req := httptest.NewRequest("POST", "/api/v1/admin/keeper/judge/test", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxRole, "MEMBER"))
	rr := httptest.NewRecorder()
	h.Test(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("member got %d, want 403", rr.Code)
	}
}

// ── Model discovery ─────────────────────────────────────────────────────────

func TestJudgeModels_ListsWhatTheEndpointServes(t *testing.T) {
	srv := fakeOllama(t, fakeOllamaOpts{models: []string{"qwen2.5:7b", "llama3:8b"}})
	h := NewAdminKeeperJudgeHandler(nil, newTestLogger())

	req := httptest.NewRequest("GET", "/api/v1/admin/keeper/judge/models?endpoint="+srv.URL, nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxRole, "OWNER"))
	rr := httptest.NewRecorder()
	h.Models(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rr.Code, rr.Body.String())
	}
	var resp judgeModelsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Models) != 2 {
		t.Errorf("models = %v", resp.Models)
	}
	if resp.Error != "" {
		t.Errorf("unexpected error: %q", resp.Error)
	}
}

// A picker that refreshes as the operator types must not turn "Ollama is not
// running yet" into a 500 in the console.
func TestJudgeModels_UnreachableIsAFieldError(t *testing.T) {
	h := NewAdminKeeperJudgeHandler(nil, newTestLogger())
	req := httptest.NewRequest("GET", "/api/v1/admin/keeper/judge/models?endpoint=http://127.0.0.1:1", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxRole, "OWNER"))
	rr := httptest.NewRecorder()
	h.Models(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 with an error field", rr.Code)
	}
	var resp judgeModelsResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Error == "" {
		t.Error("no error reported for an unreachable endpoint")
	}
	if len(resp.Models) != 0 {
		t.Errorf("models = %v, want none", resp.Models)
	}
}

// ── judgeRoot ───────────────────────────────────────────────────────────────

// Every shape an operator plausibly pastes has to reduce to the same root. This
// table is the reason "test green, production DENY" cannot happen through the
// endpoint field: the tested URL and the judged URL are derived identically.
func TestJudgeRoot(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"http://localhost:11434", "http://localhost:11434"},
		{"http://localhost:11434/", "http://localhost:11434"},
		{"  http://localhost:11434  ", "http://localhost:11434"},
		{"http://localhost:11434/v1", "http://localhost:11434"},
		{"http://localhost:11434/v1/", "http://localhost:11434"},
		{"http://localhost:11434/v1/chat/completions", "http://localhost:11434"},
		{"http://localhost:11434/api/chat", "http://localhost:11434"},
		{"http://localhost:11434/api/tags", "http://localhost:11434"},
		{"http://192.168.1.222:11434", "http://192.168.1.222:11434"},
		{"https://ollama.example.com", "https://ollama.example.com"},
		// url.Parse canonicalises the scheme, which is what we want stored.
		{"HTTP://localhost:11434", "http://localhost:11434"},
		{"http://[::1]:11434/v1", "http://[::1]:11434"},
		// A reverse proxy mounted under a path keeps it — stripping the mount
		// would send every request to the proxy's root.
		{"http://gw.example.com/ollama", "http://gw.example.com/ollama"},
		{"http://gw.example.com/ollama/v1", "http://gw.example.com/ollama"},
		// Scheme-less is what somebody copies out of `ollama serve`. The
		// canonical normalizer assumes http rather than refusing it — https
		// would simply fail to connect against a local daemon.
		{"localhost:11434", "http://localhost:11434"},
		{"192.168.1.222:11434", "http://192.168.1.222:11434"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			got, err := judgeRoot(tc.in)
			if err != nil {
				t.Fatalf("rejected %q: %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("judgeRoot(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestJudgeRoot_Rejects(t *testing.T) {
	for _, in := range []string{"", "   ", "ftp://host:11434", "http://", "http://u:p@host:11434"} {
		if _, err := judgeRoot(in); err == nil {
			t.Errorf("accepted %q", in)
		}
	}
}
