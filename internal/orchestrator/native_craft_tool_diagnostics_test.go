package orchestrator

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func nativeToolDiagnosticRequest() AgentRunRequest {
	r := covRunReq().WithStrictContentPrivacy()
	r.nativeCraftTools = true
	return r
}
func nativeToolDiagnosticLog(t *testing.T, d *nativeCraftToolDiagnostic, req AgentRunRequest) map[string]any {
	t.Helper()
	var b bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&b, nil))
	d.report(logger, req)
	d.report(logger, req)
	if b.Len() == 0 {
		t.Fatal("missing native CLI tool summary")
	}
	if strings.Count(strings.TrimSpace(b.String()), "\n") != 0 {
		t.Fatal("more than one finish summary")
	}
	var row map[string]any
	if err := json.Unmarshal(b.Bytes(), &row); err != nil {
		t.Fatal(err)
	}
	if len(row) != 19 {
		t.Fatal("unexpected log field surface", row)
	}
	for key, value := range row {
		if key == "time" || key == "level" || key == "msg" || key == "incomplete" {
			continue
		}
		if _, ok := value.(float64); !ok {
			t.Fatal("non-numeric counter", key)
		}
	}
	return row
}
func TestNativeCraftToolDiagnosticActualParserCountsAndRedacts(t *testing.T) {
	const secret = "PRIVATE_NATIVE_TOKEN_PATH_ARGUMENT_RESULT_ERROR_RUN"
	req := nativeToolDiagnosticRequest()
	req.RunID = secret
	var d nativeCraftToolDiagnostic
	for i, name := range []string{"read", "acquire", "renew", "apply", "release"} {
		id := fmt.Sprintf("%s-%d", secret, i)
		raw, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "tool_use", "id": id, "name": "mcp__crewship-craft__craft_" + name, "input": map[string]any{"secret": secret}}}}})
		parseClaudeCodeStreamJSON(raw, func(e AgentEvent) { d.observe(req, e) })
		raw, _ = json.Marshal(map[string]any{"type": "user", "message": map[string]any{"content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "is_error": i%2 == 0, "content": secret}}}})
		parseClaudeCodeStreamJSON(raw, func(e AgentEvent) { d.observe(req, e) })
	}
	row := nativeToolDiagnosticLog(t, &d, req)
	encoded, _ := json.Marshal(row)
	if bytes.Contains(encoded, []byte(secret)) {
		t.Fatal("private child data or run identity leaked")
	}
	for i, name := range []string{"read", "acquire", "renew", "apply", "release"} {
		for suffix, want := range map[string]float64{"calls": 1, "results": 1, "errors": float64(1 - i%2)} {
			if row[name+"_"+suffix] != want {
				t.Fatalf("%s_%s=%v want%v", name, suffix, row[name+"_"+suffix], want)
			}
		}
	}
	if row["incomplete"] != false || len(d.tracked) != 5 {
		t.Fatal("valid paired stream incomplete", row)
	}
}
func TestNativeCraftToolDiagnosticBoundsAndAmbiguousIDs(t *testing.T) {
	req := nativeToolDiagnosticRequest()
	call := func(d *nativeCraftToolDiagnostic, id string) {
		d.observe(req, AgentEvent{Type: "tool_call", Metadata: map[string]any{"tool_name": "mcp__crewship-craft__craft_apply", "tool_id": id}})
	}
	result := func(d *nativeCraftToolDiagnostic, id string) {
		d.observe(req, AgentEvent{Type: "tool_result", Metadata: map[string]any{"tool_use_id": id, "is_error": true}})
	}
	for _, tc := range []struct {
		name           string
		run            func(*nativeCraftToolDiagnostic)
		calls, results int
		incomplete     bool
	}{
		{"duplicate call before result", func(d *nativeCraftToolDiagnostic) { call(d, "same"); call(d, "same"); result(d, "same") }, 2, 0, true},
		{"reuse after result", func(d *nativeCraftToolDiagnostic) {
			call(d, "same")
			result(d, "same")
			call(d, "same")
			result(d, "same")
		}, 2, 1, true},
		{"duplicate result", func(d *nativeCraftToolDiagnostic) { call(d, "same"); result(d, "same"); result(d, "same") }, 1, 1, true},
		{"different tool reuses pending id", func(d *nativeCraftToolDiagnostic) {
			call(d, "same")
			d.observe(req, AgentEvent{Type: "tool_call", Metadata: map[string]any{"tool_name": "mcp__crewship-craft__craft_read", "tool_id": "same"}})
			result(d, "same")
		}, 1, 0, true},
		{"unknown tool reuses pending id", func(d *nativeCraftToolDiagnostic) {
			call(d, "same")
			d.observe(req, AgentEvent{Type: "tool_call", Metadata: map[string]any{"tool_name": "PRIVATE_UNKNOWN_NAME", "tool_id": "same"}})
			result(d, "same")
		}, 1, 0, true},
		{"unknown error flag", func(d *nativeCraftToolDiagnostic) {
			call(d, "same")
			d.observe(req, AgentEvent{Type: "tool_result", Metadata: map[string]any{"tool_use_id": "same", "is_error": "PRIVATE_ERROR"}})
		}, 1, 1, true},
		{"missing call id", func(d *nativeCraftToolDiagnostic) { call(d, ""); result(d, "") }, 1, 0, true},
		{"oversize call id", func(d *nativeCraftToolDiagnostic) { call(d, strings.Repeat("x", 257)) }, 1, 0, true},
		{"missing result id", func(d *nativeCraftToolDiagnostic) { call(d, "known"); result(d, "") }, 1, 0, true},
		{"oversize result id", func(d *nativeCraftToolDiagnostic) { call(d, "known"); result(d, strings.Repeat("x", 257)) }, 1, 0, true},
		{"malformed result metadata", func(d *nativeCraftToolDiagnostic) {
			d.observe(req, AgentEvent{Type: "tool_result", Metadata: "PRIVATE_METADATA"})
		}, 0, 0, true},
		{"max size id", func(d *nativeCraftToolDiagnostic) { id := strings.Repeat("x", 256); call(d, id); result(d, id) }, 1, 1, false},

		{"unmatched result", func(d *nativeCraftToolDiagnostic) { result(d, "unknown") }, 0, 0, true},
		{"pending result at finish", func(d *nativeCraftToolDiagnostic) { call(d, "pending") }, 1, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var d nativeCraftToolDiagnostic
			tc.run(&d)
			row := nativeToolDiagnosticLog(t, &d, req)
			if row["apply_calls"] != float64(tc.calls) || row["apply_results"] != float64(tc.results) || row["incomplete"] != tc.incomplete {
				t.Fatal(row)
			}
			if len(d.tracked) > 32 {
				t.Fatal("unbounded IDs")
			}
		})
	}
	var d nativeCraftToolDiagnostic
	for i := 0; i < 1000; i++ {
		call(&d, fmt.Sprintf("id-%d", i))
		result(&d, fmt.Sprintf("id-%d", i))
	}
	row := nativeToolDiagnosticLog(t, &d, req)
	if len(d.tracked) != 32 || row["apply_calls"] != float64(32) || row["apply_results"] != float64(32) || row["apply_errors"] != float64(32) || row["incomplete"] != true {
		t.Fatal("capture budget not bounded", row, len(d.tracked))
	}
	for _, state := range d.tracked {
		if state != 0 {
			t.Fatal("consumed ID lost tombstone")
		}
	}
	var exhausted nativeCraftToolDiagnostic
	for i := 0; i < 32; i++ {
		call(&exhausted, fmt.Sprintf("pending-%d", i))
	}
	call(&exhausted, "pending-0") // Beyond budget: must still poison the old pairing.
	result(&exhausted, "pending-0")
	if exhausted.counts[3].results != 0 || exhausted.tracked["pending-0"] != 0 || len(exhausted.tracked) != 32 || !exhausted.incomplete {
		t.Fatal("budget exhaustion revived an ambiguous pending ID")
	}

}
func TestNativeCraftToolDiagnosticNativePrivateScopeAndNoCalls(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		native, private, mooring bool
	}{
		{"ordinary", false, false, false}, {"native nonprivate", true, false, false}, {"other private", false, true, false}, {"mooring private", false, true, true}, {"mixed", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := covRunReq()
			req.nativeCraftTools = tc.native
			req.strictContentPrivacy = tc.private
			req.mooringDocumentTools = tc.mooring
			var d nativeCraftToolDiagnostic
			d.observe(req, AgentEvent{Type: "tool_call", Metadata: map[string]any{"tool_name": "mcp__crewship-craft__craft_read", "tool_id": "secret"}})
			var b bytes.Buffer
			d.report(slog.New(slog.NewJSONHandler(&b, nil)), req)
			if b.Len() != 0 || d.tracked != nil || d.totalCalls != 0 {
				t.Fatal("escaped native private scope")
			}
		})
	}
	req := nativeToolDiagnosticRequest()
	var d nativeCraftToolDiagnostic
	parseClaudeCodeStreamJSON([]byte(`{"type":"system","subtype":"init","tools":["mcp__crewship-craft__craft_read"]}`), func(e AgentEvent) { d.observe(req, e) })
	d.observe(req, AgentEvent{Type: "tool_call", Metadata: map[string]any{"tool_name": "mcp__crewship-craft__craft_read_lookalike", "tool_id": "secret"}})
	d.report(nil, req)
	row := nativeToolDiagnosticLog(t, &d, req)
	for _, name := range []string{"read", "acquire", "renew", "apply", "release"} {
		for _, suffix := range []string{"calls", "results", "errors"} {
			if row[name+"_"+suffix] != float64(0) {
				t.Fatal("invented call from init/unknown tool", row)
			}
		}
	}
	if row["incomplete"] != false || d.tracked != nil {
		t.Fatal("no calls should be zero and complete", row)
	}
	var noInit nativeCraftToolDiagnostic
	if nativeToolDiagnosticLog(t, &noInit, req)["incomplete"] != false {
		t.Fatal("absence of init does not invent tool calls")
	}
}
