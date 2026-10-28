package artifacts

import "testing"

func TestProjectIntentRejectsNoncanonicalOrMixedScope(t *testing.T) {
	for _, fields := range []map[string]any{
		{"Submission_ID": "one"},
		{"submission_id": "one", "Submission_ID": "other"},
		{"submission_id": "one", "human_user_id": "forged"},
		{"submission_id": "one", "comment_id": "comment", "base_version": 1},
		{"submission_id": "one", "page_id": "page"},
	} {
		if _, err := ProjectIntentFromMetadata(map[string]any{ProjectIntentMetadataKey: fields}); err == nil {
			t.Errorf("invalid explicit purpose accepted: %+v", fields)
		}
	}
	if got, err := ProjectIntentFromMetadata(map[string]any{ProjectIntentMetadataKey: map[string]any{"submission_id": "one", "page_id": "page", "base_version": 1}}); err != nil || got == nil || got.PageID != "page" {
		t.Fatal("positive calibration", got, err)
	}
}

func TestProjectIntentTestOriginIsExplicitAndCreationOnly(t *testing.T) {
	creation := map[string]any{"submission_id": "qa-create", "created_by_test": true}
	if got, err := ProjectIntentFromMetadata(map[string]any{ProjectIntentMetadataKey: creation}); err != nil || got == nil {
		t.Fatalf("explicit human test creation rejected: %#v %v", got, err)
	}
	for _, fields := range []map[string]any{
		{"submission_id": "qa-edit", "page_id": "page", "base_version": 1, "created_by_test": true},
		{"submission_id": "qa-comment", "page_id": "page", "base_version": 1, "comment_id": "comment", "created_by_test": true},
		{"submission_id": "qa-create", "created_by_test": "true"},
		{"submission_id": "qa-create", "created_by_test": nil},
		{"submission_id": "qa-create", "Created_By_Test": true},
	} {
		if _, err := ProjectIntentFromMetadata(map[string]any{ProjectIntentMetadataKey: fields}); err == nil {
			t.Fatalf("noncanonical/mixed test purpose accepted: %#v", fields)
		}
	}
}
