//go:build linux

package main

import (
	"bufio"
	"bytes"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// startTreeFixture starts a session leader shell shaped like a cancelled agent
// CLI (#4774): background children in its session, a grandchild, a child that
// moved to its own session with setsid (Claude Code runs every shell command
// that way), and extra commands from the caller. It returns the leader, its
// start ticks and pidfds pinning every process of the tree once `want` exist.
func startTreeFixture(t *testing.T, extra string, want int) (*exec.Cmd, uint64, []int) {
	t.Helper()
	script := `sleep 300 & (sh -c 'sleep 300 & wait') & setsid sleep 300 & ` + extra + ` echo ready; wait`
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var pinned []int
	t.Cleanup(func() {
		for _, fd := range pinned {
			_ = unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0)
			unix.Close(fd)
		}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "ready" {
		t.Fatalf("fixture not ready: %q %v", line, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var tree []processStat
	for {
		procs, err := scanProcesses()
		if err != nil {
			t.Fatal(err)
		}
		tree = treeOf(cmd.Process.Pid, procs)
		if len(tree) >= want {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("fixture tree has %d processes, want %d", len(tree), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, p := range tree {
		fd, err := unix.PidfdOpen(p.pid, 0)
		if err != nil {
			t.Fatal(err)
		}
		pinned = append(pinned, fd)
	}
	start, err := processStartTicks(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	return cmd, start, pinned
}

func survivors(pinned []int) int {
	n := 0
	for _, fd := range pinned {
		if !pidfdExited(fd) {
			n++
		}
	}
	return n
}

func TestExactTreeStopKillsSessionAndDetachedDescendants(t *testing.T) {
	// sleep, subshell + its sleep, setsid sleep
	leader, start, pinned := startTreeFixture(t, "", 4)
	foreign, _ := startStopFixture(t)
	var diagnostics bytes.Buffer
	confirmed, err := stopExactTree(leader.Process.Pid, start, &diagnostics)
	if err != nil || !confirmed {
		t.Fatalf("tree stop confirmed=%v err=%v diag=%s", confirmed, err, diagnostics.String())
	}
	if n := survivors(pinned); n != 0 {
		t.Fatalf("confirmation issued while %d tree processes still run", n)
	}
	if err := foreign.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("process outside the tree affected: %v", err)
	}
}

// A process whose main thread exited shows state Z while its other threads
// keep running; it must still be stopped and killed, never counted as dead.
func TestExactTreeStopKillsProcessWhoseMainThreadExited(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Fatal("python3 is required to build a process without its main thread")
	}
	extra := `python3 -c 'import ctypes,threading,time; threading.Thread(target=lambda: time.sleep(300)).start(); time.sleep(0.05); ctypes.CDLL(None).pthread_exit(None)' & `
	leader, start, pinned := startTreeFixture(t, extra, 5)
	// Wait for the main-thread exit to show up as a Z state with live threads.
	deadline := time.Now().Add(5 * time.Second)
	for {
		procs, _ := scanProcesses()
		found := false
		for _, p := range treeOf(leader.Process.Pid, procs) {
			if p.state == 'Z' && !allThreadsStopped(p.pid) {
				found = true
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture never produced a process without its main thread")
		}
		time.Sleep(10 * time.Millisecond)
	}
	confirmed, err := stopExactTree(leader.Process.Pid, start, &bytes.Buffer{})
	if err != nil || !confirmed {
		t.Fatalf("tree stop confirmed=%v err=%v", confirmed, err)
	}
	if n := survivors(pinned); n != 0 {
		t.Fatalf("confirmation issued while %d tree processes still run", n)
	}
}

func TestExactTreeStopRefusesChangedIdentityAndTouchesNothing(t *testing.T) {
	leader, start, pinned := startTreeFixture(t, "", 4)
	confirmed, err := stopExactTree(leader.Process.Pid, start+1, &bytes.Buffer{})
	if err == nil || confirmed {
		t.Fatalf("changed identity accepted: confirmed=%v err=%v", confirmed, err)
	}
	if n := survivors(pinned); n != len(pinned) {
		t.Fatalf("wrong identity changed the tree: %d of %d left", n, len(pinned))
	}
}

func TestNonLeaderStopKillsOnlyTheExactProcessWithoutConfirmation(t *testing.T) {
	target, start := startStopFixture(t) // shares the test's session: not a leader
	confirmed, err := stopExactTree(target.Process.Pid, start, &bytes.Buffer{})
	if err != nil || confirmed {
		t.Fatalf("non-leader: confirmed=%v err=%v", confirmed, err)
	}
	done := make(chan error, 1)
	go func() { done <- target.Wait() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("exact target survived")
	}
}

// The confirmation travels in the exit status, which no other process in the
// container can write, and only for a confirmed tree.
func TestStopConfirmationIsTheExitStatusOnlyForAConfirmedTree(t *testing.T) {
	leader, start, _ := startTreeFixture(t, "", 4)
	args := []string{"--pid", strconv.Itoa(leader.Process.Pid), "--starttime", strconv.FormatUint(start, 10)}
	if code := runStopProcessCommand(args, &bytes.Buffer{}); code != stopConfirmedExit {
		t.Fatalf("tree stop exit %d, want %d", code, stopConfirmedExit)
	}
	target, tstart := startStopFixture(t)
	if code := runStopProcessCommand([]string{"--pid", strconv.Itoa(target.Process.Pid), "--starttime", strconv.FormatUint(tstart, 10)}, &bytes.Buffer{}); code != 0 {
		t.Fatalf("non-leader stop exit %d, want 0", code)
	}
}
