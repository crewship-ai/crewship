package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureStdout redirects os.Stdout for the duration of fn and returns
// what was printed. Not safe for parallel tests — callers must not use
// t.Parallel().
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	runErr := fn()
	_ = w.Close()
	os.Stdout = orig
	buf, _ := io.ReadAll(r)
	_ = r.Close()
	return string(buf), runErr
}

func TestSlashCmdStructure(t *testing.T) {
	t.Parallel()

	if slashCmd.Use != "slash" {
		t.Errorf("slash Use: got %q", slashCmd.Use)
	}
	have := map[string]bool{}
	for _, sub := range slashCmd.Commands() {
		have[sub.Name()] = true
	}
	for _, want := range []string{"list", "init"} {
		if !have[want] {
			t.Errorf("slash missing subcommand %q; have %v", want, have)
		}
	}
}

func TestSlashListRunE_ListsCommands(t *testing.T) {
	saveCLIState(t)
	dir := setupSlashHome(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\ndescription: cov list entry\nagent: viktor\n---\nbody $args\n"
	if err := os.WriteFile(filepath.Join(dir, "cov-listed.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := captureStdout(t, func() error {
		return slashListCmd.RunE(slashListCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"cov-listed", "cov list entry", "viktor"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; got %q", want, out)
		}
	}
}

func TestSlashListRunE_LoadError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".crewship"), 0o755); err != nil {
		t.Fatal(err)
	}
	// commands path is a FILE → ReadDir fails → RunE surfaces the error.
	if err := os.WriteFile(filepath.Join(home, ".crewship", "commands"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := slashListCmd.RunE(slashListCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "read slash dir") {
		t.Errorf("got %v; want read-slash-dir error", err)
	}
}

func TestSlashInitRunE_CreatesSampleThenLeavesItAlone(t *testing.T) {
	dir := setupSlashHome(t)

	out, err := captureStdout(t, func() error {
		return slashInitCmd.RunE(slashInitCmd, nil)
	})
	if err != nil {
		t.Fatalf("first RunE: %v", err)
	}
	if !strings.Contains(out, "Created ") {
		t.Errorf("first run output: got %q", out)
	}

	sample := filepath.Join(dir, "review.md")
	content, err := os.ReadFile(sample)
	if err != nil {
		t.Fatalf("sample not created: %v", err)
	}
	for _, want := range []string{"name: review", "vars:", "$args"} {
		if !strings.Contains(string(content), want) {
			t.Errorf("sample missing %q", want)
		}
	}

	// Second run must not overwrite.
	if err := os.WriteFile(sample, []byte("user edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = captureStdout(t, func() error {
		return slashInitCmd.RunE(slashInitCmd, nil)
	})
	if err != nil {
		t.Fatalf("second RunE: %v", err)
	}
	if !strings.Contains(out, "already exists") {
		t.Errorf("second run output: got %q", out)
	}
	after, _ := os.ReadFile(sample)
	if string(after) != "user edited" {
		t.Errorf("sample overwritten: got %q", after)
	}
}

func TestSlashInitRunE_MkdirError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// ~/.crewship is a FILE → MkdirAll(~/.crewship/commands) fails.
	if err := os.WriteFile(filepath.Join(home, ".crewship"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := slashInitCmd.RunE(slashInitCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "create ") {
		t.Errorf("got %v; want create-dir error", err)
	}
}

func TestSlashInitRunE_NoHome(t *testing.T) {
	t.Setenv("HOME", "")

	if err := slashInitCmd.RunE(slashInitCmd, nil); err == nil {
		t.Error("want error when $HOME is unset; got nil")
	}
}
