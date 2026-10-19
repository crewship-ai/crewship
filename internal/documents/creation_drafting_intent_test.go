package documents

import (
	"encoding/json"
	"testing"
)

func TestCreationDraftingPurposeWire(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		allowed   bool
		drafting  bool
	}{
		{"legacy", `{"submission_id":"original"}`, true, false},
		{"explicit drafting", `{"submission_id":"original","drafting":true}`, true, true},
		{"explicit one shot", `{"submission_id":"original","drafting":false}`, true, false},
		{"null purpose", `{"submission_id":"original","drafting":null}`, false, false},
		{"string purpose", `{"submission_id":"original","drafting":"true"}`, false, false},
		{"numeric purpose", `{"submission_id":"original","drafting":1}`, false, false},
		{"extra authority", `{"submission_id":"original","drafting":true,"page_id":"other"}`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var intent CreationIntent
			err := json.Unmarshal([]byte(tc.raw), &intent)
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%t, error=%v", tc.allowed, err)
			}
			if err != nil {
				return
			}
			encoded, err := json.Marshal(intent)
			if err != nil {
				t.Fatal(err)
			}
			var value map[string]any
			if err := json.Unmarshal(encoded, &value); err != nil {
				t.Fatal(err)
			}
			if got, _ := value["drafting"].(bool); got != tc.drafting {
				t.Fatalf("drafting purpose was not preserved: %v", value)
			}
		})
	}
}

func TestCreationDraftingPurposeMetadata(t *testing.T) {
	for _, tc := range []struct {
		name    string
		value   map[string]any
		allowed bool
	}{
		{"legacy", map[string]any{"submission_id": "original"}, true},
		{"drafting", map[string]any{"submission_id": "original", "drafting": true}, true},
		{"one shot", map[string]any{"submission_id": "original", "drafting": false}, true},
		{"null", map[string]any{"submission_id": "original", "drafting": nil}, false},
		{"text", map[string]any{"submission_id": "original", "drafting": "true"}, false},
		{"extra", map[string]any{"submission_id": "original", "drafting": true, "run_id": "other"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			intent, err := CreationIntentFromMetadata(map[string]any{CreationMetadataKey: tc.value})
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%t, intent=%v, error=%v", tc.allowed, intent, err)
			}
			if err == nil {
				encoded, err := json.Marshal(intent)
				if err != nil {
					t.Fatal(err)
				}
				var value map[string]any
				if err := json.Unmarshal(encoded, &value); err != nil {
					t.Fatal(err)
				}
				if value["drafting"] != nil && value["drafting"] != tc.value["drafting"] {
					t.Fatal("drafting purpose changed")
				}
				if tc.value["drafting"] == true && value["drafting"] != true {
					t.Fatal("explicit drafting purpose disappeared")
				}
			}
		})
	}
}
