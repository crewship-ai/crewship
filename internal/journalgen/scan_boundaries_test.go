package journalgen

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func sourceFile(t *testing.T, root, name, src string) string {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(src), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestScanReportsFileAndDeclarationFailures(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct{ name, src, want string }{
		{"syntax", "package journal\nconst =", "parse"},
		{"integer", `package journal; const Bad EntryType = 42`, "not a string literal"},
		{"wrong-call", `package journal; var Bad EntryType = other("value")`, "neither a string literal"},
		{"conversion-number", `package journal; var Bad EntryType = EntryType(42)`, "not a string literal"},
		{"conversion-expression", `package journal; var Bad EntryType = EntryType(prefix + "suffix")`, "not a string literal"},
		{"conversion-arity", `package journal; var Bad EntryType = EntryType("one", "two")`, "neither a string literal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := sourceFile(t, root, tc.name+".go", tc.src)
			if rows, err := Scan(path); err == nil || rows != nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("invalid declaration not diagnosed: %#v %v", rows, err)
			}
		})
	}
	if _, err := Scan(filepath.Join(root, "missing.go")); err == nil {
		t.Fatal("missing file accepted")
	}
	path := sourceFile(t, root, "valid.go", `package journal; const Valid EntryType = EntryType("valid.event")`)
	rows, err := Scan(path)
	if err != nil || len(rows) != 2 || !strings.Contains(rows[0].Pos, "valid.go:") {
		t.Fatalf("conversion scan: %#v %v", rows, err)
	}
}
func TestScanTreeRefusesPartialResults(t *testing.T) {
	for _, tc := range []struct{ name, src string }{{"syntax", `package journal; const =`}, {"declaration", `package journal; const Bad EntryType = 42`}} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			sourceFile(t, root, "a.go", `package journal; const Good EntryType = "good.event"`)
			sourceFile(t, root, "z.go", tc.src)
			if rows, err := ScanTree(root, "."); err == nil || rows != nil {
				t.Fatalf("partial registry emitted: %#v %v", rows, err)
			}
		})
	}
	root := t.TempDir()
	if rows, err := ScanTree(root, "missing"); err == nil || rows != nil {
		t.Fatalf("missing root emitted registry: %#v %v", rows, err)
	}
	if err := os.Symlink(filepath.Join(root, "missing"), filepath.Join(root, "broken.go")); err != nil {
		t.Fatal(err)
	}
	if rows, err := ScanTree(root, "."); err == nil || rows != nil || !strings.Contains(err.Error(), "read") {
		t.Fatalf("unreadable source emitted registry: %#v %v", rows, err)
	}
}
func TestScanTreeSkipsNonProductionTrees(t *testing.T) {
	root := t.TempDir()
	sourceFile(t, root, "internal/main.go", `package journal; const Good EntryType = "good.event"`)
	for _, name := range []string{"internal/.cache/broken.go", "internal/node_modules/broken.go", "internal/testdata/broken.go", "internal/vendor/broken.go", "internal/example_test.go", "internal/notes.txt"} {
		sourceFile(t, root, name, "not valid Go")
	}
	rows, err := ScanTree(root, "internal")
	if err != nil || len(rows) != 1 || rows[0].Value != "good.event" || filepath.IsAbs(strings.Split(rows[0].Pos, ":")[0]) {
		t.Fatalf("production tree scan: %#v %v", rows, err)
	}
}
func TestScanMixedConstBlockDoesNotInheritOldEntryType(t *testing.T) {
	path := sourceFile(t, t.TempDir(), "mixed.go", `package journal
 const (
  Event EntryType = "event.real"
  Count = 42
  RepeatedCount
 )`)
	rows, err := Scan(path)
	if err != nil || len(rows) != 1 || rows[0].Name != "Event" {
		t.Fatalf("unrelated constant inherited entry type: %#v %v", rows, err)
	}
}
func TestRegistrySelectionIsStableAcrossRootOrder(t *testing.T) {
	root := t.TempDir()
	sourceFile(t, root, "a/events.go", `package api; const First = journal.EntryType("event.shared"); const Other journal.EntryType = "z.event"`)
	sourceFile(t, root, "b/events.go", `package api; const Second = journal.EntryType("event.shared"); func f(){ _ = journal.EntryType("event.shared") }`)
	one, err := ScanTree(root, "a", "b")
	if err != nil {
		t.Fatal(err)
	}
	two, err := ScanTree(root, "b", "a")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(one, two) || len(one) != 2 || one[0].Name != "First" || one[0].InJournalPkg {
		t.Fatalf("root-order-dependent registry: %#v %#v", one, two)
	}
}
func TestRepoRootSearchFromNestedAndUnrelatedDirectories(t *testing.T) {
	root := t.TempDir()
	sourceFile(t, root, "go.mod", "module test.invalid/fixture\n")
	nested := filepath.Join(root, "internal", "nested")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nested)
	got, err := RepoRoot()
	if err != nil || got != root {
		t.Fatalf("nested lookup: %s %v", got, err)
	}
	t.Chdir(t.TempDir())
	if got, err := RepoRoot(); err == nil || got != "" {
		t.Fatalf("unrelated directory found module: %q %v", got, err)
	}
}
