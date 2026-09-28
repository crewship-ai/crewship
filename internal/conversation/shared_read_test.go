package conversation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadSharedBoundsAndFailures(t *testing.T) {
	store := NewStore(t.TempDir(), nil)
	path := filepath.Join(store.basePath, "conversations", "share.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	read := func(maxBytes int64, maxRows int) ([]Message, error) {
		return store.ReadShared(context.Background(), "share", maxBytes, maxRows)
	}
	if got, err := read(100, 2); err != nil || len(got) != 0 {
		t.Fatalf("missing file = %+v, %v", got, err)
	}
	first := `{"id":"a","role":"user","content":"hello"}` + "\n"
	second := `{"id":"b","role":"assistant","content":"world"}` + "\n"
	if err := os.WriteFile(path, []byte(first+second), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := read(int64(len(first+second)), 2); err != nil || len(got) != 2 {
		t.Fatalf("exact bound = %+v, %v", got, err)
	}
	if got, err := read(int64(len(first+second)-1), 2); !errors.Is(err, ErrSharedLimit) || got != nil {
		t.Fatalf("byte limit returned partial data: %+v, %v", got, err)
	}
	if got, err := read(100, 1); !errors.Is(err, ErrSharedLimit) || got != nil {
		t.Fatalf("row limit returned partial data: %+v, %v", got, err)
	}
	if err := os.WriteFile(path, []byte(first+`{"id":`), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := read(100, 2); err == nil || errors.Is(err, ErrSharedLimit) || got != nil {
		t.Fatalf("corrupt row did not fail closed: %+v, %v", got, err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("a", 101)), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := read(100, 2); !errors.Is(err, ErrSharedLimit) || got != nil {
		t.Fatalf("long line did not hit limit: %+v, %v", got, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.ReadShared(cancelled, "share", 100, 2); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read = %v", err)
	}
}
