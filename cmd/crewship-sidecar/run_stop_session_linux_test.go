//go:build linux

package main

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// startSessionFixture starts a session leader shell that forks two background
// sleeps and one grandchild that stays in the session, the shape a cancelled
// agent CLI leaves behind (#4774). It returns the leader and its start ticks.
func startSessionFixture(t *testing.T) (*exec.Cmd, uint64) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", `sleep 300 & sleep 300 & (sh -c 'sleep 300 & wait') & echo ready; wait`)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() })
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "ready" {
		t.Fatalf("fixture not ready: %q %v", line, err)
	}
	// The grandchild's sleep is forked asynchronously; wait until it exists.
	deadline := time.Now().Add(3 * time.Second)
	for {
		members, err := sessionProcesses(cmd.Process.Pid)
		if err != nil {
			t.Fatal(err)
		}
		if len(members) >= 5 { // leader, 2 sleeps, subshell, its sleep
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("fixture session has %d processes", len(members))
		}
		time.Sleep(10 * time.Millisecond)
	}
	start, err := processStartTicks(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	return cmd, start
}

func liveSessionMembers(t *testing.T, session int) []int {
	t.Helper()
	members, err := sessionProcesses(session)
	if err != nil {
		t.Fatal(err)
	}
	var live []int
	for _, m := range members {
		if m.state != 'Z' && m.state != 'X' {
			live = append(live, m.pid)
		}
	}
	return live
}

func TestExactSessionStopKillsEveryProcessLeftInTheSession(t *testing.T) {
	leader, start := startSessionFixture(t)
	foreign, _ := startStopFixture(t)
	var diagnostics bytes.Buffer
	empty, err := stopExactSession(leader.Process.Pid, start, &diagnostics)
	if err != nil || !empty {
		t.Fatalf("session stop empty=%v err=%v diag=%s", empty, err, diagnostics.String())
	}
	if live := liveSessionMembers(t, leader.Process.Pid); len(live) != 0 {
		t.Fatalf("receipt issued while session still has live processes %v", live)
	}
	if err := foreign.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("process outside the session affected: %v", err)
	}
}

func TestExactSessionStopRefusesChangedIdentityAndTouchesNothing(t *testing.T) {
	leader, start := startSessionFixture(t)
	before := liveSessionMembers(t, leader.Process.Pid)
	empty, err := stopExactSession(leader.Process.Pid, start+1, &bytes.Buffer{})
	if err == nil || empty {
		t.Fatalf("changed identity accepted: empty=%v err=%v", empty, err)
	}
	if after := liveSessionMembers(t, leader.Process.Pid); len(after) != len(before) {
		t.Fatalf("wrong identity changed the session: %v -> %v", before, after)
	}
}

func TestNonLeaderStopKillsOnlyTheExactProcessAndIssuesNoReceipt(t *testing.T) {
	target, start := startStopFixture(t) // shares the test's session: not a leader
	empty, err := stopExactSession(target.Process.Pid, start, &bytes.Buffer{})
	if err != nil || empty {
		t.Fatalf("non-leader: empty=%v err=%v", empty, err)
	}
	done := make(chan error, 1)
	go func() { done <- target.Wait() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("exact target survived")
	}
}

func TestSessionReceiptIsPrintedOnlyForAConfirmedEmptySession(t *testing.T) {
	leader, start := startSessionFixture(t)
	args := []string{"--pid", strconv.Itoa(leader.Process.Pid), "--starttime", strconv.FormatUint(start, 10)}
	out := captureStdout(t, func() {
		if code := runStopProcessCommand(args, &bytes.Buffer{}); code != 0 {
			t.Fatalf("session stop exit %d", code)
		}
	})
	if strings.TrimSpace(out) != sessionEmptyReceipt {
		t.Fatalf("stdout %q, want the session receipt", out)
	}
	target, tstart := startStopFixture(t)
	out = captureStdout(t, func() {
		_ = runStopProcessCommand([]string{"--pid", strconv.Itoa(target.Process.Pid), "--starttime", strconv.FormatUint(tstart, 10)}, &bytes.Buffer{})
	})
	if strings.Contains(out, sessionEmptyReceipt) {
		t.Fatal("receipt printed for a process that does not lead its session")
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() { var b bytes.Buffer; _, _ = b.ReadFrom(r); done <- b.String() }()
	func() { defer func() { os.Stdout = saved }(); fn() }()
	_ = w.Close()
	return <-done
}
