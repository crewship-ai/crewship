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
