package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// A response schema that names its properties but not its REQUIRED ones
// accepts a body in which every one of those properties is missing and a
// different set has appeared. Nothing about that is hypothetical:
//
// /api/v1/approvals serialized harbormaster.Request, which carried no JSON
// tags, so the API answered "ID"/"Kind"/"Status"/"CreatedAt" while THIS
// document, the web client's zod schema and docs/api-reference/approvals.mdx
// all described snake_case. Three artifacts agreed with each other and none
// with the server. Every Go test passed, because encoding/json matches field
// names case-insensitively on the way in and every test decoded into a struct;
// every frontend test passed, because each wrote its own snake_case fixture.
// The approvals surface rendered zero rows in production the whole time.
//
// Of those four artifacts this document is the only one a machine checks
// against the running server, so it is the one that has to be strict. A
// permissive schema does not merely fail to catch the drift — it certifies it.
func specBytes(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../internal/api/openapi.gen.json")
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	return raw
}

// NOTE ON `nullable`: this compiles the schema as plain JSON Schema, which has
// no `nullable` keyword — that is OpenAPI 3.0's spelling, and Schemathesis, the
// tool actually graded against this document, does understand it. So the good
// fixtures below use non-null values for nullable fields. This test is about
// field NAMES, not nullability; asserting the latter here would only measure
// the gap between two validators.
func compileAt(t *testing.T, raw []byte, pointer string) *jsonschema.Schema {
	t.Helper()
	c := jsonschema.NewCompiler()
	if err := c.AddResource("spec.json", bytes.NewReader(raw)); err != nil {
		t.Fatalf("add spec: %v", err)
	}
	sch, err := c.Compile("spec.json#" + pointer)
	if err != nil {
		t.Fatalf("compile %s: %v", pointer, err)
	}
	return sch
}

func esc(path string) string { return strings.ReplaceAll(path, "/", "~1") }

func TestResponseSchemas_RejectABodyWithEveryFieldRenamed(t *testing.T) {
	raw := specBytes(t)

	cases := []struct {
		name    string
		pointer string
		good    string
		renamed string
	}{
		{
			name:    "GET /api/v1/approvals",
			pointer: "/paths/" + esc("/api/v1/approvals") + "/get/responses/200/content/" + esc("application/json") + "/schema",
			good:    `{"rows":[{"id":"ap_1","workspace_id":"ws","crew_id":"","agent_id":"","mission_id":"","requested_by":"u","kind":"tool_call","reason":"r","payload":{},"status":"pending","decided_by":"","decided_at":"2026-08-31T00:00:00Z","decision_comment":"","timeout_at":"2026-08-31T01:00:00Z","created_at":"2026-08-31T00:00:00Z"}],"status":"pending","count":1,"has_more":false}`,
			renamed: `{"rows":[{"ID":"ap_1","WorkspaceID":"ws","Kind":"tool_call","Status":"pending","CreatedAt":"2026-08-31T00:00:00Z","Payload":{},"TimeoutSecs":0}],"status":"pending","count":1}`,
		},
		{
			name:    "GET /api/v1/inbox",
			pointer: "/paths/" + esc("/api/v1/inbox") + "/get/responses/200/content/" + esc("application/json") + "/schema",
			good:    `{"rows":[{"id":"ibx_1","workspace_id":"ws","kind":"waitpoint","source_id":"src","title":"t","state":"unread","priority":"medium","blocking":false,"created_at":"2026-08-31T00:00:00Z","updated_at":"2026-08-31T00:00:00Z"}],"count":1,"unread_count":0,"has_more":false}`,
			renamed: `{"rows":[{"ID":"ibx_1","Kind":"waitpoint","State":"unread","Title":"t","CreatedAt":"2026-08-31T00:00:00Z"}],"count":1,"unread_count":0}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sch := compileAt(t, raw, tc.pointer)

			var good any
			if err := json.Unmarshal([]byte(tc.good), &good); err != nil {
				t.Fatalf("fixture: %v", err)
			}
			if err := sch.Validate(good); err != nil {
				t.Fatalf("the shape the server is SUPPOSED to send does not validate — the document is wrong about its own API: %v", err)
			}

			var renamed any
			if err := json.Unmarshal([]byte(tc.renamed), &renamed); err != nil {
				t.Fatalf("fixture: %v", err)
			}
			if err := sch.Validate(renamed); err == nil {
				t.Errorf("a body with EVERY field renamed validates against this schema.\n" +
					"That is the exact drift that shipped an empty approvals screen, and the\n" +
					"contract gate is graded against this document — so it cannot see it.\n" +
					"Name the required properties at the call site in cmd/gen-openapi.")
			}
		})
	}
}

// The ratchet. Every response schema that names properties but no required
// properties is one the gate cannot grade. The number may fall; it may not
// rise. Same shape as the stdout-receipt budget in cmd/crewship.
func TestResponseSchemas_WithoutRequired_DoNotGrow(t *testing.T) {
	// Measured, not chosen: 228 when this test was written, 227 after
	// /api/v1/approvals was named. It is a debt counter, and the only
	// legitimate direction is down. Raising it re-opens the hole that shipped
	// an empty approvals screen — for one more route, every time.
	//
	// The inbox is not in this count: its 200 body is a $ref to
	// FinalInboxList, and a $ref is graded where the component is defined.
	// That component now names its required fields too.
	const budget = 227

	var doc map[string]any
	if err := json.Unmarshal(specBytes(t), &doc); err != nil {
		t.Fatalf("parse spec: %v", err)
	}
	paths, _ := doc["paths"].(map[string]any)

	var loose []string
	for path, item := range paths {
		methods, _ := item.(map[string]any)
		for method, op := range methods {
			operation, ok := op.(map[string]any)
			if !ok {
				continue
			}
			responses, _ := operation["responses"].(map[string]any)
			ok200, _ := responses["200"].(map[string]any)
			content, _ := ok200["content"].(map[string]any)
			appJSON, _ := content["application/json"].(map[string]any)
			schema, _ := appJSON["schema"].(map[string]any)
			if schema == nil {
				continue
			}
			props, hasProps := schema["properties"].(map[string]any)
			if !hasProps || len(props) == 0 {
				continue // a $ref or a free-form object; graded where it is defined
			}
			if _, hasRequired := schema["required"]; !hasRequired {
				loose = append(loose, fmt.Sprintf("%s %s", strings.ToUpper(method), path))
			}
		}
	}

	if len(loose) > budget {
		t.Errorf("%d response schemas declare properties but no `required` (budget %d).\n"+
			"Each one accepts a body that shares not a single field name with what the\n"+
			"server actually sends. Do not raise the budget; name the required fields.\n"+
			"First few: %s", len(loose), budget, strings.Join(loose[:min(len(loose), 8)], ", "))
	}
}
