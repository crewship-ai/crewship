package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
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
