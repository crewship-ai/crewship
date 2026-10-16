package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// OpenAPI nullable is translated for the standards-based JSON Schema validator;
// every required list, property and reference remains the generated contract.
func documentResponseSchemas(t *testing.T) map[string]*jsonschema.Schema {
	t.Helper()
	raw, err := os.ReadFile("openapi.gen.json")
	if err != nil {
		t.Fatal(err)
	}
	var spec any
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	var nullable func(any)
	nullable = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			if v["nullable"] == true {
				if kind, ok := v["type"].(string); ok {
					v["type"] = []any{kind, "null"}
				}
				delete(v, "nullable")
			}
			for _, child := range v {
				nullable(child)
			}
		case []any:
			for _, child := range v {
				nullable(child)
			}
		}
	}
	nullable(spec)
	raw, err = json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.Draft = jsonschema.Draft7
	const resource = "https://crewship.test/docs-responses.json"
	if err := compiler.AddResource(resource, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	out := map[string]*jsonschema.Schema{}
	for _, name := range []string{"DocumentState", "DocumentReceipt", "DocumentCheckpoint", "DocumentCheckpoints", "DocumentComments", "DocumentRequests", "DocumentInitializationState", "DocumentInitializeReceipt"} {
		out[name], err = compiler.Compile(resource + "#/components/schemas/" + name)
		if err != nil {
			t.Fatalf("compile %s: %v", name, err)
		}
	}
	return out
}

func validateDocumentResponse(t *testing.T, schemas map[string]*jsonschema.Schema, name string, raw []byte) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if err := schemas[name].Validate(body); err != nil {
		t.Fatalf("actual %s response violates generated contract: %v\n%s", name, err, raw)
	}
	// A renamed/missing identity envelope must fail, not silently pass a schema
	// whose required list accidentally vanished while its property list survived.
	damaged := make(map[string]any, len(body))
	for key, value := range body {
		damaged[key] = value
	}
	field := "id"
	switch name {
	case "DocumentState", "DocumentInitializationState":
		field = "schema"
	case "DocumentCheckpoints":
		field = "checkpoints"
	case "DocumentComments":
		field = "comments"
	case "DocumentRequests":
		field = "requests"
	}
	delete(damaged, field)
	if schemas[name].Validate(damaged) == nil {
		t.Fatalf("%s accepts response without mandatory %s", name, field)
	}
	return body
}

func TestDocumentRequestResponseSchemaMatchesRealHandler(t *testing.T) {
	schemas := documentResponseSchemas(t)
	h, workspace, user, _ := documentRequestFixture(t)
	read := func() map[string]any {
		req := pagesGrantRequest(t, "GET", "/api/v1/pages/report/document/requests", workspace, user, "OWNER", "")
		req.SetPathValue("slug", "report")
		response := httptest.NewRecorder()
		h.ListDocumentRequests(response, req)
		if response.Code != 200 {
			t.Fatalf("list status %d: %s", response.Code, response.Body.String())
		}
		return validateDocumentResponse(t, schemas, "DocumentRequests", response.Body.Bytes())
	}
	empty := read()
	if rows, ok := empty["requests"].([]any); !ok || len(rows) != 0 {
		t.Fatalf("empty requests is not an empty array: %#v", empty)
	}
	id := createDocumentRequestTest(t, h, workspace, user, documentRequestBody, 202)
	populated := read()
	rows := populated["requests"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["id"] != id || rows[0].(map[string]any)["status"] != "queued" {
		t.Fatalf("saved request missing from response: %#v", populated)
	}
}
