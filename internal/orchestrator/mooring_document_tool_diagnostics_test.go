package orchestrator

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestMooringDocumentPrivateToolDiagnosticsUseActualParserAndRedact(t *testing.T) {
	const secret = "private-document-token-path-result"
	req := covRunReq().WithStrictContentPrivacy()
	req.mooringDocumentTools = true
	var d mooringDocumentToolDiagnostic
	lines := []string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"` + secret + `-read","name":"mcp__crewship-document__doc_read","input":{"private":"` + secret + `"}},{"type":"tool_use","id":"` + secret + `-submit","name":"mcp__crewship-document__doc_submit","input":{"private":"` + secret + `"}},{"type":"tool_use","id":"other","name":"` + secret + `","input":{}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"` + secret + `-read","is_error":true,"content":"` + secret + `"},{"type":"tool_result","tool_use_id":"` + secret + `-submit","content":"` + secret + `"},{"type":"tool_result","tool_use_id":"unmatched","is_error":true,"content":"` + secret + `"}]}}`,
	}
	for _, line := range lines {
		parseClaudeCodeStreamJSON([]byte(line), func(e AgentEvent) { d.observe(req, e) })
	}
	// A duplicate result cannot invent another outcome.
	d.observe(req, AgentEvent{Type: "tool_result", Metadata: map[string]any{"tool_use_id": secret + "-read", "is_error": true}})
	var output bytes.Buffer
	d.report(slog.New(slog.NewJSONHandler(&output, nil)), req)
	if strings.Contains(output.String(), secret) || strings.Contains(output.String(), "unmatched") {
		t.Fatal("private tool diagnostic leaked child-controlled content")
	}
	var entry map[string]any
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	for field, want := range map[string]float64{"read_calls": 1, "submit_calls": 1, "read_results": 1, "submit_results": 1, "read_errors": 1, "submit_errors": 0} {
		if entry[field] != want {
			t.Fatalf("%s = %v, want %v", field, entry[field], want)
		}
	}
	if entry["run_id"] != req.RunID || entry["incomplete"] != false || len(entry) != 11 || len(d.tracked) != 2 || d.tracked[secret+"-read"] != 0 || d.tracked[secret+"-submit"] != 0 {
		t.Fatal("unexpected diagnostic fields or pending state", entry)
	}
}

func TestMooringDocumentPrivateToolDiagnosticsBoundAndScope(t *testing.T) {
	private := covRunReq().WithStrictContentPrivacy()
	private.mooringDocumentTools = true
	for _, req := range []AgentRunRequest{covRunReq(), covRunReq().WithStrictContentPrivacy(), private} {
		var d mooringDocumentToolDiagnostic
		for i := 0; i < 1000; i++ {
			d.observe(req, AgentEvent{Type: "tool_call", Metadata: map[string]any{"tool_name": "mcp__crewship-document__doc_read", "tool_id": fmt.Sprintf("id-%d", i)}})
		}
		var output bytes.Buffer
		d.report(slog.New(slog.NewJSONHandler(&output, nil)), req)
		if req.mooringDocumentTools {
			if len(d.tracked) != 32 || d.readCalls != 32 || !d.incomplete {
				t.Fatal("diagnostic memory or counters are unbounded")
			}
		} else if output.Len() != 0 || d.tracked != nil || d.readCalls != 0 {
			t.Fatal("unrelated run retained or logged document tool data")
		}
	}
	var d mooringDocumentToolDiagnostic
	d.observe(private, AgentEvent{Type: "tool_call", Metadata: map[string]any{"tool_name": "mcp__crewship-document__doc_read", "tool_id": strings.Repeat("x", 257)}})
	d.observe(private, AgentEvent{Type: "tool_result", Metadata: "private-untrusted-shape"})
	if d.tracked != nil || d.readCalls != 1 || d.readResults != 0 || !d.incomplete {
		t.Fatal("oversized ID retained or unmatched result counted")
	}
	// The deferred reporter also permits a nil logger.
	d.report(nil, private)
}

func TestMooringDocumentPrivateToolDiagnosticsRejectAmbiguousPairing(t *testing.T) {
	req := covRunReq().WithStrictContentPrivacy()
	req.mooringDocumentTools = true
	var d mooringDocumentToolDiagnostic
	for _, tool := range []string{"doc_read", "doc_submit", "doc_read"} {
		d.observe(req, AgentEvent{Type: "tool_call", Metadata: map[string]any{"tool_name": "mcp__crewship-document__" + tool, "tool_id": "reused"}})
	}
	d.observe(req, AgentEvent{Type: "tool_result", Metadata: map[string]any{"tool_use_id": "reused", "is_error": true}})
	if !d.incomplete || d.readResults != 0 || d.submitResults != 0 || len(d.tracked) != 1 {
		t.Fatal("ambiguous tool ID was assigned an outcome")
	}
}
