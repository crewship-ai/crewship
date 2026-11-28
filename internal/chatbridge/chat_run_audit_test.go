package chatbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/testutil"
	"github.com/crewship-ai/crewship/internal/ws"
)

type auditedToolEvent struct {
	Kind, ID, Body string
	Failed         bool
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
	failed, _ := fields["is_error"].(bool)
	return auditedToolEvent{Kind: kind, ID: id, Body: body, Failed: failed}, true
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
		{"replayed_tool_frames", 1, 15 << 10, true},
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
				expected = append(expected, auditedToolEvent{"tool_call", id, "", false}, auditedToolEvent{"tool_result", id, body, true})
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
				call := map[string]any{"type": "tool_use", "name": "mcp__crewship-routines__craft_sheet_apply", "id": id, "input": args}
				outcome := map[string]any{"type": "user", "message": map[string]any{"content": []any{
					map[string]any{"type": "tool_result", "tool_use_id": id, "content": result.String(), "is_error": true},
				}}}
				assistant(call)
				if tc.name == "replayed_tool_frames" {
					assistant(call)
				}
				emit(outcome)
				if tc.name == "replayed_tool_frames" {
					emit(outcome)
				}
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
			db := testutil.MigratedDB(t).DB
			for _, statement := range []string{
				`INSERT INTO workspaces(id,name,slug) VALUES('ws-1','Workspace','audit-workspace')`,
				`INSERT INTO users(id,email) VALUES('user-1','audit-user@example.test')`,
				`INSERT INTO agents(id,workspace_id,name,slug) VALUES('agent-1','ws-1','Agent','audit-agent')`,
				`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('audit-synthetic','ws-1','agent-1','user-1','private')`,
			} {
				if _, err := db.ExecContext(t.Context(), statement); err != nil {
					t.Fatal(err)
				}
			}
			conv := conversation.NewStore(dir, logger, conversation.WithDB(db))
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
			if len(messages[1].RunResults) != 1 || messages[1].RunResults[0].Content != "after tools" {
				t.Fatalf("terminal result missing in the durable message")
			}
			var status string
			var archive []byte
			if err := db.QueryRowContext(t.Context(), `SELECT status, wire FROM chat_run_audits WHERE chat_id=? AND run_id=?`,
				"audit-synthetic", messages[1].RunID).Scan(&status, &archive); err != nil || status != conversation.RunAuditArchived {
				t.Fatalf("archive status %s: %v", status, err)
			}
			var frames []struct {
				Type    string
				Message struct {
					Content []struct {
						Type      string
						ID        string
						ToolUseID string `json:"tool_use_id"`
						Content   string
						IsError   bool `json:"is_error"`
					}
				}
			}
			if err := json.Unmarshal(archive, &frames); err != nil || len(frames) == 0 || frames[len(frames)-1].Type != "result" {
				t.Fatalf("archive has no terminal frame: %v", err)
			}
			var archived []auditedToolEvent
			seenCalls, seenResults := map[string]bool{}, map[string]bool{}
			for _, frame := range frames {
				for _, block := range frame.Message.Content {
					switch {
					case block.Type == "tool_use" && !seenCalls[block.ID]:
						seenCalls[block.ID] = true
						archived = append(archived, auditedToolEvent{"tool_call", block.ID, "", false})
					case block.Type == "tool_result" && !seenResults[block.ToolUseID]:
						seenResults[block.ToolUseID] = true
						archived = append(archived, auditedToolEvent{"tool_result", block.ToolUseID, block.Content, block.IsError})
					}
				}
			}
			checkEvents("retained wire, unique tool events", archived)
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

type brokenAuditReader struct {
	io.ReadCloser
}

func (r brokenAuditReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}

type interruptedAuditContainer struct {
	*scriptedContainer
}

func (c *interruptedAuditContainer) Exec(ctx context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	result, err := c.scriptedContainer.Exec(ctx, cfg)
	if err == nil && result.ExecID == "agent-exec" {
		result.Reader = brokenAuditReader{result.Reader}
	}
	return result, err
}

func TestChatRunAuditReaderErrorOverridesTerminalSuccess(t *testing.T) {
	resolver := &capResolver{info: baseInfo()}
	container := &interruptedAuditContainer{&scriptedContainer{agentOutput: claudeSuccessOutput(1)}}
	bridge := testBridgeWithContainer(t, resolver, container)
	err := bridge.HandleChatMessage(t.Context(), "user-1", "audit-reader-error", "verify audit", func(ws.ChatEvent) {})
	if !errors.Is(err, orchestrator.ErrRunOutputIncomplete) {
		t.Fatalf("transport failure after result must fail the audit, got %v", err)
	}
	if len(resolver.runUpdates) != 1 || resolver.runUpdates[0].status != "FAILED" {
		t.Fatalf("run updates %v", resolver.runUpdates)
	}
}
