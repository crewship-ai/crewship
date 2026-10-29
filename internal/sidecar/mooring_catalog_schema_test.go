package sidecar

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/santhosh-tekuri/jsonschema/v5"
)

func TestMooringAdvertisedToolsHaveMCPObjectInputSchemas(t *testing.T) {
	s, key := newRunIdentityServer(t)
	s.ipc.Token = "synthetic-document-ipc"
	r := httptest.NewRequest("POST", "/mcp/mooring", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	r.Header.Set("Authorization", "Bearer "+internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot"))
	w := httptest.NewRecorder()
	s.handleMooringMCP(w, r)
	if w.Code != 200 {
		t.Fatalf("tools/list: HTTP %d", w.Code)
	}
	var response struct {
		Result struct {
			Tools []memoryMCPToolDescriptor `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Result.Tools) != 2 {
		t.Fatal("missing document tool catalog")
	}
	for _, tool := range response.Result.Tools {
		t.Run(tool.Name, func(t *testing.T) {
			var input map[string]any
			if err := json.Unmarshal(tool.InputSchema, &input); err != nil {
				t.Fatal(err)
			}
			// MCP's Tool.inputSchema requires an explicit object type, even
			// when every oneOf branch is already an object. A valid JSON
			// Schema alone is not necessarily a valid MCP tool descriptor.
			// https://modelcontextprotocol.io/specification/2025-06-18/schema#tool
			if input["type"] != "object" {
				t.Fatal("MCP Tool.inputSchema lacks required root type: object")
			}
			schema, err := jsonschema.CompileString("document-catalog.json", string(tool.InputSchema))
			if err != nil {
				t.Fatal(err)
			}
			var valid, invalid []string
			switch tool.Name {
			case "doc_read":
				valid = []string{`{}`, `{"page":{}}`}
				invalid = []string{`[]`, `{"page_id":"another"}`}
			case "doc_submit":
				valid = []string{`{"submission":{}}`, mooringTypedToolInput}
				invalid = []string{`[]`, `{}`, `{"submission":{},"extra":true}`, `{"submission":{},"view":{},"mutation_id":"x","changes":[]}`}
			default:
				t.Fatal("unexpected document tool")
			}
			for _, group := range []struct {
				values []string
				valid  bool
			}{{valid, true}, {invalid, false}} {
				for _, raw := range group.values {
					var value any
					if err := json.Unmarshal([]byte(raw), &value); err != nil {
						t.Fatal(err)
					}
					if err := schema.Validate(value); (err == nil) != group.valid {
						t.Fatalf("valid=%v: %v", group.valid, err)
					}
				}
			}
		})
	}
}
