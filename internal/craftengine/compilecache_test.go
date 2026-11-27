//go:build !windows

package craftengine

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/tetratelabs/wazero"
)

func withCompileCache(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "cache")
	if err := EnableCompileCache(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		compileCacheMu.Lock()
		compileCache = nil
		compileCacheMu.Unlock()
	})
	return root
}

func runHello(t *testing.T, p *Process, want string) {
	t.Helper()
	var out, errb bytes.Buffer
	if err := p.Run(context.Background(), t.TempDir(), []string{"convert"}, &out, &errb); err != nil || out.String() != want || errb.Len() != 0 {
		t.Fatalf("%q %q %v", out.String(), errb.String(), err)
	}
}

func cacheFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	_ = filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			files[p] = fi.Mode().String() + fi.ModTime().String()
		}
		return nil
	})
	return files
}

// The server compiles a module once into its private directory; workers read that entry and
// never write the directory.
func TestCompileCacheIsFilledOnceByTheServerAndOnlyReadByWorkers(t *testing.T) {
	root := withCompileCache(t)
	p := newTestProcess(t, wasiPrinting("hello\n"), Limits{})
	runHello(t, p, "hello\n")
	first := cacheFiles(t, root)
	if len(first) != 1 {
		t.Fatalf("entries: %v", first)
	}
	for path, mode := range first {
		if mode[:10] != "-r--------" {
			t.Fatalf("%s is %s", path, mode)
		}
	}
	if info, _ := os.Stat(root); info.Mode().Perm() != 0o700 {
		t.Fatalf("root is %v", info.Mode())
	}
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ { // two workers at once
		wg.Add(1)
		go func() { defer wg.Done(); runHello(t, p, "hello\n") }()
	}
	wg.Wait()
	if after := cacheFiles(t, root); len(after) != 1 || after[firstKey(first)] != first[firstKey(first)] {
		t.Fatalf("a worker changed the cache: %v -> %v", first, after)
	}
}

func firstKey(m map[string]string) string {
	for k := range m {
		return k
	}
	return ""
}

// A verified entry is used; a changed or unreadable one is not, and the worker compiles by itself.
func TestCompileCacheEntryIsVerifiedAndAChangedOneFallsBackSilently(t *testing.T) {
	root := withCompileCache(t)
	module := wasiPrinting("hello\n")
	p := newTestProcess(t, module, Limits{})
	runHello(t, p, "hello\n")
	if err := p.materialize(); err != nil {
		t.Fatal(err)
	}
	entry, rel, hash, ok := activeCompileCache().entry(p.sha, p.path)
	if !ok {
		t.Fatal("no entry")
	}
	ctx := context.Background()
	if rt, _, used := compileFromEntry(ctx, wazero.NewRuntimeConfig(), module, entry, rel, hash); !used {
		t.Fatal("a verified entry was not used")
	} else {
		_ = rt.Close(ctx)
	}
	_ = os.Chmod(entry, 0o600)
	raw, _ := os.ReadFile(entry)
	raw[len(raw)/2] ^= 0xff
	if err := os.WriteFile(entry, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, used := compileFromEntry(ctx, wazero.NewRuntimeConfig(), module, entry, rel, hash); used {
		t.Fatal("a changed entry was used")
	}
	runHello(t, p, "hello\n") // still answers, no error and nothing on stderr
	if err := os.Remove(entry); err != nil {
		t.Fatal(err)
	}
	runHello(t, p, "hello\n")
	_ = root
}

// Another module (another SHA) gets its own entry.
func TestCompileCacheKeepsOneEntryPerModule(t *testing.T) {
	root := withCompileCache(t)
	runHello(t, newTestProcess(t, wasiPrinting("hello\n"), Limits{}), "hello\n")
	runHello(t, newTestProcess(t, wasiPrinting("other\n"), Limits{}), "other\n")
	if n := len(cacheFiles(t, root)); n != 2 {
		t.Fatalf("%d entries", n)
	}
}

// A directory that others may use is refused, and what an earlier process left is removed.
func TestCompileCacheRefusesAShareableDirectoryAndStartsEmpty(t *testing.T) {
	root := filepath.Join(t.TempDir(), "open")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := NewCompileCache(root); err == nil {
		t.Fatal("a 0755 directory was accepted")
	}
	_ = os.Chmod(root, 0o700)
	_ = os.WriteFile(filepath.Join(root, "left-over"), []byte("x"), 0o600)
	if _, err := NewCompileCache(root); err != nil {
		t.Fatal(err)
	}
	if list, _ := os.ReadDir(root); len(list) != 0 {
		t.Fatalf("left over: %v", list)
	}
}
