package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDesignHTTPContractsUseConcreteSchemas(t *testing.T) {
	for _, tc := range []struct{ method, path, status, request, response string }{
		{"POST", "/api/v1/pages/design", "201", "DesignCreateRequest", "DesignCreateResponse"},
		{"GET", "/api/v1/pages/{slug}/design", "200", "", "DesignSnapshot"},
		{"GET", "/api/v1/pages/{slug}/design/versions", "200", "", "DesignVersionList"},
		{"POST", "/api/v1/pages/{slug}/design/operations", "200", "DesignOperationsRequest", "DesignOperationsResponse"},
		{"POST", "/api/v1/pages/{slug}/design/snapshots", "200", "DesignCheckpointRequest", "DesignVersionReceipt"},
		{"POST", "/api/v1/pages/{slug}/design/restore", "200", "DesignVersionRequest", "DesignVersionReceipt"},
		{"GET", "/api/v1/pages/{slug}/design/comments", "200", "", "DesignCommentsResponse"},
		{"POST", "/api/v1/pages/{slug}/design/comments", "201", "DesignCommentRequest", "ArtifactProjectComment"},
		{"POST", "/api/v1/pages/{slug}/design/share", "200", "DesignVersionRequest", "DesignShareResponse"},
		{"POST", "/api/v1/pages/{slug}/design/agent", "200", "DesignAgentRequest", "DesignAgentResponse"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			doc := buildDocument([]route{{method: tc.method, path: tc.path}})
			op := doc["paths"].(map[string]any)[tc.path].(map[string]any)[strings.ToLower(tc.method)].(map[string]any)
			responses := op["responses"].(map[string]any)
			success, ok := responses[tc.status].(map[string]any)
			if !ok {
				t.Fatalf("missing real success %s: %#v", tc.status, responses)
			}
			schema := success["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
			if schema["$ref"] != "#/components/schemas/"+tc.response {
				t.Fatalf("response %#v", schema)
			}
			if tc.status == "201" {
				if _, exists := responses["200"]; exists {
					t.Fatal("invented 200 creation response")
				}
			}
			if tc.request != "" {
				body := op["requestBody"].(map[string]any)
				request := body["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
				if body["required"] != true || request["$ref"] != "#/components/schemas/"+tc.request {
					t.Fatalf("request %#v", body)
				}
			} else if _, exists := op["requestBody"]; exists {
				t.Fatal("GET must not invent a body")
			}
		})
	}
}

func TestDesignWebsocketContractIsBinaryUpgradeWithoutJSONBody(t *testing.T) {
	const path = "/api/v1/pages/{slug}/design/ws"
	op := buildDocument([]route{{method: "GET", path: path}})["paths"].(map[string]any)[path].(map[string]any)["get"].(map[string]any)
	parameters, _ := op["parameters"].([]map[string]any)
	foundRevision := false
	for _, parameter := range parameters {
		if parameter["name"] == "authority_revision" {
			foundRevision = parameter["in"] == "query" && parameter["required"] == false
		}
	}
	if !foundRevision {
		t.Fatal("missing optional browser authority revision fence")
	}
	responses := op["responses"].(map[string]any)
	upgrade, ok := responses["101"].(map[string]any)
	if !ok {
		t.Fatal("missing websocket 101 upgrade")
	}
	if _, exists := responses["200"]; exists {
		t.Fatal("websocket must not advertise a JSON 200 response")
	}
	if _, exists := upgrade["content"]; exists {
		t.Fatal("HTTP upgrade has no JSON body")
	}
}

func TestDesignSchemasValidateNodePatchesAndExactReceipts(t *testing.T) {
	raw, err := json.Marshal(buildDocument(nil))
	if err != nil {
		t.Fatal(err)
	}
	fixtures := []struct{ schema, good, bad string }{
		{"DesignOperationsRequest", `{"operations":[{"op":"upsert","node":{"id":"existing","fill":"#2563eb"}}]}`, `{"operations":[{"op":"execute","command":"arbitrary"}]}`},
		{"DesignNode", `{"id":"screen","type":"frame","x":0,"y":0,"width":360,"height":640}`, `{"id":"screen","type":"script","x":0,"y":0,"width":360,"height":640}`},
		{"DesignVersionReceipt", `{"version":2,"actor":"user"}`, `{"Version":2,"Actor":"user"}`},
		{"DesignCreateRequest", `{"slug":"checkout","name":"Checkout","created_by_test":true}`, `{"slug":"checkout","name":"Checkout","actor":"forged"}`},
		{"DesignCreateRequest", `{"slug":"continued","name":"Continued","source_page_id":"page","expected_human":"alice","initial_state":"AAA="}`, `{"slug":"continued","name":"Continued","source_page_id":17,"expected_human":"alice","initial_state":"AAA="}`},
		{"DesignSnapshot", `{"page_id":"page","authority_revision":"9007199254740993","origin":null,"can_edit":false,"can_comment":false,"nodes":[],"order":[],"version":1,"latest_version":1,"shared_version":0,"actor":{"id":"user","name":"Reader"}}`, `{"page_id":"page","nodes":[]}`},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.schema, func(t *testing.T) {
			schema := compileAt(t, raw, "/components/schemas/"+fixture.schema)
			var good, bad any
			if err = json.Unmarshal([]byte(fixture.good), &good); err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal([]byte(fixture.bad), &bad); err != nil {
				t.Fatal(err)
			}
			if err = schema.Validate(good); err != nil {
				t.Fatalf("valid wire shape refused: %v", err)
			}
			if schema.Validate(bad) == nil {
				t.Fatal("invalid wire shape accepted")
			}
		})
	}
}

func TestDesignAuthorityRevisionIsRequiredOpaqueString(t *testing.T) {
	raw, err := json.Marshal(buildDocument(nil))
	if err != nil {
		t.Fatal(err)
	}
	schema := compileAt(t, raw, "/components/schemas/DesignSnapshot")
	var body map[string]any
	err = json.Unmarshal([]byte(`{"page_id":"page","authority_revision":"9007199254740993","origin":null,"can_edit":true,"can_comment":true,"nodes":[],"order":[],"version":1,"latest_version":1,"shared_version":0,"actor":{"id":"user","name":"Owner"}}`), &body)
	if err != nil {
		t.Fatal(err)
	}
	if err = schema.Validate(body); err != nil {
		t.Fatalf("opaque policy revision refused: %v", err)
	}
	delete(body, "authority_revision")
	if schema.Validate(body) == nil {
		t.Fatal("missing authority revision accepted")
	}
	body["authority_revision"] = float64(2)
	if schema.Validate(body) == nil {
		t.Fatal("numeric policy revision accepted")
	}
}
