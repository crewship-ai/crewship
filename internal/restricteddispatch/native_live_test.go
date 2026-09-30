//go:build linux && restrictedruntime_live

package restricteddispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/restrictedruntime"
)

func TestLiveNativeFrozenToolsAndDurableAccounting(t *testing.T) {
	image := os.Getenv("CREWSHIP_RESTRICTED_NATIVE_IMAGE")
	if os.Getenv("CREWSHIP_RESTRICTED_LIVE") != "1" || image == "" {
		t.Fatal("set pinned CREWSHIP_RESTRICTED_NATIVE_IMAGE for owned native acceptance")
	}
	a := nativeFixture(t)
	if _, e := a.Store.DB.ExecContext(t.Context(), `INSERT INTO budget_limits(id,workspace_id,scope_kind,scope_id,window,limit_usd,mode) VALUES('native-cap','w','workspace','w','month',.4,'hard')`); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	m, e := restrictedruntime.NewNative(filepath.Join(t.TempDir(), "native-runtime"), restrictedruntime.Docker{Image: image}, a, a, restrictedruntime.NativeLimits())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := m.Close(); e != nil {
			t.Error(e)
		}
	})
	gate := make(chan struct{})
	started := make(chan struct{}, 1)
	var calls atomic.Int32
	var unknown atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := calls.Add(1)
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid provider body")
			w.WriteHeader(400)
			return
		}
		instructions, _ := body["instructions"].(string)
		actor := "h1"
		if instructions == "FROZEN_h2" {
			actor = "h2"
		} else if instructions != "FROZEN_h1" {
			t.Errorf("ambient instructions escaped: %q", instructions)
		}
		if body["model"] != "gpt-5-mini" || body["max_output_tokens"] != float64(128) || body["store"] != false || body["stream"] != true || r.Header.Get("Authorization") != "Bearer synthetic-pinned-key" || r.Header.Get("ChatGPT-Account-ID") != "" || body["client_metadata"] != nil || body["prompt_cache_key"] != nil {
			t.Error("native pinned contract lost")
		}
		tools, _ := body["tools"].([]any)
		if len(tools) != 2 {
			t.Error("unchecked native tools")
		}
		for _, v := range tools {
			tool := v.(map[string]any)
			if tool["name"] != "exec_command" && tool["name"] != "write_stdin" {
				t.Error("unsupported native tool")
			}
		}
		var pending int
		if e := a.Store.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM restricted_cost_reservations WHERE state='pending'`).Scan(&pending); e != nil || pending != 1 {
			t.Errorf("native upstream without one durable debit: %d %v", pending, e)
		}
		if count == 1 {
			started <- struct{}{}
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
		input, _ := body["input"].([]any)
		followup := false
		for _, v := range input {
			item := v.(map[string]any)
			if item["type"] == "reasoning" && item["encrypted_content"] != "OPAQUE_"+actor {
				t.Error("foreign opaque state")
			}
			if item["type"] == "function_call_output" {
				followup = true
				out, _ := item["output"].(string)
				if item["call_id"] != "call_"+actor || !strings.Contains(out, "OWN_"+actor) || !strings.Contains(out, "code 0") {
					t.Errorf("actual scratch tool failed or foreign: %v", item)
				}
			}
		}
		var output []any
		if !followup {
			args, _ := json.Marshal(map[string]any{"cmd": "printf OWN_" + actor + " > native-canary.txt; cat native-canary.txt", "shell": "/bin/sh", "login": false, "max_output_tokens": 100, "yield_time_ms": 1000})
			output = []any{map[string]any{"type": "reasoning", "id": "rs_" + actor, "summary": []any{map[string]any{"type": "summary_text", "text": "synthetic reasoning"}}, "encrypted_content": "OPAQUE_" + actor}, map[string]any{"type": "function_call", "id": "fc_" + actor, "call_id": "call_" + actor, "name": "exec_command", "arguments": string(args), "status": "completed"}}
		} else {
			output = []any{map[string]any{"type": "message", "id": "msg_" + actor, "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "NATIVE_DONE_" + actor, "annotations": []any{}}}}}
		}
		response := map[string]any{"id": fmt.Sprintf("resp_%d", count), "model": "gpt-5-mini", "status": "completed", "output": output}
		if !unknown.Load() || !followup {
			response["usage"] = map[string]any{"input_tokens": 20, "output_tokens": 20, "total_tokens": 40, "input_tokens_details": map[string]any{"cached_tokens": 0}}
		}
		payload, _ := json.Marshal(map[string]any{"type": "response.completed", "response": response})
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.completed\ndata: "+string(payload)+"\n\n")
	}))
	t.Cleanup(server.Close)
	if e = restrictedruntime.InstallAcceptanceTLS(m, server); e != nil {
		t.Fatal(e)
	}
	start := func(user, chat string) (string, *restrictedruntime.Session) {
		h, _, e := a.PrepareNative(ctx, user, "w", "a", chat, "", nil, 128, func(context.Context, access.Attempt) (NativePrompt, error) {
			return NativePrompt{Instructions: "FROZEN_" + user, Input: "OWN_SCOPED_TASK_" + user}, nil
		})
		if e != nil {
			t.Fatal(e)
		}
		s, e := m.Start(ctx, h)
		if e != nil {
			t.Fatal(e)
		}
		return h, s
	}
	finish := func(s *restrictedruntime.Session) {
		select {
		case <-s.Done():
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	h1, first := start("h1", "c1")
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	_, blocked := start("h2", "c2")
	finish(blocked)
	if calls.Load() != 1 {
		t.Fatal("concurrent native workers overspent shared hard cap")
	}
	close(gate)
	finish(first)
	out, e := first.Output(ctx)
	if e != nil || !strings.Contains(out, "NATIVE_DONE_h1") || !strings.Contains(out, `"done"`) {
		t.Fatalf("native positive loop failed: %q %v (provider calls=%d record=%+v)", out, e, calls.Load(), first.Record())
	}
	var artifactSeen bool
	for _, line := range strings.Split(out, "\n") {
		var frame struct {
			Type, Name string
			Content    []byte
		}
		if json.Unmarshal([]byte(line), &frame) == nil && frame.Type == "artifact" {
			if frame.Name != "native-canary.txt" || string(frame.Content) != "OWN_h1" {
				t.Fatalf("foreign native artifact: %+v", frame)
			}
			artifactSeen = true
		}
	}
	if !artifactSeen {
		t.Fatal("actual scratch artifact not captured before container deletion")
	}
	if calls.Load() != 2 {
		t.Fatalf("missing real native tool followup: %d", calls.Load())
	}
	var known int
	if e = a.Store.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM restricted_cost_reservations WHERE state='known' AND principal_id='h1'`).Scan(&known); e != nil || known != 2 {
		t.Fatalf("native model calls not settled: %d %v", known, e)
	}
	if e = a.Store.CompleteAttempt(ctx, h1); e != nil {
		t.Fatal(e)
	}
	unknown.Store(true)
	_, second := start("h2", "c2")
	finish(second)
	out, e = second.Output(ctx)
	if e != nil || !strings.Contains(out, "NATIVE_DONE_h2") || !strings.Contains(out, `"done"`) {
		t.Fatalf("second human native loop failed: %q %v", out, e)
	}
	var unknownCount int
	if e = a.Store.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM restricted_cost_reservations WHERE state='unknown' AND principal_id='h2'`).Scan(&unknownCount); e != nil || unknownCount != 1 {
		t.Fatalf("missing usage released native debit: %d %v", unknownCount, e)
	}
	_, last := start("h1", "c1")
	finish(last)
	if calls.Load() != 4 {
		t.Fatal("unknown native usage reset cumulative budget")
	}
	t.Log("real pinned native Codex: H1/H2 scratch tool loops, host-frozen prompts/tool definitions, attempt-bound opaque state, cumulative durable model debits, concurrent cap and unknown usage; no paid provider")
}
