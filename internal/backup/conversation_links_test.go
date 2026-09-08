package backup

import (
	"encoding/json"
	"testing"
)

func TestForkRewritesLegacyConversationLinksOnlyForMappedConversation(t *testing.T) {
	for _, oldURL := range []string{"/conversations?conversation=old-conversation", "/chat?conversation=old-conversation"} {
		t.Run(oldURL, func(t *testing.T) {
			payload, _ := json.Marshal(map[string]any{"conversation_id": "old-conversation", "chat_url": oldURL, "last_sequence": 3})
			legacyAgent := `{"chat_url":"/chat/ma-ena?session=legacy-session"}`
			unrelated := `{"conversation_id":"other-workspace","chat_url":"/conversations?conversation=other-workspace"}`
			dump := &DBDump{Tables: map[string][]map[string]any{"inbox_items": {{"id": "human", "kind": "message", "target_user_id": "recipient", "source_id": "conversation_old-conversation_recipient", "payload_json": string(payload)}, {"id": "agent", "kind": "message", "payload_json": legacyAgent}, {"id": "other", "kind": "message", "payload_json": unrelated}}}}
			if err := remapConversationMetadata(dump, map[string]map[string]string{"workspace_conversations": {"old-conversation": "new-conversation"}}); err != nil {
				t.Fatal(err)
			}
			row := dump.Tables["inbox_items"][0]
			var got map[string]any
			if err := json.Unmarshal([]byte(row["payload_json"].(string)), &got); err != nil {
				t.Fatal(err)
			}
			if got["chat_url"] != "/chat?conversation=new-conversation" || got["conversation_id"] != "new-conversation" || row["source_id"] != "conversation_new-conversation_recipient" || got["last_sequence"] != float64(3) {
				t.Fatalf("incorrect fork metadata %+v %+v", row, got)
			}
			if dump.Tables["inbox_items"][1]["payload_json"] != legacyAgent || dump.Tables["inbox_items"][2]["payload_json"] != unrelated {
				t.Fatal("rewrote legacy agent or unrelated workspace link")
			}
		})
	}
}
