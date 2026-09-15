//go:build linux

package orchestrator

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Exercises the generated protocol against real processes, not a fake that
// always answers ABSENT. A sibling process group must survive the stop.
func TestDirectRun_StopTargetsOnlyItsProcessGroup(t *testing.T) {
	start := func(label string) (string, *exec.Cmd, chan error) {
		id := fmt.Sprintf("review-%s-%d", label, time.Now().UnixNano())
		argv := directRunCommand(id, []string{"sleep", "60"})
		cmd := exec.Command(argv[0], argv[1:]...)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = os.Remove(directRunPIDFile(id)) })
		return id, cmd, done
	}
	probe := func(id string, stop bool) string {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "sh", "-c", directRunProbe(id, stop)+"echo MISSING").CombinedOutput()
		if err != nil {
			t.Fatalf("probe: %v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	a, _, done := start("a")
	b, _, _ := start("b")
	deadline := time.Now().Add(3 * time.Second)
	for probe(a, false) != "PRESENT" || probe(b, false) != "PRESENT" {
		if time.Now().After(deadline) {
			t.Fatal("process identities never became present")
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = probe(a, true)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stop did not terminate direct exec")
	}
	if got := probe(a, false); got != "ABSENT" {
		t.Fatalf("stopped run: %s", got)
	}
	if got := probe(b, false); got != "PRESENT" {
		t.Fatalf("sibling damaged: %s", got)
	}
}

func TestDirectRun_PreservesStdinAndLiteralArguments(t *testing.T) {
	id := fmt.Sprintf("stdin-%d", time.Now().UnixNano())
	defer os.Remove(directRunPIDFile(id))
	argv := directRunCommand(id, []string{"sh", "-c", `printf '%s\n' "$1"; cat`, "arg0", "literal $(false) ' value"})
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin = strings.NewReader("prompt\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if string(out) != "literal $(false) ' value\nprompt\n" {
		t.Fatalf("argv/stdin corrupted: %q", out)
	}
}
