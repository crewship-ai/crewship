package api

import (
	"encoding/json"
	"os"
	"testing"
)

func TestOpenAPIChatShareUsesOnlyDedicatedBearer(t *testing.T) {
	raw, err := os.ReadFile("openapi.gen.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components struct {
			SecuritySchemes map[string]json.RawMessage `json:"securitySchemes"`
		} `json:"components"`
		Paths map[string]map[string]struct {
			Security []map[string][]string `json:"security"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc.Components.SecuritySchemes["chatShareBearer"]; !ok {
		t.Fatal("chatShareBearer scheme missing")
	}
	reader := doc.Paths["/api/v1/shared-chats/{shareId}/messages"]["get"]
	if len(reader.Security) != 1 || len(reader.Security[0]) != 1 {
		t.Fatalf("reader security = %#v, want only dedicated bearer", reader.Security)
	}
	if _, ok := reader.Security[0]["chatShareBearer"]; !ok {
		t.Fatalf("reader security = %#v, want chatShareBearer", reader.Security)
	}
	ordinary := doc.Paths["/api/v1/chats/{chatId}/messages"]["get"]
	for _, alternative := range ordinary.Security {
		if _, ok := alternative["chatShareBearer"]; ok {
			t.Fatal("share bearer advertised on ordinary chat route")
		}
	}
}
