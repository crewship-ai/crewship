package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"

	"github.com/crewship-ai/crewship/internal/groupchat"
)

// Exercise the real authenticated handlers, including empty arrays, nullable
// pagination, optional identities and a queued mention, against the generated
// response schemas. No test-local replacement DTO can hide a wire discrepancy.
func TestWorkspaceConversationResponseSchemasMatchHandlerWire(t *testing.T) {
	raw, err := os.ReadFile("openapi.gen.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	// OpenAPI 3.0 nullable is equivalent to a JSON Schema null type alternative.
	// Convert it only for the standards-based JSON Schema validator used here.
	var nullable func(any)
	nullable = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			if v["nullable"] == true {
				if typ, ok := v["type"].(string); ok {
					v["type"] = []any{typ, "null"}
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
	nullable(document)
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.Draft = jsonschema.Draft7
	const resource = "https://crewship.test/response-contract.json"
	if err = compiler.AddResource(resource, bytes.NewReader(encoded)); err != nil {
		t.Fatal(err)
	}
	schemas := map[string]*jsonschema.Schema{}
	for _, name := range []string{"WorkspaceConversation", "WorkspaceConversationMessage", "WorkspaceConversationActivity", "WorkspaceConversationList", "WorkspaceConversationMessages", "WorkspaceConversationParticipants", "WorkspaceConversationAgents", "WorkspaceConversationAgentJobs"} {
		schemas[name], err = compiler.Compile(resource + "#/components/schemas/" + name)
		if err != nil {
			t.Fatalf("compile %s: %v", name, err)
		}
	}
	db := setupTestDB(t)
	owner := seedTestUser(t, db)
	workspace := seedTestWorkspace(t, db, owner)
	h := NewWorkspaceConversationsHandler(groupchat.New(db), newTestLogger())
	request := func(handler http.HandlerFunc, method, query, conversation, body, schema string, status int) map[string]any {
		t.Helper()
		r := httptest.NewRequest(method, "/api/v1/conversations"+query, strings.NewReader(body))
		r.SetPathValue("conversationId", conversation)
		r = r.WithContext(withWorkspace(withUser(r.Context(), &AuthUser{ID: owner}), workspace, "OWNER"))
		w := httptest.NewRecorder()
		handler(w, r)
		if w.Code != status {
			t.Fatalf("%s status %d: %s", schema, w.Code, w.Body.String())
		}
		var response map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if err := schemas[schema].Validate(response); err != nil {
			t.Fatalf("%s actual response violates schema: %v\n%s", schema, err, w.Body.String())
		}
		return response
	}
	empty := request(h.List, "GET", "", "", "", "WorkspaceConversationList", 200)
	if rows, ok := empty["conversations"].([]any); !ok || len(rows) != 0 || empty["next_offset"] != nil {
		t.Fatalf("empty list encoding: %#v", empty)
	}
	channel := request(h.Create, "POST", "", "", `{"title":"Response contracts","kind":"channel"}`, "WorkspaceConversation", 201)
	id := channel["id"].(string)
	request(h.Get, "GET", "", id, "", "WorkspaceConversation", 200)
	request(h.Messages, "GET", "", id, "", "WorkspaceConversationMessages", 200)
	request(h.Members, "GET", "", id, "", "WorkspaceConversationParticipants", 200)
	request(h.Agents, "GET", "", id, "", "WorkspaceConversationAgents", 200)
	request(h.AgentJobs, "GET", "", id, "", "WorkspaceConversationAgentJobs", 200)
	activity := request(h.Activity, "GET", "", id, "", "WorkspaceConversationActivity", 200)
	if activity["issues"] != false || activity["routines"] != false {
		t.Fatal("activity must default off")
	}
	request(h.SetActivity, "PUT", "", id, `{"issues":true,"routines":false}`, "WorkspaceConversationActivity", 200)
	if _, err := db.Exec(`INSERT INTO agents(id,workspace_id,name,slug,agent_role) VALUES('schema-agent',?,'Schema agent','schema-agent','AGENT')`, workspace); err != nil {
		t.Fatal(err)
	}
	if err := h.store.AddAgent(t.Context(), workspace, owner, id, "schema-agent"); err != nil {
		t.Fatal(err)
	}
	request(h.Agents, "GET", "", id, "", "WorkspaceConversationAgents", 200)
	sent := request(h.Send, "POST", "", id, `{"client_id":"schema-retry","content":"Please summarize","mentioned_agent_ids":["schema-agent"]}`, "WorkspaceConversationMessage", 201)
	replay := request(h.Send, "POST", "", id, `{"client_id":"schema-retry","content":"Please summarize","mentioned_agent_ids":["schema-agent"]}`, "WorkspaceConversationMessage", 200)
	if sent["id"] != replay["id"] {
		t.Fatal("retry changed durable message identity")
	}
	request(h.Messages, "GET", "", id, "", "WorkspaceConversationMessages", 200)
	jobs := request(h.AgentJobs, "GET", "", id, "", "WorkspaceConversationAgentJobs", 200)
	if len(jobs["jobs"].([]any)) != 1 {
		t.Fatal("mention must expose one real job in the checked envelope")
	}
	page := request(h.List, "GET", "?limit=1", "", "", "WorkspaceConversationList", 200)
	if page["next_offset"] != float64(1) {
		t.Fatalf("paged list offset: %#v", page)
	}
	// Missing membership cannot be disguised as a successful, schema-valid page.
	if _, err := db.Exec(`DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, workspace, owner); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/api/v1/conversations", nil)
	r = r.WithContext(withWorkspace(withUser(r.Context(), &AuthUser{ID: owner}), workspace, "OWNER"))
	w := httptest.NewRecorder()
	h.List(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("removed member receives %d: %s", w.Code, w.Body.String())
	}
}
