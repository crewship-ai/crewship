package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/runoutput"
)

// launchInput is internal transport, never an unauthenticated network API.
// The parent directory must be a provisioned persistent, instance-local path.
type launchInput = runoutput.LaunchInput

func runLaunchCommand(ctx context.Context, mode string, args []string, input io.Reader, out, diagnostics io.Writer) int {
	fs := flag.NewFlagSet(mode, flag.ContinueOnError)
	fs.SetOutput(diagnostics)
	dir := fs.String("dir", "", "exclusive persistent launch directory")
	session := fs.String("session", "", "run-scoped tmux session")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || !filepath.IsAbs(*dir) {
		fmt.Fprintln(diagnostics, "absolute launch directory required")
		return 125
	}
	if mode == "run-execute" {
		return executeLaunch(ctx, *dir, diagnostics)
	}
	if !validLaunchSession(*session) {
		fmt.Fprintln(diagnostics, "invalid launch session")
		return 125
	}
	var spec launchInput
	data, err := io.ReadAll(io.LimitReader(input, (8<<20)+1))
	if err != nil || len(data) > 8<<20 || json.Unmarshal(data, &spec) != nil || len(spec.Command.Args) == 0 || spec.Command.Args[0] == "" || !spec.Deadline.After(time.Now()) {
		fmt.Fprintln(diagnostics, "invalid launch input or expired deadline")
		return 125
	}
	// Capture the admitted client's environment; an existing tmux server's
	// environment belongs to a different execution and must not be inherited.
	spec.Command.Env = os.Environ()
	if spec.Command.Dir == "" {
		spec.Command.Dir, err = os.Getwd()
		if err != nil {
			return 125
		}
	}
	executable, err := os.Executable()
	if err != nil {
		return 125
	}
	tmux, err := exec.LookPath("tmux")
	if err != nil {
		return 125
	}
	// This claim precedes every write. Interrupted claims are not recycled:
	// retrying admission must never overwrite a live run's credentials/argv.
	if err = os.Mkdir(*dir, 0700); err != nil {
		fmt.Fprintln(diagnostics, "launch identity already claimed or unavailable")
		return 125
	}
	if err = syncLaunchDir(filepath.Dir(*dir)); err != nil {
		return 125
	}
	encoded, err := json.Marshal(spec)
	if err != nil {
		return 125
	}
	path := filepath.Join(*dir, "input.json")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return 125
	}
	_, writeErr := f.Write(encoded)
	syncErr := f.Sync()
	closeErr := f.Close()
	if errors.Join(writeErr, syncErr, closeErr, syncLaunchDir(*dir)) != nil {
		_ = os.Remove(path)
		return 125
	}
	// The daemonized tmux session owns capture. This client only waits for
	// output; losing the client does not close the child's output pipe.
	script := "exec " + launchQuote(executable) + " run-execute --dir " + launchQuote(*dir)
	start := exec.CommandContext(ctx, tmux, "new-session", "-d", "-s", *session, script)
	start.Stdout, start.Stderr = io.Discard, io.Discard
	if err = start.Run(); err != nil {
		_ = os.Remove(path)
		fmt.Fprintln(diagnostics, "detached launch failed; identity remains claimed")
		return 125
	}
	return runOutputCommand(ctx, "run-read", []string{"--dir", filepath.Join(*dir, "output"), "--raw", "--timeout", time.Until(spec.Deadline).String()}, out, diagnostics)
}

func executeLaunch(ctx context.Context, dir string, diagnostics io.Writer) int {
	path := filepath.Join(dir, "input.json")
	f, err := os.Open(path)
	if err != nil {
		return 125
	}
	data, err := io.ReadAll(io.LimitReader(f, (8<<20)+1))
	f.Close()
	// Consume the secret-bearing input before starting any workload. The
	// exclusive directory remains as the duplicate-launch tombstone.
	removeErr := os.Remove(path)
	var spec launchInput
	if err != nil || removeErr != nil || len(data) > 8<<20 || json.Unmarshal(data, &spec) != nil || spec.Deadline.IsZero() {
		return 125
	}
	if syncLaunchDir(dir) != nil {
		return 125
	}
	ctx, cancel := context.WithDeadline(ctx, spec.Deadline)
	defer cancel()
	result, err := runoutput.CaptureCommand(ctx, filepath.Join(dir, "output"), spec.Command, runoutput.DefaultLimit)
	if err != nil {
		fmt.Fprintln(diagnostics, "capture failed")
		return 125
	}
	if result.Reason != "exited" || result.ExitCode < 0 || result.ExitCode > 255 {
		return 125
	}
	return result.ExitCode
}

func validLaunchSession(session string) bool {
	if session == "" || len(session) > 200 {
		return false
	}
	for _, r := range session {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func launchQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

func syncLaunchDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
