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

func TestSharedChatReaderHasDedicatedBearerScheme(t *testing.T) {
	doc := buildDocument([]route{
		{method: "GET", path: "/api/v1/shared-chats/{shareId}/messages"},
		{method: "GET", path: "/api/v1/chats/{chatId}/messages", auth: true},
	})
	paths := doc["paths"].(map[string]any)
	shared := paths["/api/v1/shared-chats/{shareId}/messages"].(map[string]any)["get"].(map[string]any)
	security := shared["security"].([]map[string][]string)
	if len(security) != 1 || len(security[0]) != 1 {
		t.Fatalf("shared chat security = %#v, want only chatShareBearer", security)
	}
	if _, ok := security[0]["chatShareBearer"]; !ok {
		t.Fatalf("shared chat security = %#v, want only chatShareBearer", security)
	}
	ordinary := paths["/api/v1/chats/{chatId}/messages"].(map[string]any)["get"].(map[string]any)
	for _, item := range ordinary["security"].([]map[string][]string) {
		if _, ok := item["chatShareBearer"]; ok {
			t.Fatalf("ordinary chat route accepts share bearer: %#v", ordinary["security"])
		}
	}
	schemes := doc["components"].(map[string]any)["securitySchemes"].(map[string]any)
	if _, ok := schemes["chatShareBearer"]; !ok {
		t.Fatal("dedicated share security scheme missing")
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

// TestAddRouteExcludesTheGatedFixtureSurface pins the /api/v1/e2e/ exclusion
// (#2403): the browser-test fixture routes exist only on a server started
// with CREWSHIP_E2E_FIXTURES, so the public spec must not list them. RED
// before the exclusion: the route lands in the table like any other.
func TestAddRouteExcludesTheGatedFixtureSurface(t *testing.T) {
	seen := map[route]bool{}
	var routes []route
	src := `r.authedMut("POST", "/api/v1/e2e/fixtures/run-needs-human", roleManage, h.RunNeedsHuman)`
	addRoute(seen, &routes, "POST", "/api/v1/e2e/fixtures/run-needs-human", "e2e_fixtures.go", 0, src)
	if len(routes) != 0 {
		t.Fatalf("the gated fixture route was added to the public spec: %+v", routes)
	}
	// The exclusion is by prefix, not by the one path: a sibling fixture
	// added later is excluded the same way.
	addRoute(seen, &routes, "POST", "/api/v1/e2e/fixtures/other", "e2e_fixtures.go", 0, src)
	if len(routes) != 0 {
		t.Fatalf("a second route under %s was added to the public spec: %+v", e2eFixturePrefix, routes)
	}
	// And an ordinary route still lands.
	addRoute(seen, &routes, "POST", "/api/v1/inbox/{id}/act", "router_orchestration.go", 0,
		`r.authedMut("POST", "/api/v1/inbox/{id}/act", roleSelf, ih.Act)`)
	if len(routes) != 1 {
		t.Fatalf("an ordinary route was not added: %+v", routes)
	}
}

// These are the actual body and success contracts of the Harbor handlers,
// including operations that deliberately never read a request body.
func TestHarborHandlerWireContracts(t *testing.T) {
	for _, tc := range []struct {
		method, path, status, media, responseType string
		responseFields, requestFields             []string
		noRequest, required                       bool
	}{
		{"GET", "/api/v1/credential-folders", "200", "application/json", "array", nil, nil, true, false},
		{"POST", "/api/v1/credential-folders", "201", "application/json", "object", []string{"id", "parent_id", "reveal_policy", "credential_count"}, []string{"name", "description", "parent_id", "min_security_level", "default_security_level", "remind_after_days", "reveal_policy"}, false, true},
		{"PATCH", "/api/v1/credential-folders/{folderId}", "200", "application/json", "object", []string{"id", "parent_id", "reveal_policy", "credential_count"}, []string{"name", "description", "parent_id", "min_security_level", "default_security_level", "remind_after_days", "reveal_policy"}, false, true},
		{"DELETE", "/api/v1/credential-folders/{folderId}", "204", "", "", nil, nil, true, false},
		{"GET", "/api/v1/paymaster/credential-spend", "200", "application/json", "object", []string{"rows", "since", "until"}, nil, true, false},
		{"GET", "/api/v1/workspaces/{workspaceId}/pipelines/calendar.ics", "200", "text/calendar", "string", nil, nil, true, false},
		{"POST", "/api/v1/agents/{agentId}/release", "200", "application/json", "", nil, nil, true, false},
		{"POST", "/api/v1/workspaces/{workspaceId}/skills/{skillId}/archive", "200", "application/json", "", nil, nil, true, false},
		{"POST", "/api/v1/workspaces/{workspaceId}/skills/{skillId}/restore", "200", "application/json", "", nil, nil, true, false},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			doc := buildDocument([]route{{method: tc.method, path: tc.path, auth: true}})
			op := doc["paths"].(map[string]any)[tc.path].(map[string]any)[strings.ToLower(tc.method)].(map[string]any)
			resolve := func(schema map[string]any) map[string]any {
				if ref, ok := schema["$ref"].(string); ok {
					return doc["components"].(map[string]any)["schemas"].(map[string]any)[strings.TrimPrefix(ref, "#/components/schemas/")].(map[string]any)
				}
				return schema
			}
			body, hasBody := op["requestBody"]
			if tc.noRequest && hasBody {
				t.Error("handler does not consume a body; generated requestBody is invented")
			}
			if len(tc.requestFields) > 0 {
				if !hasBody {
					t.Fatal("missing typed request")
				}
				request := body.(map[string]any)
				if request["required"] != tc.required {
					t.Error("request body must be required")
				}
				schema := resolve(request["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any))
				props, _ := schema["properties"].(map[string]any)
				for _, field := range tc.requestFields {
					if props[field] == nil {
						t.Errorf("request omits %s", field)
					}
				}
				if props["parent_id"].(map[string]any)["nullable"] != true {
					t.Error("parent_id must allow null to move to the top level")
				}
				required, _ := schema["required"].([]string)
				if tc.method == "POST" && (len(required) != 1 || required[0] != "name") {
					t.Errorf("create must require only name, got %v", required)
				}
				if tc.method == "PATCH" && len(required) != 0 {
					t.Error("patch must not require omitted fields")
				}
			}
			response, ok := op["responses"].(map[string]any)[tc.status].(map[string]any)
			if !ok {
				t.Fatalf("missing actual success status %s", tc.status)
			}
			if tc.status == "204" {
				if response["content"] != nil {
					t.Fatal("204 must have no content")
				}
				if op["responses"].(map[string]any)["200"] != nil {
					t.Fatal("DELETE invents 200 JSON success")
				}
				return
			}
			if tc.responseType == "" {
				return
			}
			content, _ := response["content"].(map[string]any)[tc.media].(map[string]any)
			if content == nil {
				t.Fatalf("missing actual response media %s", tc.media)
			}
			schema := resolve(content["schema"].(map[string]any))
			if schema["type"] != tc.responseType {
				t.Fatalf("response type=%v want=%s", schema["type"], tc.responseType)
			}
			props, _ := schema["properties"].(map[string]any)
			for _, field := range tc.responseFields {
				if props[field] == nil {
					t.Errorf("response omits %s", field)
				}
			}
			if tc.responseType == "array" {
				item := resolve(schema["items"].(map[string]any))
				if item["properties"].(map[string]any)["id"] == nil {
					t.Fatal("folder list omits folder identity")
				}
			}
		})
	}
}

func TestAuditedBodylessActionsPreserveOtherRequests(t *testing.T) {
	merged := mergeDomainSchema(DomainSchema{NoRequest: true}, DomainSchema{Response: map[string]any{"type": "string"}})
	if !merged.NoRequest || merged.Response["type"] != "string" {
		t.Fatal("merging a response erased the audited bodyless contract")
	}
	for _, path := range []string{"/api/v1/agents", "/api/v1/credentials", "/api/v1/credential-folders"} {
		doc := buildDocument([]route{{method: "POST", path: path}})
		op := doc["paths"].(map[string]any)[path].(map[string]any)["post"].(map[string]any)
		if op["requestBody"] == nil {
			t.Fatalf("audited actions removed unrelated create request: %s", path)
		}
	}
}
