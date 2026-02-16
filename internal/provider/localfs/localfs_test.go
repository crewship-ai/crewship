package localfs

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func tempProvider(t *testing.T) *Provider {
	t.Helper()
	p, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("new localfs provider: %v", err)
	}
	return p
}

func TestWriteAndRead(t *testing.T) {
	p := tempProvider(t)
	ctx := context.Background()

	content := "hello world"
	if err := p.Write(ctx, "test.txt", bytes.NewReader([]byte(content))); err != nil {
		t.Fatal(err)
	}

	r, err := p.Read(ctx, "test.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	data, _ := io.ReadAll(r)
	if string(data) != content {
		t.Fatalf("expected %q, got %q", content, data)
	}
}

func TestWriteNestedPath(t *testing.T) {
	p := tempProvider(t)
	ctx := context.Background()

	if err := p.Write(ctx, "a/b/c.txt", bytes.NewReader([]byte("nested"))); err != nil {
		t.Fatal(err)
	}

	exists, err := p.Exists(ctx, "a/b/c.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("expected file to exist")
	}
}

func TestList(t *testing.T) {
	p := tempProvider(t)
	ctx := context.Background()

	if err := p.Write(ctx, "a.txt", bytes.NewReader([]byte("a"))); err != nil {
		t.Fatal(err)
	}
	if err := p.Write(ctx, "b.txt", bytes.NewReader([]byte("b"))); err != nil {
		t.Fatal(err)
	}
	if err := p.EnsureDir(ctx, "subdir"); err != nil {
		t.Fatal(err)
	}

	files, err := p.List(ctx, ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(files))
	}
}

func TestListEmpty(t *testing.T) {
	p := tempProvider(t)
	files, err := p.List(context.Background(), "nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if files != nil {
		t.Fatalf("expected nil, got %v", files)
	}
}

func TestDelete(t *testing.T) {
	p := tempProvider(t)
	ctx := context.Background()

	if err := p.Write(ctx, "del.txt", bytes.NewReader([]byte("delete me"))); err != nil {
		t.Fatal(err)
	}
	if err := p.Delete(ctx, "del.txt"); err != nil {
		t.Fatal(err)
	}

	exists, err := p.Exists(ctx, "del.txt")
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("expected file to not exist after delete")
	}
}

func TestPathTraversal(t *testing.T) {
	p := tempProvider(t)
	ctx := context.Background()

	_, err := p.Read(ctx, "../../etc/passwd")
	if err == nil {
		t.Fatal("expected path traversal error")
	}
}

func TestEnsureDir(t *testing.T) {
	p := tempProvider(t)
	ctx := context.Background()

	if err := p.EnsureDir(ctx, "a/b/c"); err != nil {
		t.Fatal(err)
	}

	full := filepath.Join(p.basePath, "a", "b", "c")
	info, err := os.Stat(full)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatal("expected directory")
	}
}

func TestExists(t *testing.T) {
	p := tempProvider(t)
	ctx := context.Background()

	exists, _ := p.Exists(ctx, "nope.txt")
	if exists {
		t.Fatal("expected false for missing file")
	}

	if err := p.Write(ctx, "yes.txt", bytes.NewReader([]byte("y"))); err != nil {
		t.Fatal(err)
	}
	exists, _ = p.Exists(ctx, "yes.txt")
	if !exists {
		t.Fatal("expected true for existing file")
	}
}
