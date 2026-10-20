package documents

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeIterationIntentClosedOriginalSelection(t *testing.T) {
	good := `{"submission_id":"followup","page_id":"original","schema":3,"rev":0,"generation":0}`
	want := IterationIntent{SubmissionID: "followup", PageID: "original", Schema: 3}
	if got, err := DecodeIterationIntent([]byte(good)); err != nil || got != want {
		t.Fatalf("valid original selection: %+v %v", got, err)
	}
	for _, bad := range []string{
		"null", "[]", good + `{}`, strings.Repeat(" ", 1025) + good,
		strings.Replace(good, `"page_id":"original"`, `"page_id":"original","page_id":"other"`, 1),
		strings.Replace(good, `"page_id":"original"`, `"Page_ID":"original"`, 1),
		strings.Replace(good, `"page_id":"original"`, `"page_id":"original","mode":"DIRECT"`, 1),
		strings.Replace(good, `"rev":0`, `"rev":null`, 1),
		strings.Replace(good, `"schema":3`, `"schema":2`, 1),
		strings.Replace(good, `"generation":0`, `"generation":-1`, 1),
		strings.Replace(good, `"rev":0`, `"rev":1.5`, 1),
		strings.Replace(good, `"rev":0,`, ``, 1),
		strings.Replace(good, "original", string([]byte{0xff}), 1),
	} {
		if _, err := DecodeIterationIntent([]byte(bad)); err == nil {
			t.Fatalf("malformed or widened selection accepted: %q", bad)
		}
	}
}

func TestIterationIntentFromMetadataUsesOnlyClosedTarget(t *testing.T) {
	if got, err := IterationIntentFromMetadata(map[string]any{"page_context": map[string]any{"slug": "context-only"}}); err != nil || got != nil {
		t.Fatalf("context alone became a mandate: %+v %v", got, err)
	}
	object := map[string]any{"submission_id": "followup", "page_id": "original", "schema": float64(3), "rev": float64(0), "generation": float64(0)}
	got, err := IterationIntentFromMetadata(map[string]any{IterationMetadataKey: object, "human_id": "ignored-forgery"})
	if err != nil || got == nil || got.PageID != "original" {
		t.Fatalf("explicit selection not decoded: %+v %v", got, err)
	}
	object["page_id"] = "changed"
	if got.PageID != "original" {
		t.Fatal("caller mutation retargeted captured selection")
	}
	for _, value := range []any{nil, "original", []any{}, map[string]any{}, map[string]any{"submission_id": "followup", "page_id": "original", "schema": 3, "rev": 0, "generation": 0, "mode": "DIRECT"}, map[string]any{"submission_id": "followup", "page_id": "original", "schema": 3, "rev": json.Number("0.5"), "generation": 0}} {
		if _, err := IterationIntentFromMetadata(map[string]any{IterationMetadataKey: value}); err == nil {
			t.Fatalf("invalid metadata became a mandate: %#v", value)
		}
	}
}
