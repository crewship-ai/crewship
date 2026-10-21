package main

import "testing"

func TestChatDocumentLinksContractIsClosedLatestScope(t *testing.T) {
	catalog := documentSchemaCatalog()
	contract, ok := catalog["GET /api/v1/chats/{chatId}/links"]
	if !ok || contract.Response["$ref"] != "#/components/schemas/ChatDocumentLinks" {
		t.Fatal("missing explicitly scoped chat Document links contract")
	}
	components := documentSchemaComponents()
	response, ok := components["ChatDocumentLinks"].(map[string]any)
	if !ok || response["additionalProperties"] != false {
		t.Fatal("links response must be closed")
	}
	props := response["properties"].(map[string]any)
	if got := props["scope"].(map[string]any)["enum"].([]string); len(got) != 1 || got[0] != "latest_native_documents" {
		t.Fatalf("unsupported output families must not appear complete: %#v", got)
	}
	links := props["links"].(map[string]any)
	if links["maxItems"] != 20 {
		t.Fatal("links must remain bounded")
	}
	item := links["items"].(map[string]any)
	if item["additionalProperties"] != false || len(item["required"].([]string)) != 6 {
		t.Fatal("each link must be a closed six-field DTO")
	}
	fields := item["properties"].(map[string]any)
	if len(fields) != 6 || fields["revision"] != nil || fields["checkpoint_id"] != nil || fields["content"] != nil {
		t.Fatal("latest links must not fabricate a revision pin or disclose content")
	}
	for field, want := range map[string]string{"mode": "latest", "kind": "artifact"} {
		if got := fields[field].(map[string]any)["enum"].([]string); len(got) != 1 || got[0] != want {
			t.Fatalf("%s contract = %#v", field, got)
		}
	}
}
