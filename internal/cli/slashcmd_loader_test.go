package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// slashcmd.go — DefaultSlashDir, LoadSlashCommands, ParseSlashFile.
//
// The existing slashcmd_test.go covers parseSlashReader + Render +
// slashNameValid. This fills the three zero-coverage loader paths
// that turn ~/.crewship/commands/*.md into runnable cobra subcommands.
// ---------------------------------------------------------------------------

func TestDefaultSlashDir_PointsAtCommandsSubdir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	got, err := DefaultSlashDir()
	if err != nil {
		t.Fatalf("DefaultSlashDir: %v", err)
	}
	want := filepath.Join(tmp, ".crewship", "commands")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestLoadSlashCommands_MissingDir_ReturnsNilAndNoError(t *testing.T) {
	// Source: missing directory is an opt-in surface; not an error.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp) // ~/.crewship/commands does not exist
	got, err := LoadSlashCommands(context.Background())
	if err != nil {
		t.Errorf("err = %v, want nil for missing dir", err)
	}
	if got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

func TestLoadSlashCommands_LoadsMdFilesSortedAlphabetically(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	dir := filepath.Join(tmp, ".crewship", "commands")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	files := map[string]string{
		"zebra.md":   "---\ndescription: z\n---\nz body\n",
		"alpha.md":   "---\ndescription: a\n---\na body\n",
		"mango.md":   "---\ndescription: m\n---\nm body\n",
		"README.txt": "not a slash command",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "nested-dir"), 0700); err != nil {
		t.Fatalf("subdir: %v", err)
	}

	got, err := LoadSlashCommands(context.Background())
	if err != nil {
		t.Fatalf("LoadSlashCommands: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d commands, want 3 (.md only; README + subdir excluded). got=%+v", len(got), got)
	}
	wantOrder := []string{"alpha", "mango", "zebra"}
	for i, w := range wantOrder {
		if got[i].Name != w {
			t.Errorf("got[%d].Name = %q, want %q (alphabetic order)", i, got[i].Name, w)
		}
	}
}

func TestLoadSlashCommands_BadFileSkipped_RestSurvive(t *testing.T) {
	// Source: "Each file is parsed independently; one malformed file
	// warns to stderr but does not abort the rest". Pin that contract
	// so a single broken slash command doesn't break the whole CLI.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	dir := filepath.Join(tmp, ".crewship", "commands")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "good.md"), []byte("hello"), 0644); err != nil {
		t.Fatalf("write good: %v", err)
	}
	// broken.md has frontmatter that never closes.
	if err := os.WriteFile(filepath.Join(dir, "broken.md"), []byte("---\nname: x\nno-end-marker"), 0644); err != nil {
		t.Fatalf("write broken: %v", err)
	}

	got, err := LoadSlashCommands(context.Background())
	if err != nil {
		t.Fatalf("LoadSlashCommands: %v", err)
	}
	if len(got) != 1 || got[0].Name != "good" {
		t.Errorf("got %+v, want exactly one entry named \"good\" (broken.md must be skipped)", got)
	}
}

func TestLoadSlashCommands_RespectsContextCancel(t *testing.T) {
	// Source: "The ctx parameter is honoured between per-file parses so
	// a slow network-mounted commands directory can be aborted by CLI
	// shutdown without leaving the directory walk stuck."
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	dir := filepath.Join(tmp, ".crewship", "commands")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, n := range []string{"a.md", "b.md", "c.md"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("body"), 0644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled
	_, err := LoadSlashCommands(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestLoadSlashCommands_NilContext_Tolerated(t *testing.T) {
	// Source has `if ctx != nil` — nil ctx must not panic.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	dir := filepath.Join(tmp, ".crewship", "commands")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x.md"), []byte("hi"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil ctx panicked: %v", r)
		}
	}()
	got, err := LoadSlashCommands(nil) //nolint:staticcheck // testing the nil-ctx tolerance
	if err != nil {
		t.Errorf("nil ctx err = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Errorf("got %d commands, want 1", len(got))
	}
}

// ---- ParseSlashFile ----

func TestParseSlashFile_MissingFile_Errors(t *testing.T) {
	_, err := ParseSlashFile(filepath.Join(t.TempDir(), "does-not-exist.md"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "open") {
		t.Errorf("err = %v, want \"open\" prefix", err)
	}
}

func TestParseSlashFile_HappyPath_PopulatesSourceField(t *testing.T) {
	// parseSlashReader is exhaustively covered; ParseSlashFile's added
	// contract is that it sets SlashCommand.Source to the file path so
	// `crewship slash list` can show provenance.
	tmp := t.TempDir()
	path := filepath.Join(tmp, "summarise.md")
	body := `---
description: Summarise text
---
Summarise ${args}.`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	sc, err := ParseSlashFile(path)
	if err != nil {
		t.Fatalf("ParseSlashFile: %v", err)
	}
	if sc.Source != path {
		t.Errorf("Source = %q, want %q (loader must record provenance)", sc.Source, path)
	}
	if sc.Name != "summarise" {
		t.Errorf("Name = %q, want filename-derived \"summarise\"", sc.Name)
	}
}

func TestParseSlashFile_InvalidNameRejected(t *testing.T) {
	// slashNameValid enforces [a-z0-9][a-z0-9-]* — a filename with an
	// uppercase letter must error at parse time so the invalid cobra
	// subcommand never gets registered.
	tmp := t.TempDir()
	path := filepath.Join(tmp, "BadName.md")
	if err := os.WriteFile(path, []byte("body"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := ParseSlashFile(path)
	if err == nil {
		t.Fatal("expected error for invalid command name")
	}
	if !strings.Contains(err.Error(), "invalid command name") {
		t.Errorf("err = %v, want \"invalid command name\"", err)
	}
}

func TestParseSlashFile_FrontmatterNameOverridesBasename(t *testing.T) {
	// If frontmatter has `name: foo`, that wins over the filename-
	// derived name. Pin the precedence so a "rename via frontmatter"
	// workflow keeps working when users symlink shared command files.
	tmp := t.TempDir()
	path := filepath.Join(tmp, "different-filename.md")
	body := `---
name: chosen-name
---
hi`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	sc, err := ParseSlashFile(path)
	if err != nil {
		t.Fatalf("ParseSlashFile: %v", err)
	}
	if sc.Name != "chosen-name" {
		t.Errorf("Name = %q, want \"chosen-name\" (frontmatter overrides filename)", sc.Name)
	}
}
