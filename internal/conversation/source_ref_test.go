package conversation

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSourceSnapshotPreservesUnknownAndDoesNotInferRelations(t *testing.T) {
	ref := ConversationRef{WorkspaceID: "workspace", SourceKind: SourceAgentSession, SourceID: "chat"}
	rows := []Message{{Role: RoleUser, AgentID: "associated-agent", Content: "legacy", Metadata: map[string]any{"reply_to": "forged"}, Parts: []Part{{Type: "thinking", Content: "private legacy", Metadata: map[string]any{"private": "secret marker"}}, {Type: "text", Content: "visible"}}}}
	out, err := ProjectAgentSnapshot(ref, rows)
	if err != nil {
		t.Fatal(err)
	}
	item := out.Messages[0]
	if item.MessageRef != nil || item.Author != nil || item.OccurredAt != nil || item.ReplyTo != nil || item.ThreadRoot != nil || item.AlternativeParent != nil {
		t.Fatalf("invented legacy identity: %+v", item)
	}
	if item.Order.SourcePosition == nil || *item.Order.SourcePosition != 0 || item.Order.SourceSequence != nil {
		t.Fatal("snapshot position changed into cursor")
	}
	encoded, _ := json.Marshal(out)
	if strings.Contains(string(encoded), "private legacy") || strings.Contains(string(encoded), "secret marker") || strings.Contains(string(encoded), "forged") {
		t.Fatal("legacy private metadata escaped")
	}
	if rows[0].Parts[0].Content != "private legacy" || rows[0].Parts[0].Metadata == nil {
		t.Fatal("projection mutated persisted source")
	}
}
func TestSourceSnapshotRejectsAmbiguousIdentityAndBounds(t *testing.T) {
	ref := ConversationRef{WorkspaceID: "workspace", SourceKind: SourceAgentSession, SourceID: "chat"}
	for _, rows := range [][]Message{{{ID: "same"}, {ID: "same"}}, {{ID: "x", ChatID: "other"}}, make([]Message, 1001)} {
		if _, err := ProjectAgentSnapshot(ref, rows); err == nil {
			t.Fatal("ambiguous or oversized snapshot accepted")
		}
	}
}
