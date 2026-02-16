package bbolt

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func tempDB(t *testing.T) *Provider {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	p, err := New(path)
	if err != nil {
		t.Fatalf("new bbolt provider: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func TestSetAndGet(t *testing.T) {
	p := tempDB(t)
	ctx := context.Background()

	if err := p.Set(ctx, "runs", "run-1", []byte(`{"status":"running"}`)); err != nil {
		t.Fatal(err)
	}

	val, err := p.Get(ctx, "runs", "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if string(val) != `{"status":"running"}` {
		t.Fatalf("unexpected value: %s", val)
	}
}

func TestGetMissingBucket(t *testing.T) {
	p := tempDB(t)
	val, err := p.Get(context.Background(), "nonexistent", "key")
	if err != nil {
		t.Fatal(err)
	}
	if val != nil {
		t.Fatalf("expected nil, got %s", val)
	}
}

func TestGetMissingKey(t *testing.T) {
	p := tempDB(t)
	ctx := context.Background()
	_ = p.Set(ctx, "runs", "exists", []byte("data"))

	val, err := p.Get(ctx, "runs", "missing")
	if err != nil {
		t.Fatal(err)
	}
	if val != nil {
		t.Fatalf("expected nil, got %s", val)
	}
}

func TestDelete(t *testing.T) {
	p := tempDB(t)
	ctx := context.Background()
	_ = p.Set(ctx, "runs", "run-1", []byte("data"))

	if err := p.Delete(ctx, "runs", "run-1"); err != nil {
		t.Fatal(err)
	}

	val, err := p.Get(ctx, "runs", "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if val != nil {
		t.Fatalf("expected nil after delete, got %s", val)
	}
}

func TestDeleteMissingBucket(t *testing.T) {
	p := tempDB(t)
	if err := p.Delete(context.Background(), "nonexistent", "key"); err != nil {
		t.Fatal(err)
	}
}

func TestList(t *testing.T) {
	p := tempDB(t)
	ctx := context.Background()
	_ = p.Set(ctx, "runs", "a", []byte("1"))
	_ = p.Set(ctx, "runs", "b", []byte("2"))
	_ = p.Set(ctx, "runs", "c", []byte("3"))

	result, err := p.List(ctx, "runs")
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 items, got %d", len(result))
	}
	if string(result["b"]) != "2" {
		t.Fatalf("expected '2', got '%s'", result["b"])
	}
}

func TestListByPrefix(t *testing.T) {
	p := tempDB(t)
	ctx := context.Background()
	_ = p.Set(ctx, "runs", "team-a:run-1", []byte("1"))
	_ = p.Set(ctx, "runs", "team-a:run-2", []byte("2"))
	_ = p.Set(ctx, "runs", "team-b:run-1", []byte("3"))

	result, err := p.ListByPrefix(ctx, "runs", "team-a:")
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 items, got %d", len(result))
	}
}

func TestNewCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "dir")
	path := filepath.Join(dir, "test.db")
	p, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	p.Close()

	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Fatal("expected directory to be created")
	}
}
