package documents

import (
	"encoding/json"
	"testing"
)

func TestCreationIntentMetadataIsClosed(t *testing.T) {
	for _, value := range []any{nil, true, "id", map[string]any{}, map[string]any{"submission_id": ""}, map[string]any{"submission_id": "../id"}, map[string]any{"submission_id": "ok", "owner_user_id": "other"}} {
		if _, err := CreationIntentFromMetadata(map[string]any{CreationMetadataKey: value}); err == nil {
			t.Fatalf("accepted malformed consent %#v", value)
		}
	}
	if value, err := CreationIntentFromMetadata(nil); err != nil || value != nil {
		t.Fatal("absent consent changed")
	}
	value, err := CreationIntentFromMetadata(map[string]any{CreationMetadataKey: map[string]any{"submission_id": "owned-123"}, "forged": "ignored"})
	if err != nil || value.SubmissionID != "owned-123" {
		t.Fatalf("valid consent: %v %v", value, err)
	}
}

func TestCreationIntentWireRejectsUnknownFields(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `{"submission_id":"ok","owner_user_id":"other"}`, `{"submission_id":"../bad"}`} {
		var value CreationIntent
		if json.Unmarshal([]byte(raw), &value) == nil {
			t.Fatalf("accepted intent %s", raw)
		}
	}
}
