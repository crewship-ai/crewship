package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/groupchat"
)

func TestWorkspaceConversationSchemasMatchResponseFields(t *testing.T) {
	_, schemas := workspaceConversationSchemaCatalog()
	for name, value := range map[string]any{
		"WorkspaceConversation": groupchat.Conversation{}, "WorkspaceConversationMessage": groupchat.Message{},
		"WorkspaceConversationParticipant": groupchat.Member{}, "WorkspaceConversationAgent": groupchat.AgentMember{}, "WorkspaceConversationAgentJob": groupchat.Job{},
	} {
		t.Run(name, func(t *testing.T) {
			schema := schemas[name].(map[string]any)
			properties := schema["properties"].(map[string]any)
			required := map[string]bool{}
			for _, key := range schema["required"].([]string) {
				required[key] = true
			}
			typ := reflect.TypeOf(value)
			for i := 0; i < typ.NumField(); i++ {
				tag := typ.Field(i).Tag.Get("json")
				key := strings.Split(tag, ",")[0]
				if _, ok := properties[key]; !ok {
					t.Errorf("missing wire field %s", key)
				}
				if required[key] == strings.Contains(tag, "omitempty") {
					t.Errorf("required mismatch for %s (%s)", key, tag)
				}
			}
			if len(properties) != typ.NumField() {
				t.Fatalf("schema has fields absent from wire: %d vs %d", len(properties), typ.NumField())
			}
		})
	}
}
func TestWorkspaceConversationRouteResponsesAndRequests(t *testing.T) {
	catalog, _ := workspaceConversationSchemaCatalog()
	if len(catalog) != 18 {
		t.Fatalf("routes=%d", len(catalog))
	}
	named, empty, requests := 0, 0, 0
	for key, contract := range catalog {
		rt := routeFromKey(key)
		doc := buildDocument([]route{rt})
		operation := doc["paths"].(map[string]any)[openAPIPath(rt.path)].(map[string]any)[strings.ToLower(rt.method)].(map[string]any)
		responses := operation["responses"].(map[string]any)
		if contract.Request != nil {
			requests++
			if _, ok := operation["requestBody"]; !ok {
				t.Errorf("%s omitted request", key)
			}
		}
		if contract.Response != nil {
			named++
		} else {
			empty++
		}
		for _, status := range contract.SuccessStatuses {
			response, ok := responses[status].(map[string]any)
			if !ok {
				t.Errorf("%s lacks %s", key, status)
				continue
			}
			if status == "204" {
				if _, ok := response["content"]; ok {
					t.Errorf("%s advertises body on 204", key)
				}
			} else {
				content := response["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
				if content["$ref"] != contract.Response["$ref"] {
					t.Errorf("%s lacks named response: %#v", key, content)
				}
			}
		}
	}
	if named != 12 || empty != 6 || requests != 9 {
		t.Fatalf("named=%d empty=%d requests=%d", named, empty, requests)
	}
	for _, route := range []string{"POST /api/v1/conversations/{conversationId}/messages", "POST /api/v1/conversations/direct"} {
		statuses := catalog[route].SuccessStatuses
		if !reflect.DeepEqual(statuses, []string{"200", "201"}) {
			t.Fatalf("%s reuse must return 200 and creation 201: %v", route, statuses)
		}
	}
}
func TestWorkspaceConversationRequestValidationAndJobStates(t *testing.T) {
	_, schemas := workspaceConversationSchemaCatalog()
	for _, name := range []string{"Direct", "Continue", "Create", "Send", "Read", "Mute", "AddParticipant", "AddAgent"} {
		schema := schemas["WorkspaceConversation"+name+"Request"].(map[string]any)
		if schema["additionalProperties"] != false {
			t.Errorf("%s request permits fields the decoder rejects", name)
		}
	}
	if _, required := schemas["WorkspaceConversationReadRequest"].(map[string]any)["required"]; required {
		t.Fatal("read cursor omission is accepted as zero")
	}
	states := schemas["WorkspaceConversationAgentJob"].(map[string]any)["properties"].(map[string]any)["state"].(map[string]any)["enum"].([]string)
	if !reflect.DeepEqual(states, []string{"pending", "queued", "running", "completed", "failed"}) {
		t.Fatalf("job states=%v", states)
	}
	mentions := schemas["WorkspaceConversationMessage"].(map[string]any)["properties"].(map[string]any)["mentioned_agent_ids"].(map[string]any)
	if mentions["nullable"] != true {
		t.Fatal("system message mentions can be null")
	}
}
