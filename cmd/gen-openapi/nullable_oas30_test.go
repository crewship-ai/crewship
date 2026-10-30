package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// Plain JSON Schema ignores OpenAPI 3.0's nullable keyword. Use a real OAS
// validator for both schema validity and data acceptance at this boundary.
func loadNullableOAS30(t *testing.T) *openapi3.T {
	t.Helper()
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(specBytes(t))
	if err != nil {
		t.Fatalf("load generated OpenAPI: %v", err)
	}
	if doc.OpenAPI != "3.0.3" {
		t.Fatalf("OpenAPI = %q, want the existing 3.0.3 contract", doc.OpenAPI)
	}
	return doc
}

func TestGeneratedNullableSchemasAreOpenAPI30Valid(t *testing.T) {
	// Validate the affected schema objects with the real 3.0 validator.
	// Whole-document validation also exposes pre-existing 3.1-only keywords
	// in unrelated schemas; this nullability change does not waive that debt.
	for _, pointer := range affectedNullableOASPointers() {
		t.Run(pointer, func(t *testing.T) { compileOAS30At(t, specBytes(t), pointer) })
	}
	var raw any
	if err := json.Unmarshal(specBytes(t), &raw); err != nil {
		t.Fatal(err)
	}
	var nullTypes []string
	var walk func(any, string)
	walk = func(value any, pointer string) {
		switch typed := value.(type) {
		case map[string]any:
			if typed["type"] == "null" {
				nullTypes = append(nullTypes, pointer)
			}
			for key, child := range typed {
				key = strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
				walk(child, pointer+"/"+key)
			}
		case []any:
			for index, child := range typed {
				walk(child, fmt.Sprintf("%s/%d", pointer, index))
			}
		}
	}
	walk(raw, "")
	if len(nullTypes) != 0 {
		t.Errorf("OpenAPI 3.0 has %d unsupported null type branches: %v", len(nullTypes), nullTypes)
	}
}

func TestGeneratedNullableOriginsPreserveObjectConstraints(t *testing.T) {
	doc := loadNullableOAS30(t)
	for _, name := range []string{"ArtifactProjectSnapshot", "DesignSnapshot"} {
		t.Run(name, func(t *testing.T) {
			schema := doc.Components.Schemas[name].Value.Properties["origin"].Value
			if err := schema.Validate(context.Background()); err != nil {
				t.Fatalf("invalid OpenAPI 3.0 origin: %v", err)
			}
			for _, data := range []string{`null`, `{"chat_id":"chat","agent_id":"agent"}`} {
				checkNullableData(t, schema, data, true)
			}
			for _, data := range []string{`{}`, `{"chat_id":"chat"}`, `{"chat_id":"chat","agent_id":12}`, `{"chat_id":"chat","agent_id":"agent","extra":true}`, `"text"`, `[]`, `true`} {
				checkNullableData(t, schema, data, false)
			}
			checkNullOnlyBranch(t, schema.OneOf[1].Value)
		})
	}
}

func TestGeneratedNullableFoldersPreserveObjectConstraints(t *testing.T) {
	doc := loadNullableOAS30(t)
	page := doc.Paths.Value("/api/v1/pages/{slug}").Get.Responses.Value("200").Value.Content["application/json"].Schema.Value
	folder := page.Properties["folder"].Value
	if err := folder.Validate(context.Background()); err != nil {
		t.Fatalf("invalid OpenAPI 3.0 folder: %v", err)
	}
	for _, data := range []string{`null`, `{"slug":"folder","name":"Folder","icon":"","color":""}`} {
		checkNullableData(t, folder, data, true)
	}
	for _, data := range []string{`{"slug":12}`, `[]`, `"text"`, `false`} {
		checkNullableData(t, folder, data, false)
	}
	checkNullOnlyBranch(t, folder.OneOf[1].Value)

	move := doc.Paths.Value("/api/v1/page-folders/{slug}/pages").Post.Responses.Value("200").Value.Content["application/json"].Schema.Value.OneOf[1].Value
	from := move.Properties["from_folder"].Value
	if err := from.Validate(context.Background()); err != nil {
		t.Fatalf("invalid OpenAPI 3.0 move source: %v", err)
	}
	for _, data := range []string{`null`, `{"id":"folder","slug":"folder","acl_version":0}`} {
		checkNullableData(t, from, data, true)
	}
	for _, data := range []string{`{}`, `{"id":"folder","slug":"folder","acl_version":-1}`, `"text"`} {
		checkNullableData(t, from, data, false)
	}
	checkNullOnlyBranch(t, from.OneOf[1].Value)
}

func TestGeneratedArbitraryJSONValuesKeepAllKindsDisjoint(t *testing.T) {
	doc := loadNullableOAS30(t)
	for _, name := range []string{"FinalAuthWebhookPayload", "FinalAuthUserPreferenceValue"} {
		t.Run(name, func(t *testing.T) {
			schema := doc.Components.Schemas[name].Value
			if err := schema.Validate(context.Background()); err != nil {
				t.Fatalf("invalid OpenAPI 3.0 JSON value: %v", err)
			}
			for _, data := range []string{`null`, `{}`, `{"nested":null}`, `[]`, `[null,1,"value"]`, `[[null],{"nested":[null]}]`, `"text"`, `2.5`, `true`, `false`} {
				checkNullableData(t, schema, data, true)
			}
			checkNullOnlyBranch(t, schema.OneOf[len(schema.OneOf)-1].Value)
		})
	}
}

func checkNullableData(t *testing.T, schema *openapi3.Schema, raw string, valid bool) {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		t.Fatal(err)
	}
	err := schema.VisitJSON(value)
	if (err == nil) != valid {
		t.Errorf("data %s accepted=%v, want %v: %v", strconv.Quote(raw), err == nil, valid, err)
	}
}

func checkNullOnlyBranch(t *testing.T, schema *openapi3.Schema) {
	t.Helper()
	checkNullableData(t, schema, `null`, true)
	for _, raw := range []string{`{}`, `{"extra":true}`, `[]`, `"text"`, `0`, `false`} {
		checkNullableData(t, schema, raw, false)
	}
}

// compileOAS30At validates a schema selected from the real generated document,
// retaining its original reference graph. It is also used by existing fixtures
// that exercise null; plain JSON Schema would silently ignore nullable.
func compileOAS30At(t *testing.T, raw []byte, pointer string) *openapi3.Schema {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	var value any = root
	for _, token := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		switch parent := value.(type) {
		case map[string]any:
			var ok bool
			value, ok = parent[token]
			if !ok {
				t.Fatalf("missing schema pointer %s at %s", pointer, token)
			}
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(parent) {
				t.Fatalf("invalid schema pointer %s", pointer)
			}
			value = parent[index]
		default:
			t.Fatalf("schema pointer %s traverses a scalar", pointer)
		}
	}
	wrapper := map[string]any{
		"openapi": "3.0.3", "info": map[string]any{"title": "Schema contract fixture", "version": "1"},
		"components": root["components"],
		"paths": map[string]any{"/value": map[string]any{"post": map[string]any{
			"requestBody": map[string]any{"content": map[string]any{"application/json": map[string]any{"schema": value}}},
			"responses":   map[string]any{"204": map[string]any{"description": "No content"}},
		}}},
	}
	data, err := json.Marshal(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := openapi3.NewLoader().LoadFromData(data)
	if err != nil {
		t.Fatalf("load schema %s: %v", pointer, err)
	}
	schema := doc.Paths.Value("/value").Post.RequestBody.Value.Content["application/json"].Schema.Value
	if err := schema.Validate(context.Background()); err != nil {
		t.Fatalf("invalid OpenAPI 3.0 schema %s: %v", pointer, err)
	}
	return schema
}

func affectedNullableOASPointers() []string {
	components := []string{
		"/components/schemas/ArtifactProjectSnapshot/properties/origin",
		"/components/schemas/DesignSnapshot/properties/origin",
		"/components/schemas/FinalAuthWebhookPayload",
		"/components/schemas/FinalAuthUserPreferenceValue",
	}
	paths := []struct{ path, method, status, suffix string }{
		{"/api/v1/page-folders/{slug}", "get", "200", "/properties/pages/items/properties/folder"},
		{"/api/v1/page-folders/{slug}/pages", "post", "200", "/oneOf/0/properties/page/properties/folder"},
		{"/api/v1/page-folders/{slug}/pages", "post", "200", "/oneOf/1/properties/from_folder"},
		{"/api/v1/page-folders/{slug}/pages/{page}", "delete", "200", "/properties/page/properties/folder"},
		{"/api/v1/page-folders/{slug}/pages:batch", "post", "200", "/properties/pages/items/properties/folder"},
		{"/api/v1/pages", "get", "200", "/items/properties/folder"},
		{"/api/v1/pages", "post", "200", "/properties/folder"},
		{"/api/v1/pages", "post", "201", "/properties/folder"},
		{"/api/v1/pages/import", "post", "200", "/properties/folder"},
		{"/api/v1/pages/import", "post", "201", "/properties/folder"},
		{"/api/v1/pages/{slug}", "get", "200", "/properties/folder"},
		{"/api/v1/pages/{slug}", "patch", "200", "/properties/folder"},
		{"/api/v1/pages/{slug}/rollback", "post", "200", "/properties/page/properties/folder"},
	}
	for _, path := range paths {
		components = append(components, "/paths/"+esc(path.path)+"/"+path.method+"/responses/"+path.status+"/content/"+esc("application/json")+"/schema"+path.suffix)
	}
	return components
}
