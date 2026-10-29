package artifacts

import "testing"

func TestDesignIntentRequiresExactPageAndClosedKind(t *testing.T) {
	for _, kind := range []string{"design", "unknown"} {
		intent, err := ProjectIntentFromMetadata(map[string]any{ProjectIntentMetadataKey: map[string]any{"submission_id": "design-one", "kind": kind, "page_id": "design-page", "base_version": 1}})
		if kind == "design" && (err != nil || intent == nil) {
			t.Fatalf("valid Design intent denied: %v", err)
		}
		if kind != "design" && err == nil {
			t.Fatal("unknown kind accepted")
		}
	}
	if _, err := ProjectIntentFromMetadata(map[string]any{ProjectIntentMetadataKey: map[string]any{"submission_id": "design-one", "kind": "design"}}); err == nil {
		t.Fatal("unscoped Design creation allowed")
	}
}
