package messagesmock

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

const key = "synthetic-canary-never-a-real-provider-key"
const model = "fixture-claude"

func script(role string) []Step {
	return []Step{
		{Role: role, MatchMessage: "ROLE_" + role, ToolName: "memory.read", ToolUseID: "tool_" + role,
			ToolArgs: map[string]any{"query": "actual-owned-memory"}},
		{Role: role, MatchMessage: "ROLE_" + role, RequireResult: "MEMORY_NONCE_" + role, Text: "done " + role},
	}
}

func fixture(t *testing.T, steps []Step) *Transport {
	t.Helper()
	f, err := New(Config{ExpectedKey: key, Model: model, Script: steps})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func wire(t *testing.T, role string, result bool, stream bool) *http.Request {
	t.Helper()
	messages := []any{map[string]any{"role": "user", "content": "ROLE_" + role}}
	if result {
		messages = append(messages,
			map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "tool_" + role,
				"name": "memory.read", "input": map[string]any{"query": "actual-owned-memory"}}}},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "tool_" + role,
				"content": []any{map[string]any{"type": "text", "text": "real output MEMORY_NONCE_" + role}}}}})
	}
	body, _ := json.Marshal(map[string]any{"model": model, "max_tokens": 128, "stream": stream,
		"messages": messages, "tools": []any{map[string]any{"name": "memory.read", "input_schema": map[string]any{"type": "object"}}}})
	req, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")
	return req
}

func mutateBody(t *testing.T, req *http.Request, change func(map[string]any)) {
	t.Helper()
	var body map[string]any
	if json.NewDecoder(req.Body).Decode(&body) != nil {
		t.Fatal("fixture body")
	}
	change(body)
	data, _ := json.Marshal(body)
	req.Body = io.NopCloser(strings.NewReader(string(data)))
}

func consume(t *testing.T, f *Transport, req *http.Request) []byte {
	t.Helper()
	response, err := f.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func events(t *testing.T, data []byte) []map[string]any {
	t.Helper()
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	name := ""
	var result []map[string]any
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			name = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			var event map[string]any
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) != nil || event["type"] != name {
				t.Fatalf("invalid SSE event %q", line)
			}
			result = append(result, event)
		}
	}
	if scanner.Err() != nil {
		t.Fatal(scanner.Err())
	}
	return result
}

func TestRealCorrelatedToolLoopAndStreamSchema(t *testing.T) {
	f := fixture(t, script("lead"))
	if f.Complete() == nil {
		t.Fatal("unconsumed script accepted")
	}
	stream := events(t, consume(t, f, wire(t, "lead", false, true)))
	want := []string{"message_start", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
	if len(stream) != len(want) {
		t.Fatal(stream)
	}
	for i, kind := range want {
		if stream[i]["type"] != kind {
			t.Fatal(stream)
		}
	}
	start := stream[0]["message"].(map[string]any)
	if start["role"] != "assistant" || start["model"] != model || len(start["content"].([]any)) != 0 || start["stop_reason"] != nil {
		t.Fatal(start)
	}
	block := stream[1]["content_block"].(map[string]any)
	delta := stream[2]["delta"].(map[string]any)
	var args map[string]any
	if block["type"] != "tool_use" || block["id"] != "tool_lead" || block["name"] != "memory.read" ||
		delta["type"] != "input_json_delta" || json.Unmarshal([]byte(delta["partial_json"].(string)), &args) != nil || args["query"] != "actual-owned-memory" {
		t.Fatal(block, delta)
	}
	if stream[4]["delta"].(map[string]any)["stop_reason"] != "tool_use" {
		t.Fatal(stream[4])
	}
	var final map[string]any
	if json.Unmarshal(consume(t, f, wire(t, "lead", true, false)), &final) != nil || final["stop_reason"] != "end_turn" ||
		final["content"].([]any)[0].(map[string]any)["text"] != "done lead" || final["usage"].(map[string]any)["input_tokens"] != float64(16) {
		t.Fatal(final)
	}
	if err := f.Complete(); err != nil {
		t.Fatal(err)
	}
	evidence := f.Evidence()
	evidence[0].Role = "modified caller copy"
	encoded, _ := json.Marshal(f.Evidence())
	if strings.Contains(string(encoded), key) || strings.Contains(string(encoded), "MEMORY_NONCE") || f.Evidence()[0].Role != "lead" {
		t.Fatal("evidence leaks private bodies or aliases mutable storage")
	}
}

func TestRequestsFailClosedAndNeverDelegateToNetwork(t *testing.T) {
	cases := map[string]func(*http.Request){
		"wrong key":               func(r *http.Request) { r.Header.Set("x-api-key", "wrong") },
		"duplicate key":           func(r *http.Request) { r.Header.Add("x-api-key", key) },
		"wrong version":           func(r *http.Request) { r.Header.Set("anthropic-version", "1999-01-01") },
		"wrong host":              func(r *http.Request) { r.URL.Host = "no-network.invalid" },
		"host override":           func(r *http.Request) { r.Host = "no-network.invalid" },
		"wrong scheme":            func(r *http.Request) { r.URL.Scheme = "http" },
		"wrong method":            func(r *http.Request) { r.Method = "GET" },
		"query":                   func(r *http.Request) { r.URL.RawQuery = "unexpected=1" },
		"unconfigured count":      func(r *http.Request) { r.URL.Path = "/v1/messages/count_tokens" },
		"wrong model":             func(r *http.Request) { mutateBody(t, r, func(b map[string]any) { b["model"] = "other" }) },
		"missing token ceiling":   func(r *http.Request) { mutateBody(t, r, func(b map[string]any) { delete(b, "max_tokens") }) },
		"missing advertised tool": func(r *http.Request) { mutateBody(t, r, func(b map[string]any) { b["tools"] = []any{} }) },
		"duplicate advertised tool": func(r *http.Request) {
			mutateBody(t, r, func(b map[string]any) {
				b["tools"] = append(b["tools"].([]any), map[string]any{"name": "memory.read"})
			})
		},
		"unknown role": func(r *http.Request) {
			mutateBody(t, r, func(b map[string]any) {
				b["messages"] = []any{map[string]any{"role": "user", "content": "ROLE_unknown"}}
			})
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := fixture(t, script("lead"))
			r := wire(t, "lead", false, false)
			mutate(r)
			if response, err := f.RoundTrip(r); err == nil || response != nil || strings.Contains(err.Error(), key) {
				t.Fatal("invalid request accepted or secret included in error", err)
			}
			if _, err := f.RoundTrip(wire(t, "lead", false, false)); err == nil || f.Complete() == nil {
				t.Fatal("rejection was not sticky")
			}
		})
	}
}

func TestToolResultsMustActuallyMatchLastIssuedTool(t *testing.T) {
	cases := map[string]func(map[string]any){
		"missing":       func(b map[string]any) { b["messages"] = b["messages"].([]any)[:1] },
		"cross role ID": func(b map[string]any) { lastResult(b)["tool_use_id"] = "tool_peer" },
		"error":         func(b map[string]any) { lastResult(b)["is_error"] = true },
		"nonce only in prompt": func(b map[string]any) {
			b["messages"].([]any)[0].(map[string]any)["content"] = "ROLE_lead MEMORY_NONCE_lead"
			lastResult(b)["content"] = "no actual memory found"
		},
		"empty result": func(b map[string]any) { lastResult(b)["content"] = "" },
		"wrong tool args": func(b map[string]any) {
			b["messages"].([]any)[1].(map[string]any)["content"].([]any)[0].(map[string]any)["input"] = map[string]any{"query": "invented"}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := fixture(t, script("lead"))
			consume(t, f, wire(t, "lead", false, false))
			r := wire(t, "lead", true, false)
			mutateBody(t, r, change)
			if _, err := f.RoundTrip(r); err == nil || f.Complete() == nil {
				t.Fatal("invalid completion accepted")
			}
		})
	}
}

func lastResult(b map[string]any) map[string]any {
	m := b["messages"].([]any)
	return m[len(m)-1].(map[string]any)["content"].([]any)[0].(map[string]any)
}

func TestConcurrentRolesDoNotShareStepCounters(t *testing.T) {
	f := fixture(t, append(script("lead"), script("peer")...))
	var wg sync.WaitGroup
	for _, role := range []string{"peer", "lead"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			consume(t, f, wire(t, role, false, true))
			consume(t, f, wire(t, role, true, true))
		}()
	}
	wg.Wait()
	if err := f.Complete(); err != nil {
		t.Fatal(err)
	}
	if len(f.Evidence()) != 4 {
		t.Fatal(f.Evidence())
	}
	if _, err := f.RoundTrip(wire(t, "peer", true, false)); err == nil || f.Complete() == nil {
		t.Fatal("exhausted script accepted")
	}
}

func TestExplicitCountTokensAndImmutableScript(t *testing.T) {
	steps := append([]Step{{Role: "lead", MatchMessage: "ROLE_lead", Path: "/v1/messages/count_tokens", InputTokens: 42}}, script("lead")...)
	f := fixture(t, steps)
	steps[1].ToolArgs["query"] = "caller tampering"
	r := wire(t, "lead", false, false)
	r.URL.Path = "/v1/messages/count_tokens"
	if string(consume(t, f, r)) != `{"input_tokens":42}` {
		t.Fatal("wrong count response")
	}
	consume(t, f, wire(t, "lead", false, false))
	consume(t, f, wire(t, "lead", true, false))
	if err := f.Complete(); err != nil {
		t.Fatal(err)
	}
}

func TestTokenCountingDoesNotAcknowledgeToolCompletion(t *testing.T) {
	steps := script("lead")
	steps = []Step{steps[0], {Role: "lead", MatchMessage: "ROLE_lead", Path: "/v1/messages/count_tokens", InputTokens: 42}, steps[1]}
	f := fixture(t, steps)
	consume(t, f, wire(t, "lead", false, false))
	count := wire(t, "lead", true, false)
	count.URL.Path = "/v1/messages/count_tokens"
	consume(t, f, count)
	consume(t, f, wire(t, "lead", true, false))
	if err := f.Complete(); err != nil {
		t.Fatal(err)
	}
}

func TestRetryStormEvidenceIsBoundedAndCannotPass(t *testing.T) {
	f := fixture(t, script("lead"))
	for i := 0; i < 1100; i++ {
		r := wire(t, "lead", false, false)
		r.Header.Set("x-api-key", "wrong")
		if _, err := f.RoundTrip(r); err == nil {
			t.Fatal("retry storm request accepted")
		}
	}
	if len(f.Evidence()) != 1024 || f.Complete() == nil {
		t.Fatal("unbounded evidence or incorrect completion")
	}
}

func TestInvalidScriptsAndUnsolicitedOrAmbiguousResults(t *testing.T) {
	for name, steps := range map[string][]Step{
		"dangling tool":            script("lead")[:1],
		"duplicate tool ID":        append(script("lead"), script("lead")...),
		"missing tool arguments":   {{Role: "lead", MatchMessage: "ROLE_lead", ToolName: "Bash", ToolUseID: "id"}},
		"secret in evidence":       {{Role: key, MatchMessage: "marker", Text: "done"}},
		"secret returned to agent": {{Role: "lead", MatchMessage: "marker", Text: key}},
		"secret in tool args": {
			{Role: "lead", MatchMessage: "marker", ToolName: "Bash", ToolUseID: "id", ToolArgs: map[string]any{"command": []any{key}}},
			{Role: "lead", MatchMessage: "marker", Text: "done"},
		},
		"unsupported count": {{Role: "lead", MatchMessage: "ROLE_lead", Path: "/v1/messages/count_tokens"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := New(Config{ExpectedKey: key, Model: model, Script: steps}); err == nil {
				t.Fatal("invalid script accepted")
			}
		})
	}
	f := fixture(t, script("lead"))
	if _, err := f.RoundTrip(wire(t, "lead", true, false)); err == nil {
		t.Fatal("result before actual tool response accepted")
	}
	f = fixture(t, append(script("lead"), script("peer")...))
	r := wire(t, "lead", false, false)
	mutateBody(t, r, func(b map[string]any) { b["messages"].([]any)[0].(map[string]any)["content"] = "ROLE_lead ROLE_peer" })
	if _, err := f.RoundTrip(r); err == nil {
		t.Fatal("ambiguous role accepted")
	}
}

func ExampleConfig() {
	_, err := New(Config{ExpectedKey: "synthetic-sidecar-canary", Model: "fixture-model", Script: []Step{
		{Role: "lead", MatchMessage: "ROLE_lead", ToolName: "Bash", ToolUseID: "tool_lead", ToolArgs: map[string]any{"command": "printf fixture_nonce"}},
		{Role: "lead", MatchMessage: "ROLE_lead", RequireResult: "fixture_nonce", Text: "done"},
	}})
	fmt.Println(err)
	// Output: <nil>
}
