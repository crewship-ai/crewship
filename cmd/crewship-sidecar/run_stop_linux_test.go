//go:build linux

package main

import (
	"bytes"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func startStopFixture(t *testing.T) (*exec.Cmd, uint64) {
	t.Helper()
	cmd := exec.Command("/bin/sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	start, err := processStartTicks(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	return cmd, start
}

func TestExactProcessStopRejectsWrongStartAndPreservesForeignProcess(t *testing.T) {
	target, start := startStopFixture(t)
	foreign, _ := startStopFixture(t)
	var diagnostics bytes.Buffer
	args := []string{"--pid", strconv.Itoa(target.Process.Pid), "--starttime", strconv.FormatUint(start+1, 10)}
	if code := runStopProcessCommand(args, &diagnostics); code == 0 {
		t.Fatal("changed process identity accepted")
	}
	if err := target.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("wrong identity killed target: %v", err)
	}
	args[3] = strconv.FormatUint(start, 10)
	if code := runStopProcessCommand(args, &diagnostics); code != 0 {
		t.Fatalf("exact stop=%d %s", code, diagnostics.String())
	}
	done := make(chan error, 1)
	go func() { done <- target.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("killed process exited successfully")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("exact target survived")
	}
	if err := foreign.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("foreign process affected: %v", err)
	}
	// A reaped process does not turn into an unconditional success receipt.
	if code := runStopProcessCommand(args, &diagnostics); code == 0 {
		t.Fatal("absent exact process acknowledged as signalled")
	}
}

func TestExactProcessStopRefusesMissingOrUnsafeIdentity(t *testing.T) {
	for _, args := range [][]string{nil, {"--pid", "1", "--starttime", "1"}, {"--pid", "2", "--starttime", "0"}, {"--pid", "-2", "--starttime", "1"}, {"--pid", "2", "--starttime", "1", "extra"}} {
		if code := runStopProcessCommand(args, &bytes.Buffer{}); code == 0 {
			t.Fatalf("unsafe process identity accepted: %v", args)
		}
	}
}

func TestHostCaptureProbeRefusesUnqualifiedIdentity(t *testing.T) {
	for _, args := range [][]string{{"--probe", "--pid", "2"}, {"--probe", "--starttime", "1"}, {"--probe", "extra"}} {
		if code := runStopProcessCommand(args, &bytes.Buffer{}); code == 0 {
			t.Fatalf("mixed probe accepted: %v", args)
		}
	}
	if os.Geteuid() != 1001 {
		if code := runStopProcessCommand([]string{"--probe"}, &bytes.Buffer{}); code == 0 {
			t.Fatal("probe accepted host identity instead of workload UID1001")
		}
	}
}
