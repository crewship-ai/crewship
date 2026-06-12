package main

// Coverage tests for cmd_prompt.go — the local prompt library. All
// filesystem state is scoped to a temp HOME so ~/.crewship/prompts never
// touches the developer's real library. NOT parallel: HOME and package
// globals are process-wide.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// covPromptHome points HOME at a temp dir and returns the prompts dir
// (not created — promptSave/promptEdit auto-create it).
func covPromptHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return filepath.Join(home, ".crewship", "prompts")
}

func covWritePrompt(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir prompts: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0o600); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
}

func TestPromptDirAndPath(t *testing.T) {
	dir := covPromptHome(t)

	got, err := promptDir()
	if err != nil {
		t.Fatalf("promptDir: %v", err)
	}
	if got != dir {
		t.Errorf("promptDir = %q, want %q", got, dir)
	}

	p, err := promptPath("review-go")
	if err != nil {
		t.Fatalf("promptPath: %v", err)
	}
	if want := filepath.Join(dir, "review-go.md"); p != want {
		t.Errorf("promptPath = %q, want %q", p, want)
	}

	// Traversal attempts are rejected before any path is built.
	if _, err := promptPath("../etc/passwd"); err == nil {
		t.Error("promptPath should reject path traversal names")
	}
	if _, err := promptPath(""); err == nil {
		t.Error("promptPath should reject empty names")
	}
}

func TestSuggestSimilarPrompt(t *testing.T) {
	dir := covPromptHome(t)

	enoent := os.ErrNotExist

	t.Run("non-ENOENT passes through unchanged", func(t *testing.T) {
		base := os.ErrPermission
		if got := suggestSimilarPrompt("x", base); got != base {
			t.Errorf("expected base error back, got %v", got)
		}
	})

	t.Run("no prompts dir yet", func(t *testing.T) {
		err := suggestSimilarPrompt("review", enoent)
		if err == nil || !strings.Contains(err.Error(), "no prompts saved yet") {
			t.Errorf("expected 'no prompts saved yet', got %v", err)
		}
	})

	t.Run("did-you-mean on near match", func(t *testing.T) {
		covWritePrompt(t, dir, "review-go", "x")
		err := suggestSimilarPrompt("reviw-go", enoent)
		if err == nil || !strings.Contains(err.Error(), "Did you mean: review-go") {
			t.Errorf("expected did-you-mean suggestion, got %v", err)
		}
	})

	t.Run("available list on distant miss", func(t *testing.T) {
		covWritePrompt(t, dir, "review-go", "x")
		err := suggestSimilarPrompt("zzzzzzzzzzzzzzzz", enoent)
		if err == nil || !strings.Contains(err.Error(), "Available: review-go") {
			t.Errorf("expected available list, got %v", err)
		}
	})

	t.Run("empty dir counts as no prompts", func(t *testing.T) {
		empty := covPromptHome(t)
		if err := os.MkdirAll(empty, 0o700); err != nil {
			t.Fatal(err)
		}
		err := suggestSimilarPrompt("review", enoent)
		if err == nil || !strings.Contains(err.Error(), "no prompts saved yet") {
			t.Errorf("expected 'no prompts saved yet', got %v", err)
		}
	})
}

func TestPromptSaveUseDeleteRoundtrip(t *testing.T) {
	dir := covPromptHome(t)
	saveCLIState(t)

	// save via --content
	if err := promptSaveCmd.Flags().Set("content", "Review this diff carefully."); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = promptSaveCmd.Flags().Set("content", "")
		_ = promptSaveCmd.Flags().Set("file", "")
	})
	if err := promptSaveCmd.RunE(promptSaveCmd, []string{"review"}); err != nil {
		t.Fatalf("prompt save: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "review.md"))
	if err != nil {
		t.Fatalf("saved file missing: %v", err)
	}
	if string(data) != "Review this diff carefully." {
		t.Errorf("saved content = %q", data)
	}

	// use → prints content to stdout
	out, err := covCaptureStdoutCli7(t, func() error {
		return promptUseCmd.RunE(promptUseCmd, []string{"review"})
	})
	if err != nil {
		t.Fatalf("prompt use: %v", err)
	}
	if out != "Review this diff carefully." {
		t.Errorf("prompt use output = %q", out)
	}

	// path → prints the absolute path
	out, err = covCaptureStdoutCli7(t, func() error {
		return promptPathCmd.RunE(promptPathCmd, []string{"review"})
	})
	if err != nil {
		t.Fatalf("prompt path: %v", err)
	}
	if strings.TrimSpace(out) != filepath.Join(dir, "review.md") {
		t.Errorf("prompt path output = %q", out)
	}

	// delete → file gone
	if err := promptDeleteCmd.RunE(promptDeleteCmd, []string{"review"}); err != nil {
		t.Fatalf("prompt delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "review.md")); !os.IsNotExist(err) {
		t.Errorf("file should be gone after delete, stat err = %v", err)
	}

	// second delete → not-found error with library context
	if err := promptDeleteCmd.RunE(promptDeleteCmd, []string{"review"}); err == nil {
		t.Error("deleting a missing prompt should error")
	}
}

func TestPromptSaveFromFileAndErrors(t *testing.T) {
	dir := covPromptHome(t)
	saveCLIState(t)

	src := filepath.Join(t.TempDir(), "src.md")
	if err := os.WriteFile(src, []byte("from file"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = promptSaveCmd.Flags().Set("content", "")
	if err := promptSaveCmd.Flags().Set("file", src); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = promptSaveCmd.Flags().Set("file", "") })

	if err := promptSaveCmd.RunE(promptSaveCmd, []string{"fromfile"}); err != nil {
		t.Fatalf("save --file: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "fromfile.md"))
	if string(data) != "from file" {
		t.Errorf("content = %q", data)
	}

	// --file pointing nowhere
	_ = promptSaveCmd.Flags().Set("file", filepath.Join(t.TempDir(), "nope.md"))
	if err := promptSaveCmd.RunE(promptSaveCmd, []string{"badfile"}); err == nil || !strings.Contains(err.Error(), "read --file") {
		t.Errorf("expected read --file error, got %v", err)
	}

	// invalid name rejected before any IO
	_ = promptSaveCmd.Flags().Set("file", "")
	if err := promptSaveCmd.RunE(promptSaveCmd, []string{"bad/name"}); err == nil {
		t.Error("expected invalid-name error")
	}
}

func TestPromptUseNotFoundSuggests(t *testing.T) {
	dir := covPromptHome(t)
	covWritePrompt(t, dir, "review-go", "x")

	err := promptUseCmd.RunE(promptUseCmd, []string{"reviw-go"})
	if err == nil || !strings.Contains(err.Error(), "Did you mean: review-go") {
		t.Errorf("expected suggestion, got %v", err)
	}

	// path on a missing prompt goes through the same suggestion helper
	err = promptPathCmd.RunE(promptPathCmd, []string{"reviw-go"})
	if err == nil || !strings.Contains(err.Error(), "Did you mean: review-go") {
		t.Errorf("expected suggestion from path cmd, got %v", err)
	}
}

func TestPromptListFormats(t *testing.T) {
	dir := covPromptHome(t)
	saveCLIState(t)
	origFormat := flagFormat
	t.Cleanup(func() { flagFormat = origFormat })

	covWritePrompt(t, dir, "alpha", "aaa")
	covWritePrompt(t, dir, "beta", "bbbbbb")
	// Non-.md entries and subdirectories are ignored.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub.md"), 0o700); err != nil {
		t.Fatal(err)
	}

	flagFormat = "quiet"
	out, err := covCaptureStdoutCli7(t, func() error {
		return promptListCmd.RunE(promptListCmd, nil)
	})
	if err != nil {
		t.Fatalf("prompt list quiet: %v", err)
	}
	if out != "alpha\nbeta\n" {
		t.Errorf("quiet output = %q, want sorted names only", out)
	}

	flagFormat = "json"
	out, err = covCaptureStdoutCli7(t, func() error {
		return promptListCmd.RunE(promptListCmd, nil)
	})
	if err != nil {
		t.Fatalf("prompt list json: %v", err)
	}
	if !strings.Contains(out, `"alpha"`) || !strings.Contains(out, `"size_bytes"`) {
		t.Errorf("json output missing fields: %q", out)
	}

	flagFormat = ""
	out, err = covCaptureStdoutCli7(t, func() error {
		return promptListCmd.RunE(promptListCmd, nil)
	})
	if err != nil {
		t.Fatalf("prompt list table: %v", err)
	}
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "MODIFIED") {
		t.Errorf("table output = %q", out)
	}
}

func TestPromptListEmpty(t *testing.T) {
	covPromptHome(t) // fresh HOME, no prompts dir at all
	saveCLIState(t)
	origFormat := flagFormat
	flagFormat = ""
	t.Cleanup(func() { flagFormat = origFormat })

	out, err := covCaptureStdoutCli7(t, func() error {
		return promptListCmd.RunE(promptListCmd, nil)
	})
	if err != nil {
		t.Fatalf("prompt list on empty library: %v", err)
	}
	if !strings.Contains(out, "No prompts saved.") {
		t.Errorf("expected empty-library hint, got %q", out)
	}
}

func TestPromptEditInvokesEditor(t *testing.T) {
	dir := covPromptHome(t)

	// Fake $EDITOR: a /bin/sh script that writes a marker into the file it
	// is asked to edit (the last argument). Carrying a flag ("-q") proves
	// the strings.Fields split keeps editor arguments intact.
	script := filepath.Join(t.TempDir(), "fake-editor.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nfor last; do :; done\nprintf EDITED > \"$last\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", script+" -q")

	if err := promptEditCmd.RunE(promptEditCmd, []string{"draft"}); err != nil {
		t.Fatalf("prompt edit: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "draft.md"))
	if err != nil {
		t.Fatalf("edited file missing: %v", err)
	}
	if string(data) != "EDITED" {
		t.Errorf("editor did not receive the prompt path; content = %q", data)
	}

	// Invalid name short-circuits before exec.
	if err := promptEditCmd.RunE(promptEditCmd, []string{"../oops"}); err == nil {
		t.Error("expected invalid-name error from edit")
	}
}

// ─── additional error paths ──────────────────────────────────────────────

func TestPrompt_HomeUnset(t *testing.T) {
	// With $HOME empty, os.UserHomeDir fails → promptDir / promptPath error
	// → every subcommand surfaces the failure.
	t.Setenv("HOME", "")
	saveCLIState(t)

	if _, err := promptDir(); err == nil {
		t.Error("promptDir should fail without HOME")
	}
	if _, err := promptPath("x"); err == nil {
		t.Error("promptPath should fail without HOME")
	}
	if err := promptListCmd.RunE(promptListCmd, nil); err == nil {
		t.Error("list should fail without HOME")
	}
	_ = promptSaveCmd.Flags().Set("content", "x")
	t.Cleanup(func() { _ = promptSaveCmd.Flags().Set("content", "") })
	if err := promptSaveCmd.RunE(promptSaveCmd, []string{"valid-name"}); err == nil {
		t.Error("save should fail without HOME")
	}
	if err := promptUseCmd.RunE(promptUseCmd, []string{"valid-name"}); err == nil {
		t.Error("use should fail without HOME")
	}
	if err := promptPathCmd.RunE(promptPathCmd, []string{"valid-name"}); err == nil {
		t.Error("path should fail without HOME")
	}
	if err := promptEditCmd.RunE(promptEditCmd, []string{"valid-name"}); err == nil {
		t.Error("edit should fail without HOME")
	}
	// suggestSimilarPrompt cannot enrich without a prompt dir — the base
	// error comes back unchanged.
	if got := suggestSimilarPrompt("x", os.ErrNotExist); got != os.ErrNotExist {
		t.Errorf("suggestSimilarPrompt = %v, want base error", got)
	}
}

func TestPromptList_PromptsDirIsAFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	saveCLIState(t)
	origFormat := flagFormat
	flagFormat = ""
	t.Cleanup(func() { flagFormat = origFormat })

	// ~/.crewship/prompts exists but is a regular file → ReadDir fails with
	// ENOTDIR, which is NOT os.IsNotExist → the error must propagate.
	if err := os.MkdirAll(filepath.Join(home, ".crewship"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".crewship", "prompts"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := promptListCmd.RunE(promptListCmd, nil); err == nil {
		t.Error("list should propagate ENOTDIR")
	}

	// Same shape through suggestSimilarPrompt: ReadDir error that is not
	// ENOENT → "read prompts dir" wrapper.
	err := suggestSimilarPrompt("x", os.ErrNotExist)
	if err == nil || !strings.Contains(err.Error(), "read prompts dir") {
		t.Errorf("got %v", err)
	}
}

func TestPromptSaveEdit_MkdirFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// ~/.crewship is a FILE → MkdirAll(~/.crewship/prompts) fails.
	if err := os.WriteFile(filepath.Join(home, ".crewship"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	_ = promptSaveCmd.Flags().Set("content", "x")
	t.Cleanup(func() { _ = promptSaveCmd.Flags().Set("content", "") })
	err := promptSaveCmd.RunE(promptSaveCmd, []string{"valid"})
	if err == nil || !strings.Contains(err.Error(), "create prompts dir") {
		t.Errorf("save: %v", err)
	}

	err = promptEditCmd.RunE(promptEditCmd, []string{"valid"})
	if err == nil || !strings.Contains(err.Error(), "create prompts dir") {
		t.Errorf("edit: %v", err)
	}
}

func TestPromptSave_FromStdin(t *testing.T) {
	dir := covPromptHome(t)
	saveCLIState(t)
	_ = promptSaveCmd.Flags().Set("content", "")
	_ = promptSaveCmd.Flags().Set("file", "")

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString("piped prompt body"); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = origStdin })

	if err := promptSaveCmd.RunE(promptSaveCmd, []string{"piped"}); err != nil {
		t.Fatalf("save from stdin: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "piped.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "piped prompt body" {
		t.Errorf("content = %q", data)
	}
}

func TestPromptSave_ReadOnlyPromptsDir(t *testing.T) {
	dir := covPromptHome(t)
	saveCLIState(t)
	// Create writable first (MkdirAll needs to descend into the parent),
	// then drop the write bit on the leaf.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	_ = promptSaveCmd.Flags().Set("content", "x")
	t.Cleanup(func() { _ = promptSaveCmd.Flags().Set("content", "") })
	err := promptSaveCmd.RunE(promptSaveCmd, []string{"denied"})
	if err == nil || !strings.Contains(err.Error(), "write") {
		t.Errorf("got %v", err)
	}
}

func TestPromptEdit_WhitespaceOnlyEditor(t *testing.T) {
	covPromptHome(t)
	// Non-empty but whitespace-only $EDITOR survives the empty check yet
	// produces zero fields — the command must error instead of exec'ing "".
	t.Setenv("EDITOR", "   ")
	err := promptEditCmd.RunE(promptEditCmd, []string{"draft"})
	if err == nil || !strings.Contains(err.Error(), "$EDITOR is empty after whitespace split") {
		t.Fatalf("got %v", err)
	}
}

func TestValidatePromptName_AllowedAlphabet(t *testing.T) {
	t.Parallel()
	// One name that walks every accepted character class.
	if err := validatePromptName("aZ9-_.ok"); err != nil {
		t.Errorf("valid name rejected: %v", err)
	}
	for _, bad := range []string{".", "..", ".hidden", "has space", "ünïcode", strings.Repeat("a", 65)} {
		if err := validatePromptName(bad); err == nil {
			t.Errorf("name %q should be rejected", bad)
		}
	}
}
