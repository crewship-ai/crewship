//go:build unix

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Separate test processes exercise the real command dispatch without building a
// second binary. The reader receives SIGKILL, not a cooperative cancellation.
func TestRunOutputSubprocess(t *testing.T) {
	if os.Getenv("CREWSHIP_RUN_OUTPUT_TEST_CHILD") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" && i+1 < len(os.Args) {
			os.Exit(runOutputCommand(context.Background(), os.Args[i+1], os.Args[i+2:], os.Stdout, os.Stderr))
		}
	}
	os.Exit(125)
}

func TestRunOutputSurvivesKilledReaderProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := func(mode string, args ...string) *exec.Cmd {
		argv := append([]string{"-test.run=^TestRunOutputSubprocess$", "--", mode}, args...)
		cmd := exec.CommandContext(ctx, executable, argv...)
		// Do not hand the fixture any credentials from the test runner.
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "CREWSHIP_RUN_OUTPUT_TEST_CHILD=1"}
		return cmd
	}
	root := t.TempDir()
	dir, release := filepath.Join(root, "run"), filepath.Join(root, "release")
	starts := filepath.Join(root, "starts")
	producer := command("run-capture", "--dir", dir, "--timeout", "10s", "--", "sh", "-c",
		`printf x >> "$2"; printf before; while [ ! -f "$1" ]; do sleep 0.02; done; printf after; exit 7`, "fixture", release, starts)
	if err := producer.Start(); err != nil {
		t.Fatal(err)
	}
	// Releasing on every failure also lets Capture reap its child. Killing only
	// the supervisor in cleanup would leave the independent process group alive.
	producerWaited := false
	defer func() {
		_ = os.WriteFile(release, nil, 0600)
		if !producerWaited {
			_ = producer.Wait()
		}
	}()
	reader := command("run-read", "--dir", dir, "--raw", "--timeout", "10s")
	stdout, err := reader.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = reader.Start(); err != nil {
		t.Fatal(err)
	}
	readerWaited := false
	defer func() {
		if !readerWaited {
			_ = reader.Process.Kill()
			_ = reader.Wait()
		}
	}()
	first := make([]byte, len("before"))
	if _, err = io.ReadFull(stdout, first); err != nil || string(first) != "before" {
		t.Fatalf("initial output=%q err=%v", first, err)
	}
	if err = reader.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err = reader.Wait()
	readerWaited = true
	if err == nil {
		t.Fatal("killed reader returned success")
	}
	if err = os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	err = producer.Wait()
	producerWaited = true
	assertProcessExit(t, err, 7)
	// Sequence 1 is accepted, sequence 2 is the acknowledged first write.
	output, err := command("run-read", "--dir", dir, "--raw", "--after", "2").Output()
	assertProcessExit(t, err, 7)
	if string(output) != "after" {
		t.Fatalf("replay=%q", output)
	}
	err = command("run-capture", "--dir", dir, "--", "sh", "-c", `printf x >> "$1"`, "fixture", starts).Run()
	assertProcessExit(t, err, 125)
	count, err := os.ReadFile(starts)
	if err != nil || strings.Count(string(count), "x") != 1 {
		t.Fatalf("duplicate execution: starts=%q err=%v", count, err)
	}
}

func assertProcessExit(t *testing.T, err error, want int) {
	t.Helper()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != want {
		t.Fatalf("process exit: want %d, got %v", want, err)
	}
}
