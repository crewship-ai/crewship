package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

func TestRedactChatAuditProtectsEscapedAndFragmentedCredentials(t *testing.T) {
	const secret = "audit-test-registered-credential-01753089"
	var raw bytes.Buffer
	encoder := json.NewEncoder(&raw)
	emit := func(frame map[string]any) {
		t.Helper()
		if err := encoder.Encode(frame); err != nil {
			t.Fatal(err)
		}
	}
	fragment := func(id, kind, field, text string) {
		emit(map[string]any{"type": "stream_event", "uuid": id, "event": map[string]any{
			"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": kind, field: text},
		}})
	}
	const prefix = "audit-test-registered-"
	const suffix = "credential-01753089"
	fragment("text1", "text_delta", "text", prefix)
	fragment("text2", "text_delta", "text", suffix)
	fragment("input1", "input_json_delta", "partial_json", `{"argument":"audit-test-registered-\u00`)
	fragment("input2", "input_json_delta", "partial_json", `63redential-01753089","safe":"keep"}`)
	emit(map[string]any{"type": "assistant", "uuid": "full",
		"input": map[string]any{"arg": secret, "password": "opaque-unknown-to-scrubber"},
		"meta":  []any{secret, `{"value":"audit-test-registered-\u0063redential-01753089"}`},
	})
	emit(map[string]any{"type": "result", "uuid": "terminal", "result": `audit-test-registered-\u0063redential-01753089`})

	got, err := redactChatAudit(raw.Bytes(), []string{secret})
	if err != nil {
		t.Fatal(err)
	}
	for _, denied := range []string{secret, prefix, suffix, "redential-01753089", "opaque-unknown-to-scrubber"} {
		if bytes.Contains(got, []byte(denied)) {
			t.Fatalf("credential fragment survived redaction: %q", denied)
		}
	}
	for _, expected := range []string{`"uuid":"text1"`, `"uuid":"text2"`, `"uuid":"input1"`, `"uuid":"input2"`, `"uuid":"full"`, `"uuid":"terminal"`, "keep", "REDACTED"} {
		if !bytes.Contains(got, []byte(expected)) {
			t.Errorf("missing %s", expected)
		}
	}
	var frames []json.RawMessage
	if err := json.Unmarshal(got, &frames); err != nil || len(frames) != 6 {
		t.Fatal("redaction dropped a wire envelope")
	}
}

func TestRedactChatAuditJoinsInitialContentAndOmitsUnknownPayload(t *testing.T) {
	const raw = `{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"audit-initial-"}}}
{"type":"stream_event","uuid":"midpoint","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"private-"}}}
{"type":"stream_event","uuid":"midpoint","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"private-"}}}
{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"material"}}}
{"type":"stream_event","event":{"type":"content_block_start","index":1,"content_block":{"type":"thinking","thinking":"audit-thinking-"}}}
{"type":"stream_event","event":{"type":"content_block_delta","index":1,"delta":{"type":"thinking_delta","thinking":"private-material"}}}
{"type":"stream_event","event":{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"{\"value\":\"audit-nested-\\u00"}}}
{"type":"stream_event","event":{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"70rivate-material\",\"safe\":\"keep-this\"}"}}}
line fragment should never appear
second diagnostic line should never appear
{"type":"stream_event","event":{"type":"content_block_start","index":3,"content_block":{"type":"future_payload","data":"unknown-body"}}}
{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"orphan-without-index"}}}
{"type":"stream_event","event":{"type":"content_block_start","index":4,"content_block":{"type":"tool_use","id":"draft-tool","input":{"arg":"ambiguous-input"}}}}
{"type":"stream_event","event":{"type":"content_block_delta","index":4,"delta":{"type":"input_json_delta","partial_json":"ambiguous-continuation"}}}
`
	wire, err := redactChatAudit([]byte(raw), []string{"audit-initial-private-material", "audit-thinking-private-material", "audit-nested-private-material"})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"audit-initial-", "audit-thinking-", "audit-nested-", "private-", "material", "line fragment", "second diagnostic", "unknown-body", "orphan-without-index", "ambiguous-input", "ambiguous-continuation"} {
		if bytes.Contains(wire, []byte(forbidden)) {
			t.Errorf("archive exposed unverified content %q", forbidden)
		}
	}
	for _, retained := range []string{"keep-this", "payload_omitted", "text_output", "future_payload", "draft-tool"} {
		if !bytes.Contains(wire, []byte(retained)) {
			t.Errorf("archive discarded expected structure: %q", retained)
		}
	}
}

func TestRedactChatAuditRefusesBrokenOrOversizedOutput(t *testing.T) {
	for _, raw := range []string{`{"type":"assistant",`, strings.Repeat("a", maxChatAuditBytes+1)} {
		data, err := redactChatAudit([]byte(raw), nil)
		if err == nil || data != nil {
			t.Fatal("unverifiable output must not be published")
		}
	}
}

func TestStreamOutputOmitsContentAuditForStrictPrivacy(t *testing.T) {
	r := &chunkRecorder{}
	o := New(nil, newMemState(), slog.New(slog.DiscardHandler))
	o.SetJournal(r)
	const content = `{"type":"result","result":"private synthetic content"}` + "\n"
	var status string
	var retained []byte
	req := AgentRunRequest{
		ChatID: "audit-chat", RunID: "audit-run", CLIAdapter: "CLAUDE_CODE", strictContentPrivacy: true,
		RecordChatAudit: func(_ context.Context, auditStatus string, wire []byte) error {
			status, retained = auditStatus, wire
			return nil
		},
	}
	_, err := o.streamOutput(t.Context(), &provider.ExecResult{Reader: io.NopCloser(strings.NewReader(content))}, req, func(AgentEvent) {}, nil)
	if err != nil || status != "omitted" || retained != nil {
		t.Fatalf("private audit: status=%s has content=%v err=%v", status, retained != nil, err)
	}
	if r.findFirst("exec.output_chunk") != nil {
		t.Fatal("private output appeared in the journal")
	}
}

func TestStreamOutputJournalReferencesPrivateChatAudit(t *testing.T) {
	recorder := &chunkRecorder{}
	o := New(nil, newMemState(), slog.New(slog.DiscardHandler))
	o.SetJournal(recorder)
	const content = `{"type":"result","result":"synthetic chat content"}` + "\n"
	req := AgentRunRequest{
		ChatID: "audit-chat", RunID: "audit-run", CLIAdapter: "CLAUDE_CODE",
		RecordChatAudit: func(_ context.Context, status string, wire []byte) error {
			if status != "staged" || !strings.Contains(string(wire), "synthetic chat content") {
				t.Fatal("complete capture was not staged")
			}
			return nil
		},
	}
	if _, err := o.streamOutput(t.Context(), &provider.ExecResult{Reader: io.NopCloser(strings.NewReader(content))}, req, func(AgentEvent) {}, nil); err != nil {
		t.Fatal(err)
	}
	entry := recorder.findFirst("exec.output_chunk")
	if entry == nil || entry.Payload["audit_ref"] == nil {
		t.Fatal("missing chat audit reference")
	}
	if _, containsOutput := entry.Payload["output"]; containsOutput {
		t.Fatal("private chat capture was copied to the journal")
	}
}

func TestStreamOutputOmitsAuditForUnverifiedAdapter(t *testing.T) {
	recorder := &chunkRecorder{}
	o := New(nil, newMemState(), slog.New(slog.DiscardHandler))
	o.SetJournal(recorder)
	const content = `{"type":"turn.completed","usage":{"input_tokens":1}}` + "\n"
	recorded := false
	req := AgentRunRequest{
		ChatID: "audit-chat", RunID: "audit-run", CLIAdapter: "CODEX_CLI",
		RecordChatAudit: func(_ context.Context, status string, wire []byte) error {
			recorded = true
			if status != "omitted" || wire != nil {
				t.Fatalf("unverified adapter recorded %s with %d bytes", status, len(wire))
			}
			return nil
		},
	}
	if _, err := o.streamOutput(t.Context(), &provider.ExecResult{Reader: io.NopCloser(strings.NewReader(content))}, req, func(AgentEvent) {}, nil); err != nil {
		t.Fatal(err)
	}
	if !recorded {
		t.Fatal("audit omission was not persisted")
	}
	entry := recorder.findFirst("exec.output_chunk")
	if entry == nil || entry.Payload["audit_status"] != "omitted" {
		t.Fatal("audit omission was not reflected in the journal")
	}
	if _, containsOutput := entry.Payload["output"]; containsOutput {
		t.Fatal("unverified chat output was copied to the journal")
	}
}
