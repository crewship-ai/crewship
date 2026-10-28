package orchestrator

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestNativeCraftInitDiagnosticActualParserRedactsAndEmitsOnce(t *testing.T) {
	const secret = "private-document-token-path-error-run-id"
	var out bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&out, nil))
	req := covRunReq().WithStrictContentPrivacy()
	req.nativeCraftTools = true
	req.RunID = secret
	var d nativeCraftInitDiagnostic
	parseClaudeCodeStreamJSON([]byte(`{"type":"system","subtype":"init","tools":["mcp__crewship-craft__craft_read","mcp__crewship-craft__craft_acquire","mcp__crewship-craft__craft_renew","mcp__crewship-craft__craft_apply","mcp__crewship-craft__craft_release","`+secret+`"],"mcp_servers":[{"name":"crewship-craft","status":"connected","error":"`+secret+`"},{"name":"`+secret+`","status":"failed"}],"cwd":"`+secret+`","mcp_server_errors":[{"error":"`+secret+`"}]}`), func(e AgentEvent) { d.observe(logger, req, e); d.observe(logger, req, e) })
	if out.Len() == 0 {
		t.Fatal("native private init self-report missing")
	}
	if strings.Contains(out.String(), secret) {
		t.Fatal("private child data or run identity leaked")
	}
	if strings.Count(strings.TrimSpace(out.String()), "\n") != 0 {
		t.Fatal("duplicate init log")
	}
	var entry map[string]any
	if err := json.Unmarshal(out.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	fields := []string{"craft_server_reported", "craft_server_connected", "tool_inventory_reported", "craft_read_reported", "craft_acquire_reported", "craft_renew_reported", "craft_apply_reported", "craft_release_reported"}
	if len(entry) != len(fields)+3 {
		t.Fatal("unexpected diagnostic field surface", entry)
	}
	for _, key := range fields {
		if entry[key] != true {
			t.Fatal("missing actual fixed readiness bit", key)
		}
	}
}

func TestNativeCraftInitDiagnosticScopeAndUntrustedShapes(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		native, private, mooring bool
		event                    AgentEvent
		want                     bool
	}{
		{name: "ordinary", event: AgentEvent{Type: "system", Metadata: map[string]any{"subtype": "init"}}},
		{name: "native nonprivate", native: true, event: AgentEvent{Type: "system", Metadata: map[string]any{"subtype": "init"}}},
		{name: "other private", private: true, event: AgentEvent{Type: "system", Metadata: map[string]any{"subtype": "init"}}},
		{name: "mixed private", native: true, private: true, mooring: true, event: AgentEvent{Type: "system", Metadata: map[string]any{"subtype": "init"}}},
		{name: "tool event", native: true, private: true, event: AgentEvent{Type: "tool_call", Metadata: map[string]any{"subtype": "init"}}},
		{name: "noninit", native: true, private: true, event: AgentEvent{Type: "system", Metadata: map[string]any{"subtype": "private"}}},
		{name: "untrusted", native: true, private: true, want: true, event: AgentEvent{Type: "system", Metadata: map[string]any{"subtype": "init", "tools": "private-secret", "mcp_servers": []map[string]any{{"name": "crewship-craft", "status": "private-secret"}}}}},
		{name: "lookalikes", native: true, private: true, want: true, event: AgentEvent{Type: "system", Metadata: map[string]any{"subtype": "init", "tools": []string{"mcp__crewship-craft__craft_apply_private-secret"}, "mcp_servers": []map[string]any{{"name": "crewship-craft-private-secret", "status": "connected"}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			req := covRunReq()
			req.strictContentPrivacy = tc.private
			req.nativeCraftTools = tc.native
			req.mooringDocumentTools = tc.mooring
			var d nativeCraftInitDiagnostic
			d.observe(slog.New(slog.NewJSONHandler(&out, nil)), req, tc.event)
			if (out.Len() > 0) != tc.want {
				t.Fatal("incorrect diagnostic scope")
			}
			if !tc.want {
				return
			}
			if strings.Contains(out.String(), "private-secret") {
				t.Fatal("untrusted text leaked")
			}
			var entry map[string]any
			if err := json.Unmarshal(out.Bytes(), &entry); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"craft_server_connected", "craft_read_reported", "craft_acquire_reported", "craft_renew_reported", "craft_apply_reported", "craft_release_reported"} {
				if entry[key] != false {
					t.Fatal("invented readiness", key)
				}
			}
		})
	}
}

func TestNativeCraftInitDiagnosticFinishObservesInitWithoutPrivateData(t *testing.T) {
	for _, observed := range []bool{false, true} {
		t.Run(map[bool]string{false: "no init", true: "actual parsed init"}[observed], func(t *testing.T) {
			const secret = "private-finish-run-document-token-path"
			req := covRunReq().WithStrictContentPrivacy()
			req.nativeCraftTools = true
			req.RunID = secret
			var d nativeCraftInitDiagnostic
			if observed {
				parseClaudeCodeStreamJSON([]byte(`{"type":"system","subtype":"init","tools":["`+secret+`"],"cwd":"`+secret+`"}`), func(e AgentEvent) { d.observe(nil, req, e) })
			}
			var out bytes.Buffer
			d.report(slog.New(slog.NewJSONHandler(&out, nil)), req)
			var entry map[string]any
			if err := json.Unmarshal(out.Bytes(), &entry); err != nil {
				t.Fatal("missing finish diagnostic", err)
			}
			if entry["init_observed"] != observed || len(entry) != 4 {
				t.Fatal("unexpected finish diagnostic", entry)
			}
			if strings.Contains(out.String(), secret) {
				t.Fatal("private finish data leaked")
			}
		})
	}
	for _, tc := range []struct {
		name                     string
		native, private, mooring bool
	}{
		{name: "ordinary"}, {name: "native nonprivate", native: true}, {name: "other private", private: true}, {name: "mooring private", private: true, mooring: true}, {name: "mixed private", native: true, private: true, mooring: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := covRunReq()
			req.nativeCraftTools = tc.native
			req.strictContentPrivacy = tc.private
			req.mooringDocumentTools = tc.mooring
			var out bytes.Buffer
			d := nativeCraftInitDiagnostic{emitted: true}
			d.report(slog.New(slog.NewJSONHandler(&out, nil)), req)
			if out.Len() != 0 {
				t.Fatal("finish diagnostic escaped native private scope")
			}
		})
	}
	var d nativeCraftInitDiagnostic
	req := covRunReq().WithStrictContentPrivacy()
	req.nativeCraftTools = true
	d.report(nil, req)
}
