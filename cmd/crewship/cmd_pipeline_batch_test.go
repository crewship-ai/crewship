package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadBatchItems_JSONLAndArray(t *testing.T) {
	dir := t.TempDir()

	jsonl := filepath.Join(dir, "b.jsonl")
	if err := os.WriteFile(jsonl, []byte("{\"x\":1}\n\n{\"x\":2}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	items, err := readBatchItems(jsonl, []string{"prod"}, `{"src":"test"}`)
	if err != nil {
		t.Fatalf("jsonl: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("jsonl: want 2 items (blank line skipped), got %d", len(items))
	}
	if _, ok := items[0]["inputs"]; !ok {
		t.Fatal("item missing inputs key")
	}
	if tags, ok := items[0]["tags"].([]string); !ok || len(tags) != 1 || tags[0] != "prod" {
		t.Fatalf("item tags not applied: %v", items[0]["tags"])
	}
	if _, ok := items[0]["metadata"]; !ok {
		t.Fatal("item missing metadata key")
	}

	arr := filepath.Join(dir, "b.json")
	if err := os.WriteFile(arr, []byte(`[{"x":1},{"x":2},{"x":3}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	items, err = readBatchItems(arr, nil, "")
	if err != nil {
		t.Fatalf("array: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("array: want 3 items, got %d", len(items))
	}
}

func TestReadBatchItems_Errors(t *testing.T) {
	if _, err := readBatchItems("/nonexistent/path", nil, ""); err == nil {
		t.Fatal("expected error for missing file")
	}
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.jsonl")
	if err := os.WriteFile(empty, []byte("\n  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readBatchItems(empty, nil, ""); err == nil {
		t.Fatal("expected error for empty batch file")
	}
}
