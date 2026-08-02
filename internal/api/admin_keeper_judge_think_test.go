package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/keepercfg"
)

// recordingOllama is fakeOllama with the chat request body kept, so a test can
// assert what the probe ASKED for and not only what it did with the answer.
func recordingOllama(t *testing.T, models []string, answer string) (*httptest.Server, func() []byte) {
	t.Helper()
	var (
		mu   sync.Mutex
		last []byte
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, _ *http.Request) {
		type m struct {
			Name string `json:"name"`
		}
		list := make([]m, 0, len(models))
		for _, name := range models {
			list = append(list, m{Name: name})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"models": list})
	})
	mux.HandleFunc("/api/chat", func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		mu.Lock()
		last = buf
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":   "fake",
			"message": map[string]any{"role": "assistant", "content": answer},
			"done":    true,
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, func() []byte {
		mu.Lock()
		defer mu.Unlock()
		return last
	}
}

// The judge check exists to answer "will credential decisions work against this
// model". It can only answer that if it asks the way the credential path asks.
//
// The gatekeeper suppresses reasoning (think:false) because its 256-token budget
// has no room for a chain of thought. A probe that leaves thinking ON measures a
// different call: against a live qwen3.5:9b it spent 13.2s producing reasoning
// and no verdict, and reported "this model would deny every request" about a
// model that answers correctly in 3.4s when asked the way production asks.
//
// A check that fails a working judge is the same class of bug as one that passes
// a broken judge, and this file guards the shape rather than the verdict.
func TestJudgeTest_ProbeSuppressesThinkingLikeTheGatekeeper(t *testing.T) {
	srv, lastBody := recordingOllama(t, []string{"qwen3.5:9b"},
		`{"decision":"ALLOW","reason":"routine release publish","risk":2}`)

	resp := runJudgeTest(t, `{"judge_endpoint_url":"`+srv.URL+`","judge_model":"qwen3.5:9b"}`)
	if !resp.OK {
		t.Fatalf("probe not OK: %+v", resp.Stages)
	}

	body := string(lastBody())
	if body == "" {
		t.Fatal("the probe never called /api/chat")
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode probe body: %v\n%s", err, body)
	}
	think, present := got["think"]
	if !present {
		t.Fatalf("probe sent no think flag, so it measures a different call than the judge makes: %s", body)
	}
	if think != false {
		t.Errorf("think = %v, want false", think)
	}
	// Top-level, not buried in options where Ollama ignores it.
	if opts, ok := got["options"].(map[string]any); ok {
		if _, leaked := opts["think"]; leaked {
			t.Errorf("think landed in options, where it is ignored: %s", body)
		}
	}
	if !strings.Contains(body, `"stream":false`) {
		t.Errorf("probe should not stream: %s", body)
	}
}

// The first call to a model server after an idle period pays a COLD LOAD —
// ~6GB of weights before the first token. On the reference deployment that is
// 20s against a 20s budget, so stage 4 fails and the check reports the judge as
// too slow. The same check seconds later measures 2.7s.
//
// Hit three times in one afternoon: a fresh `crewship seed` left the watchdog
// off, the escalation harness skipped its whole suite in preflight, and a manual
// run reported a working judge as unusable. Each of those is a false negative in
// a situation that is not rare — it is what every first check looks like.
//
// suggestBudget's own comment already assumed "the measurement is one warm
// call". It was not. So when the budget stage fails, the verdict is measured
// once more against a now-resident model, and THAT is the number reported.
// runJudgeTestWithBudget drives the probe against a handler whose judge budget
// is short enough to test the cold-load path without sleeping for the default.
func runJudgeTestWithBudget(t *testing.T, body string, budget time.Duration) judgeTestResponse {
	t.Helper()
	db := setupTestDB(t)
	store := keepercfg.New(db, keepercfg.Defaults{})
	if err := store.Load(context.Background()); err != nil {
		t.Fatalf("load judge config: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES ('u_probe','p@example.com','P')`); err != nil {
		t.Fatalf("seed actor: %v", err)
	}
	ms := budget.Milliseconds()
	if _, err := store.Apply(context.Background(), keepercfg.Patch{TimeoutMS: &ms}, "u_probe"); err != nil {
		t.Fatalf("set budget: %v", err)
	}

	h := NewAdminKeeperJudgeHandler(store, newTestLogger())
	rr := httptest.NewRecorder()
	h.Test(rr, judgeReq(t, body))
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rr.Code, rr.Body.String())
	}
	var resp judgeTestResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

func TestJudgeTest_ColdLoadIsNotReportedAsASlowJudge(t *testing.T) {
	var calls int
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"name":"qwen3.5:9b"}]}`))
	})
	mux.HandleFunc("/api/chat", func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			// The cold load. Slow enough to blow any sane budget.
			time.Sleep(1500 * time.Millisecond)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"m","message":{"role":"assistant","content":"{\"decision\":\"ALLOW\",\"reason\":\"ok\",\"risk\":2}"},"done":true}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp := runJudgeTestWithBudget(t,
		`{"judge_endpoint_url":"`+srv.URL+`","judge_model":"qwen3.5:9b"}`,
		1000*time.Millisecond)

	if calls < 2 {
		t.Fatalf("chat calls = %d — the budget stage did not re-measure against a warm model", calls)
	}
	budget := stageByName(t, resp, "budget")
	if !budget.OK {
		t.Errorf("budget stage failed on a judge that answers fast once loaded: %s", budget.Detail)
	}
	if !resp.OK {
		t.Error("the check reported a working judge as unusable")
	}
	// The operator must be told the first call was slower, or a genuinely
	// marginal judge looks comfortable.
	if !strings.Contains(strings.ToLower(budget.Detail), "load") {
		t.Errorf("the re-measure was silent: %q", budget.Detail)
	}
}
