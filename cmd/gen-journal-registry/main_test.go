package main

import (
	"bytes"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journalgen"
)

func generatorFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"internal/journal", "cmd/emitter"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module generator.invalid/fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	return root
}
func writeGeneratorSource(t *testing.T, root, path, src string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, path), []byte(src), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestMainGeneratesStableCompleteRegistry(t *testing.T) {
	root := generatorFixture(t)
	writeGeneratorSource(t, root, "internal/journal/types.go", `package journal; type EntryType string; const EntryA EntryType = "a.event"`)
	writeGeneratorSource(t, root, "cmd/emitter/events.go", `package emitter; const Named = journal.EntryType("b.event"); func emit(){_ = journal.EntryType("c.event"); _ = journal.EntryType("a.event")}`)
	main()
	first, err := os.ReadFile(filepath.Join(root, outputPath))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"EntryA,", `EntryType("b.event"), // Named, cmd/emitter/events.go:1`, `EntryType("c.event"), // cmd/emitter/events.go:1`} {
		if !bytes.Contains(first, []byte(want)) {
			t.Fatalf("missing registry entry %q: %s", want, first)
		}
	}
	if bytes.Contains(first, []byte(root)) {
		t.Fatal("generated file leaks absolute checkout path")
	}
	if _, err := parser.ParseFile(token.NewFileSet(), outputPath, first, parser.AllErrors); err != nil {
		t.Fatalf("generated invalid Go: %v", err)
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(filepath.Join(root, outputPath))
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("generation is not reproducible: %v", err)
	}
}
func TestRunPreservesRegistryWhenInputsCannotBeScanned(t *testing.T) {
	for _, tc := range []struct{ name, source, want string }{{"empty", `package journal`, "zero EntryType"}, {"parse", `package journal; const =`, "parse"}, {"declaration", `package journal; const Broken EntryType = 42`, "not a string literal"}} {
		t.Run(tc.name, func(t *testing.T) {
			root := generatorFixture(t)
			writeGeneratorSource(t, root, outputPath, "// existing registry\npackage journal\n")
			writeGeneratorSource(t, root, "internal/journal/types.go", tc.source)
			if err := run(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("missing input error: %v", err)
			}
			kept, err := os.ReadFile(filepath.Join(root, outputPath))
			if err != nil || string(kept) != "// existing registry\npackage journal\n" {
				t.Fatalf("failed generation damaged registry: %q %v", kept, err)
			}
		})
	}
}
func TestRunReportsMissingRootsAndWriteFailure(t *testing.T) {
	t.Run("no module", func(t *testing.T) {
		t.Chdir(t.TempDir())
		if err := run(); err == nil || !strings.Contains(err.Error(), "go.mod") {
			t.Fatalf("missing module: %v", err)
		}
	})
	t.Run("missing source root", func(t *testing.T) {
		root := generatorFixture(t)
		if err := os.RemoveAll(filepath.Join(root, "cmd")); err != nil {
			t.Fatal(err)
		}
		if err := run(); err == nil {
			t.Fatal("missing command root ignored")
		}
	})
	t.Run("output is directory", func(t *testing.T) {
		root := generatorFixture(t)
		writeGeneratorSource(t, root, "internal/journal/types.go", `package journal; const EntryA EntryType = "a.event"`)
		if err := os.Mkdir(filepath.Join(root, outputPath), 0700); err != nil {
			t.Fatal(err)
		}
		if err := run(); err == nil || !strings.Contains(err.Error(), "write "+outputPath) {
			t.Fatalf("output write error: %v", err)
		}
	})
}
func TestRenderEscapesValuesAndRefusesInvalidGoNames(t *testing.T) {
	out, err := render([]journalgen.EntryConst{{Value: "quoted\"value\n", Pos: "source.go:1"}})
	if err != nil || !bytes.Contains(out, []byte(`EntryType("quoted\"value\n")`)) {
		t.Fatalf("literal escaping: %s %v", out, err)
	}
	out, err = render([]journalgen.EntryConst{{Name: "not valid Go", Value: "a.event", InJournalPkg: true}})
	if err == nil || out != nil || !strings.Contains(err.Error(), "gofmt generated source") {
		t.Fatalf("invalid generated declaration accepted: %s %v", out, err)
	}
}
