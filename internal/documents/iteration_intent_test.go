package documents

import (
	"encoding/json"
	"testing"
)

func TestIterationIntentClosedOriginalTarget(t *testing.T) {
	valid := `{"submission_id":"send-one","page_id":"page-one","schema":3,"rev":7,"generation":2}`
	for _, raw := range []string{valid, `{"submission_id":"s","page_id":"p","schema":3,"rev":0,"generation":0}`} {
		var metadata map[string]any
		if err := json.Unmarshal([]byte(`{"document_iteration_intent":`+raw+`}`), &metadata); err != nil {
			t.Fatal(err)
		}
		got, err := IterationIntentFromMetadata(metadata)
		if err != nil || got == nil || got.Validate() != nil {
			t.Fatalf("valid target lost: %+v %v", got, err)
		}
	}
	for _, raw := range []string{
		`null`, `[]`, `"page"`, `{}`,
		`{"submission_id":"s","page_id":"p","schema":3,"rev":null,"generation":0}`,
		`{"submission_id":"s","page_id":"p","schema":3,"rev":0,"generation":null}`, `{"submission_id":"s","page_id":"p","schema":3,"rev":0}`,
		`{"submission_id":"s","page_id":"p","schema":3,"rev":-1,"generation":0}`,
		`{"submission_id":"s","page_id":"p","schema":3,"rev":0.5,"generation":0}`,
		`{"submission_id":"s","page_id":"p","schema":2,"rev":0,"generation":0}`,
		`{"submission_id":"s","page_id":"p","schema":3,"rev":0,"generation":0,"human_user_id":"other"}`,
		`{"submission_id":"s","page_id":"p","schema":3,"rev":9007199254740992,"generation":0}`,
	} {
		t.Run(raw, func(t *testing.T) {
			var metadata map[string]any
			_ = json.Unmarshal([]byte(`{"document_iteration_intent":`+raw+`}`), &metadata)
			if _, err := IterationIntentFromMetadata(metadata); err == nil {
				t.Fatal("unclosed target admitted")
			}
		})
	}
	if got, err := IterationIntentFromMetadata(nil); got != nil || err != nil {
		t.Fatal("ordinary message gained purpose")
	}
}
