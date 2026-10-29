package orchestrator

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestMooringDocumentPrivateInitDiagnosticsRedactAndObserveOnce(t *testing.T) {
	const secret = "private-token-path-document-diagnostic"
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	req := covRunReq().WithStrictContentPrivacy()
	req.mooringDocumentTools = true
	var diagnostic mooringDocumentInitDiagnostic
	parseClaudeCodeStreamJSON([]byte(`{"type":"system","subtype":"init","tools":["mcp__crewship-document__doc_read","mcp__crewship-document__doc_submit","`+secret+`"],"mcp_servers":[{"name":"crewship-document","status":"connected","error":"`+secret+`"},{"name":"`+secret+`","status":"failed"}],"cwd":"`+secret+`"}`), func(event AgentEvent) {
		diagnostic.observe(logger, req, event)
		diagnostic.observe(logger, req, event)
	})
	if strings.Contains(output.String(), secret) || strings.Count(strings.TrimSpace(output.String()), "\n") != 0 {
		t.Fatalf("private init leaked or repeated: %s", output.String())
	}
	var entry map[string]any
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"document_server_reported", "document_server_connected", "tool_inventory_reported", "doc_read_reported", "doc_submit_reported"} {
		if entry[field] != true {
			t.Fatalf("missing reported readiness %s", field)
		}
	}
	if entry["run_id"] != req.RunID || len(entry) != 9 {
		t.Fatal("unexpected diagnostic fields", entry)
	}
}

func TestMooringDocumentPrivateInitDiagnosticsDoNotInventReadiness(t *testing.T) {
	for _, tc := range []struct {
		name    string
		req     AgentRunRequest
		event   AgentEvent
		wantLog bool
	}{
		{"ordinary", covRunReq(), AgentEvent{Type: "system", Metadata: map[string]any{"subtype": "init"}}, false},
		{"other private", covRunReq().WithStrictContentPrivacy(), AgentEvent{Type: "system", Metadata: map[string]any{"subtype": "init"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			var d mooringDocumentInitDiagnostic
			d.observe(slog.New(slog.NewJSONHandler(&output, nil)), tc.req, tc.event)
			if output.Len() != 0 {
				t.Fatal("unrelated run logged document readiness")
			}
		})
	}
	var output bytes.Buffer
	var d mooringDocumentInitDiagnostic
	req := covRunReq().WithStrictContentPrivacy()
	req.mooringDocumentTools = true
	event := AgentEvent{Type: "system", Metadata: map[string]any{"subtype": "init", "mcp_servers": []map[string]any{{"name": "crewship-document", "status": "private-untrusted-text"}}, "tools": "private-untrusted-text"}}
	d.observe(slog.New(slog.NewJSONHandler(&output, nil)), req, event)
	var entry map[string]any
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if entry["document_server_reported"] != true {
		t.Fatal("server presence lost")
	}
	for _, key := range []string{"document_server_connected", "tool_inventory_reported", "doc_read_reported", "doc_submit_reported"} {
		if entry[key] != false {
			t.Fatal("invented positive readiness", key)
		}
	}
	if strings.Contains(output.String(), "private-untrusted-text") {
		t.Fatal("untrusted scalar leaked")
	}
}
