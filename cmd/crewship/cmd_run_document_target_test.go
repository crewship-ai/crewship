package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadRunDocumentTargetClosedBounded(t *testing.T) {
	for _, raw := range []string{`{"submission_id":"one","page_id":"same","schema":3,"rev":7,"generation":2}`, `{"page_id":"same","schema":3,"rev":7,"generation":2,"submission_id":"one","user":"other"}`, `{"submission_id":"one","page_id":"same","schema":3,"rev":7}`, `{"submission_id":"one","page_id":"same","schema":3,"rev":7,"rev":8,"generation":2}`} {
		path := filepath.Join(t.TempDir(), "target.json")
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		intent, err := readRunDocumentTarget(path)
		want := raw == `{"submission_id":"one","page_id":"same","schema":3,"rev":7,"generation":2}`
		if (err == nil) != want || (want && (intent == nil || intent.PageID != "same" || intent.Rev != 7)) {
			t.Fatalf("target=%+v err=%v", intent, err)
		}
	}
}
