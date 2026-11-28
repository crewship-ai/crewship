package chatbridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/ws"
)

type auditedToolEvent struct {
	Kind, ID, Body string
}

func auditToolEvent(kind, body string, metadata any) (auditedToolEvent, bool) {
	if kind != "tool_call" && kind != "tool_result" {
		return auditedToolEvent{}, false
	}
	fields, _ := metadata.(map[string]any)
	idKey := "tool_id"
	if kind == "tool_result" {
		idKey = "tool_use_id"
	}
	id, _ := fields[idKey].(string)
	if kind == "tool_call" {
		body = ""
	}
	return auditedToolEvent{Kind: kind, ID: id, Body: body}, true
}

func TestChatRunAuditLargeClaudeToolEvents(t *testing.T) {
	for _, tc := range []struct {
		name            string
		calls, inputLen int
		terminalDelta   bool
	}{
		{"small_control", 2, 32, true},
		{"one_large_tool", 1, 95 << 10, true},
		{"aggregate_over_one_mib", 13, 90 << 10, true},
		{"terminal_text_without_delta", 2, 15 << 10, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var expected []auditedToolEvent
			var lock sync.Mutex
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				id := r.Header.Get("X-Synthetic-Tool-ID")
				var args map[string]string
				if err := json.NewDecoder(r.Body).Decode(&args); err != nil || len(args["value"]) != tc.inputLen {
					http.Error(w, "invalid synthetic arguments", http.StatusBadRequest)
					return
				}
				body := fmt.Sprintf("synthetic refusal for %s", id)
				lock.Lock()
				expected = append(expected, auditedToolEvent{"tool_call", id, ""}, auditedToolEvent{"tool_result", id, body})
				lock.Unlock()
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = fmt.Fprint(w, body)
			}))
			defer server.Close()

			var wire bytes.Buffer
			encoder := json.NewEncoder(&wire)
			emit := func(event any) {
				t.Helper()
				if err := encoder.Encode(event); err != nil {
					t.Fatal(err)
				}
			}
			assistant := func(block map[string]any) {
				emit(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{block}}})
			}
			delta := func(kind, field, value string) {
				emit(map[string]any{"type": "stream_event", "event": map[string]any{
					"type": "content_block_delta", "delta": map[string]any{"type": kind, field: value},
				}})
			}
			delta("text_delta", "text", "before tools")
			assistant(map[string]any{"type": "text", "text": "before tools"})
			for i := range tc.calls {
				id := fmt.Sprintf("synthetic-%d", i)
				args := map[string]string{"value": strings.Repeat("a", tc.inputLen), "call": id}
				raw, err := json.Marshal(args)
				if err != nil {
					t.Fatal(err)
				}
				for len(raw) > 0 {
					fragment := min(96, len(raw))
					delta("input_json_delta", "partial_json", string(raw[:fragment]))
					raw = raw[fragment:]
				}
				payload, err := json.Marshal(args)
				if err != nil {
					t.Fatal(err)
				}
				req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL, bytes.NewReader(payload))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("X-Synthetic-Tool-ID", id)
				res, err := server.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				var result bytes.Buffer
				_, err = result.ReadFrom(res.Body)
				_ = res.Body.Close()
				if err != nil || res.StatusCode != http.StatusUnprocessableEntity {
					t.Fatalf("synthetic MCP call %s returned %d: %v", id, res.StatusCode, err)
				}
				assistant(map[string]any{"type": "tool_use", "name": "mcp__crewship-routines__craft_sheet_apply", "id": id, "input": args})
				emit(map[string]any{"type": "user", "message": map[string]any{"content": []any{
					map[string]any{"type": "tool_result", "tool_use_id": id, "content": result.String(), "is_error": true},
				}}})
			}
			if tc.terminalDelta {
				delta("text_delta", "text", "after tools")
			}
			assistant(map[string]any{"type": "text", "text": "after tools"})
			emit(map[string]any{"type": "result", "subtype": "success", "result": "after tools", "is_error": false})
			if tc.name == "aggregate_over_one_mib" && wire.Len() <= 1<<20 {
				t.Fatal("aggregate did not cross 1 MiB")
			}

			dir := t.TempDir()
			logger := slog.New(slog.DiscardHandler)
			conv := conversation.NewStore(dir, logger)
			log := logcollector.NewWriter(dir, logger)
			resolver := &capResolver{info: baseInfo()}
			container := &scriptedContainer{agentOutput: wire.String()}
			b := testBridgeWithContainer(t, resolver, container)
			b.convStore, b.logWriter = conv, log
			defer conv.Close()
			defer log.Close()

			checkEvents := func(stage string, received []auditedToolEvent) {
				t.Helper()
				lock.Lock()
				defer lock.Unlock()
				if !reflect.DeepEqual(received, expected) {
					t.Errorf("%s received %v, want %v", stage, received, expected)
				}
			}
			var live []auditedToolEvent
			err := b.HandleChatMessage(t.Context(), "user-1", "audit-synthetic", "edit the synthetic sheet", func(event ws.ChatEvent) {
				if record, ok := auditToolEvent(event.Type, event.Content, event.Metadata); ok {
					live = append(live, record)
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			checkEvents("live parser", live)

			records, err := logcollector.NewReader(dir).ReadAgentLogs(resolver.info.CrewID, resolver.info.AgentSlug, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			var audited []auditedToolEvent
			for _, entry := range records {
				if record, ok := auditToolEvent(entry.Event, entry.Content, entry.Metadata); ok {
					audited = append(audited, record)
				}
			}
			checkEvents("audit log", audited)

			rows, err := os.ReadFile(dir + "/conversations/audit-synthetic.jsonl")
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("wire %d bytes, conversation JSONL %d bytes", wire.Len(), len(rows))
			messages, err := conv.Read(t.Context(), "audit-synthetic", 0, 0)
			if err != nil || len(messages) != 2 {
				t.Fatalf("conversation reload: %d messages: %v", len(messages), err)
			}
			var persisted []auditedToolEvent
			for _, part := range messages[1].Parts {
				if record, ok := auditToolEvent(part.Type, part.Content, part.Metadata); ok {
					persisted = append(persisted, record)
				}
			}
			checkEvents("persisted message", persisted)
			if !strings.Contains(messages[1].Content, "after tools") {
				t.Errorf("final assistant text missing after reload: %q", messages[1].Content)
			}
		})
	}
}

func TestChatRunAuditMissingTerminalIsVisibleAfterReload(t *testing.T) {
	output := claudeSuccessOutput(1)
	output = output[:strings.LastIndex(output, `{"type":"result"`)]
	resolver := &capResolver{info: baseInfo()}
	provider := &scriptedContainer{agentOutput: output}
	bridge := testBridgeWithContainer(t, resolver, provider)

	var sawError bool
	err := bridge.HandleChatMessage(t.Context(), "user-1", "audit-incomplete", "check audit loss", func(event ws.ChatEvent) {
		if event.Type == "error" && event.Content == orchestrator.ErrRunOutputIncomplete.Error() {
			sawError = true
		}
	})
	if !errors.Is(err, orchestrator.ErrRunOutputIncomplete) || !sawError {
		t.Fatalf("run error %v, surfaced=%v", err, sawError)
	}
	if len(resolver.runUpdates) != 1 || resolver.runUpdates[0].status != "FAILED" {
		t.Fatalf("run updates %v", resolver.runUpdates)
	}
	messages, err := bridge.convStore.Read(t.Context(), "audit-incomplete", 0, 0)
	if err != nil || len(messages) != 2 {
		t.Fatalf("reload: %d messages, %v", len(messages), err)
	}
	parts := messages[1].Parts
	if len(parts) == 0 || parts[len(parts)-1].Type != "error" || parts[len(parts)-1].Content != orchestrator.ErrRunOutputIncomplete.Error() {
		t.Fatalf("missing durable incomplete marker: %v", parts)
	}
}
