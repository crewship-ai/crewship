package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/conversation"
)

func TestChatSourceProjectionHasDistinctTypedResponse(t *testing.T) {
	for _, path := range []string{"/api/v1/chats/{chatId}/messages", "/api/v1/conversations/{conversationId}/messages"} {
		t.Run(path, func(t *testing.T) {
			doc := buildDocument([]route{{method: "GET", path: path, auth: true}})
			components := doc["components"].(map[string]any)["schemas"].(map[string]any)
			if _, ok := components["ChatSourceMessages"]; !ok {
				t.Fatal("source_v1 response is missing its typed schema")
			}
			raw, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			schema := compileAt(t, raw, "/paths/"+esc(path)+"/get/responses/200/content/"+esc("application/json")+"/schema")
			good := map[string]any{"contract_version": float64(1), "conversation_ref": map[string]any{"workspace_id": "w", "source_kind": "agent_session", "source_id": "chat"}, "messages": []any{}, "snapshot_complete": true}
			if err := schema.Validate(good); err != nil {
				t.Fatalf("valid source response rejected (including overlapping oneOf branches): %v", err)
			}
			good["contract_version"] = float64(2)
			if err := schema.Validate(good); err == nil {
				t.Fatal("future source contract fell through the permissive legacy branch")
			}
			good["contract_version"] = float64(1)
			good["conversation_ref"].(map[string]any)["source_kind"] = "filesystem_path"
			if err := schema.Validate(good); err == nil {
				t.Fatal("unregistered source kind accepted")
			}
			if err := schema.Validate(map[string]any{"messages": []any{}, "has_more": false}); err != nil {
				t.Fatalf("legacy response rejected: %v", err)
			}
		})
	}
}

func TestChatSourceSchemaFieldsMatchDeliveredDTOs(t *testing.T) {
	doc := buildDocument([]route{{method: "GET", path: "/api/v1/chats/{chatId}/messages", auth: true}})
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	for name, value := range map[string]any{"ChatSourceMessages": conversation.SourceMessagesResponse{}, "ChatSourceConversationRef": conversation.ConversationRef{}, "ChatSourceMessageRef": conversation.MessageRef{}, "ChatSourceMessage": conversation.SourceMessage{}, "ChatSourceAuthor": conversation.SourceAuthor{}, "ChatSourceOrder": conversation.SourceOrder{}} {
		t.Run(name, func(t *testing.T) {
			schema, ok := schemas[name].(map[string]any)
			if !ok {
				t.Fatal("missing typed source DTO")
			}
			properties := schema["properties"].(map[string]any)
			required := map[string]bool{}
			keys, _ := schema["required"].([]string)
			for _, key := range keys {
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
					t.Errorf("required mismatch %s", key)
				}
			}
			if len(properties) != typ.NumField() {
				t.Fatal("schema invents source fields")
			}
		})
	}
}

func TestChatSourceUnknownLegacyIdentityRemainsNullable(t *testing.T) {
	doc := buildDocument([]route{{method: "GET", path: "/api/v1/chats/{chatId}/messages", auth: true}})
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	message, ok := schemas["ChatSourceMessage"].(map[string]any)
	if !ok {
		t.Fatal("missing typed source message")
	}
	fields := message["properties"].(map[string]any)
	for _, name := range []string{"message_ref", "role", "author", "occurred_at", "reply_to", "thread_root", "alternative_parent"} {
		field := fields[name].(map[string]any)
		if name, ok := field["$ref"].(string); ok {
			field = schemas[strings.TrimPrefix(name, "#/components/schemas/")].(map[string]any)
		}
		if field["nullable"] != true {
			t.Errorf("unknown %s cannot be null", name)
		}
	}
	part := schemas["ChatSourcePart"].(map[string]any)
	if _, ok := part["properties"].(map[string]any)["metadata"]; ok || part["additionalProperties"] != false {
		t.Fatal("projection advertises private legacy metadata")
	}
}
