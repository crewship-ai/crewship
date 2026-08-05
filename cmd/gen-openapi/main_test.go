package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildDocumentUsesAuditedReadSchemas(t *testing.T) {
	doc := buildDocument([]route{
		{method: "GET", path: "/api/v1/agents"},
		{method: "GET", path: "/api/v1/runs"},
		{method: "GET", path: "/api/v1/agents/{agentId}"},
		{method: "POST", path: "/api/v1/agents"},
	})

	paths := doc["paths"].(map[string]any)
	assertResponseRef(t, paths, "/api/v1/agents", "AgentList")
	assertResponseRef(t, paths, "/api/v1/runs", "RunList")
	assertResponseRef(t, paths, "/api/v1/agents/{agentId}", "Agent")

	post := paths["/api/v1/agents"].(map[string]any)["post"].(map[string]any)
	request := post["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"]
	if request.(map[string]any)["$ref"] != "#/components/schemas/CoreAgentCreateRequestV2" {
		t.Fatalf("request schema = %#v, want CoreAgentCreateRequestV2", request)
	}
}

func TestBuildDocumentUsesAccurateUploadRequestMedia(t *testing.T) {
	doc := buildDocument([]route{
		{method: "POST", path: "/api/v1/agents/{agentId}/chats/{chatId}/attachments"},
		{method: "PUT", path: "/api/v1/agents/{agentId}/files/save"},
		{method: "PUT", path: "/api/v1/crews/{crewId}/files/save"},
	})
	paths := doc["paths"].(map[string]any)
	want := map[string]string{
		"/api/v1/agents/{agentId}/chats/{chatId}/attachments": "multipart/form-data",
		"/api/v1/agents/{agentId}/files/save":                 "application/octet-stream",
		"/api/v1/crews/{crewId}/files/save":                   "application/octet-stream",
	}
	for path, mediaType := range want {
		method := "put"
		if strings.HasSuffix(path, "attachments") {
			method = "post"
		}
		op := paths[path].(map[string]any)[method].(map[string]any)
		content := op["requestBody"].(map[string]any)["content"].(map[string]any)
		if _, ok := content[mediaType]; !ok {
			t.Fatalf("%s request media = %#v, want %s", path, content, mediaType)
		}
		if _, genericJSON := content["application/json"]; genericJSON {
			t.Fatalf("%s incorrectly advertises application/json", path)
		}
	}
}

func assertResponseRef(t *testing.T, paths map[string]any, path, name string) {
	t.Helper()
	op := paths[path].(map[string]any)["get"].(map[string]any)
	response := op["responses"].(map[string]any)["200"].(map[string]any)
	schema := response["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	want := "#/components/schemas/" + name
	if schema["$ref"] != want {
		t.Fatalf("%s response schema = %#v, want ref %q", path, schema, want)
	}
}

func TestAuditedListSchemasAreArrays(t *testing.T) {
	components := responseComponents()["schemas"].(map[string]any)
	for _, name := range []string{"WorkspaceList", "CrewList", "AgentList", "ProjectList", "IssueList", "SkillList"} {
		schema := components[name].(map[string]any)
		if schema["type"] != "array" {
			t.Errorf("%s type = %v, want array", name, schema["type"])
		}
	}
}
func TestBuildDocumentIncludesAnnotatedQueryTypesAndResponses(t *testing.T) {
	doc := buildDocument([]route{{
		method: "GET", path: "/api/v1/audit", annot: "query page:integer; responses 200,400,500",
	}})
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Paths map[string]map[string]struct {
			Parameters []map[string]any `json:"parameters"`
			Responses  map[string]any   `json:"responses"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	op := got.Paths["/api/v1/audit"]["get"]
	if len(op.Parameters) != 1 || op.Parameters[0]["name"] != "page" || op.Parameters[0]["schema"].(map[string]any)["type"] != "integer" {
		t.Fatalf("parameters = %#v", op.Parameters)
	}
	for _, code := range []string{"200", "400", "500"} {
		if _, ok := op.Responses[code]; !ok {
			t.Errorf("missing response %s", code)
		}
	}
}

func TestPathParametersRemainRequired(t *testing.T) {
	doc := buildDocument([]route{{method: "GET", path: "/api/v1/crews/{crewId}"}})
	params := doc["paths"].(map[string]any)["/api/v1/crews/{crewId}"].(map[string]any)["get"].(map[string]any)["parameters"].([]map[string]any)
	if len(params) != 1 || params[0]["in"] != "path" || params[0]["required"] != true {
		t.Fatalf("parameters = %#v", params)
	}
}
