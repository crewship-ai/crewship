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

func TestSharedReadRejectsInvalidBoundsAndSourceKinds(t *testing.T) {
	store := NewStore(t.TempDir(), nil)
	for _, tc := range []struct {
		name, id string
		bytes    int64
		rows     int
	}{
		{"traversal", "../secret", 100, 2}, {"zero-bytes", "share", 0, 2},
		{"too-many-bytes", "share", 16<<20 + 1, 2}, {"zero-rows", "share", 100, 0}, {"too-many-rows", "share", 100, 1001},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := store.ReadShared(context.Background(), tc.id, tc.bytes, tc.rows)
			if err == nil || got != nil {
				t.Fatalf("invalid read exposed data: %+v %v", got, err)
			}
		})
	}
	path := filepath.Join(store.basePath, "conversations", "share.jsonl")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if got, err := store.ReadShared(context.Background(), "share", 100, 2); err == nil || got != nil {
		t.Fatalf("directory returned transcript: %+v %v", got, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := store.ReadShared(context.Background(), "share", 100, 2); err == nil || got != nil {
		t.Fatalf("blank row returned transcript: %+v %v", got, err)
	}
}

func TestCountingReaderHonorsCancellationBeforeTouchingSource(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source := strings.NewReader("secret")
	reader := sharedCountingReader{ctx: ctx, r: source}
	if n, err := reader.Read(make([]byte, 16)); n != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read=%d,%v", n, err)
	}
	if source.Len() != len("secret") || reader.n != 0 {
		t.Fatal("canceled reader consumed source bytes")
	}
}
