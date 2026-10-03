//go:build unix && integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/runoutput"
)

func TestMain(m *testing.M) {
	if os.Getenv("CREWSHIP_LAUNCH_TEST_HELPER") == "1" && len(os.Args) > 1 {
		switch os.Args[1] {
		case "run-launch", "run-execute", "run-read":
			main()
			os.Exit(125)
		}
	}
	os.Exit(m.Run())
}

func TestDetachedLaunchSurvivesReaderAndRejectsDuplicate(t *testing.T) {
	tmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatal("tmux is required for detached launch conformance")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	socket := filepath.Join(root, "tmux.sock")
	shim := filepath.Join(root, "tmux")
	if err = os.WriteFile(shim, []byte("#!/bin/sh\nexec "+launchQuote(tmux)+" -S "+launchQuote(socket)+" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		_ = exec.CommandContext(cleanup, tmux, "-S", socket, "kill-server").Run()
	}()
	env := []string{"PATH=" + root + ":" + os.Getenv("PATH"), "CREWSHIP_LAUNCH_TEST_HELPER=1", "TMUX=", "CAPTURE_TEST_ENV=admitted"}
	dir := filepath.Join(root, "run")
	release, starts := filepath.Join(root, "release"), filepath.Join(root, "starts")
	spec := launchInput{Command: runoutput.Command{
		Args:  []string{"sh", "-c", `printf x >> "$2"; read input; printf '%s:%s' "$input" "$CAPTURE_TEST_ENV"; while [ ! -f "$1" ]; do sleep 0.02; done; printf after; exit 7`, "fixture", release, starts},
		Stdin: []byte("prompt\n"), Dir: root,
	}, Deadline: time.Now().Add(15 * time.Second)}
	payload, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	launch := func() *exec.Cmd {
		cmd := exec.CommandContext(ctx, executable, "run-launch", "--dir", dir, "--session", "fixture")
		cmd.Env, cmd.Stdin = env, bytes.NewReader(payload)
		return cmd
	}
	observer := launch()
	stdout, err := observer.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = observer.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		_ = os.WriteFile(release, nil, 0600)
		if !waited {
			_ = observer.Process.Kill()
			_ = observer.Wait()
		}
	}()
	first := make([]byte, len("prompt:admitted"))
	if _, err = io.ReadFull(stdout, first); err != nil || string(first) != "prompt:admitted" {
		t.Fatalf("first=%q err=%v", first, err)
	}
	if _, err = os.Stat(filepath.Join(dir, "input.json")); !os.IsNotExist(err) {
		t.Fatalf("launch input not consumed: %v", err)
	}
	// Duplicate admission must not replace the live session or its inputs.
	assertProcessExit(t, launch().Run(), 125)
	if err = observer.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = observer.Wait()
	waited = true
	if err = os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	replay := exec.CommandContext(ctx, executable, "run-read", "--dir", filepath.Join(dir, "output"), "--after", "2", "--raw", "--timeout", "10s")
	replay.Env = env
	output, err := replay.Output()
	assertProcessExit(t, err, 7)
	if string(output) != "after" {
		t.Fatalf("replay=%q", output)
	}
	count, err := os.ReadFile(starts)
	if err != nil || string(count) != "x" {
		t.Fatalf("starts=%q err=%v", count, err)
	}
}
