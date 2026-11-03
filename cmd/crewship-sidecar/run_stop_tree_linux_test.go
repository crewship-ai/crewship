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

// A member that is still running when the leader is frozen can exit before the
// first scan reads it. Its setsid child is then reparented to the namespace
// init (or a subreaper above the leader) and shares neither the leader's
// session nor its ancestry. That process belonged to the run and still runs,
// so the stop must not be confirmed (#4797). The same holds when the member
// exited just before the stop began: the helper cannot tell the two apart.
func TestExactTreeStopRefusesConfirmationWhileAnOrphanedDescendantSurvives(t *testing.T) {
	for _, tc := range []struct {
		name         string
		duringFreeze bool
	}{
		{"member exits between leader stop and first scan", true},
		{"member exited before the stop", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// C is a running child of the leader; G is C's setsid child.
			leader, start, _ := startTreeFixture(t, `sh -c 'setsid sleep 300 & wait' & `, 6)
			child, grandchild := orphanableMember(t, leader.Process.Pid)
			childFd, err := unix.PidfdOpen(child, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer unix.Close(childFd)
			grandFd, err := unix.PidfdOpen(grandchild, 0)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = unix.PidfdSendSignal(grandFd, unix.SIGKILL, nil, 0)
				unix.Close(grandFd)
			})
			orphan := func() {
				if err := unix.PidfdSendSignal(childFd, unix.SIGKILL, nil, 0); err != nil {
					t.Errorf("kill member: %v", err)
					return
				}
				deadline := time.Now().Add(3 * time.Second)
				for {
					stat, err := readProcessStat(grandchild)
					if err == nil && stat.ppid != child {
						return
					}
					if time.Now().After(deadline) {
						t.Errorf("descendant %d never reparented: %+v %v", grandchild, stat, err)
						return
					}
					time.Sleep(time.Millisecond)
				}
			}
			if tc.duringFreeze {
				afterLeaderStopped = orphan
				t.Cleanup(func() { afterLeaderStopped = nil })
			} else {
				orphan()
			}
			var diagnostics bytes.Buffer
			confirmed, err := stopExactTree(leader.Process.Pid, start, &diagnostics)
			if pidfdExited(grandFd) {
				t.Fatal("fixture lost its orphaned descendant before the check")
			}
			if err != nil || confirmed {
				t.Fatalf("stop confirmed=%v err=%v while orphaned descendant %d still runs; diag=%s", confirmed, err, grandchild, diagnostics.String())
			}
			done := make(chan error, 1)
			go func() { done <- leader.Wait() }()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("leader survived an unconfirmed stop")
			}
		})
	}
}

// orphanableMember finds the leader's direct child that has a child of its own
// in a separate session, and returns both PIDs.
func orphanableMember(t *testing.T, leader int) (int, int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		procs, err := scanProcesses()
		if err != nil {
			t.Fatal(err)
		}
		byPID := map[int]processStat{}
		for _, p := range procs {
			byPID[p.pid] = p
		}
		for _, p := range procs {
			parent, ok := byPID[p.ppid]
			if ok && parent.ppid == leader && parent.session == leader && p.session == p.pid {
				return parent.pid, p.pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture member with a setsid child never appeared")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestMayBeOrphanOfTree(t *testing.T) {
	adopters := map[int]bool{1: true, 40: true}
	byPID := map[int]processStat{
		1:  {pid: 1, session: 1, start: 5},
		40: {pid: 40, ppid: 1, session: 40, start: 50},
	}
	for _, tc := range []struct {
		name string
		p    processStat
		want bool
	}{
		{"adopted by init in its own session", processStat{pid: 200, ppid: 1, session: 200, start: 100}, true},
		{"adopted by an ancestor subreaper", processStat{pid: 201, ppid: 40, session: 150, start: 100}, true},
		{"started in the leader's own tick", processStat{pid: 202, ppid: 1, session: 202, start: 100}, true},
		{"older than the leader", processStat{pid: 204, ppid: 1, session: 204, start: 99}, false},
		{"forked by the adopter in its session", processStat{pid: 205, ppid: 40, session: 40, start: 120}, false},
		{"parent is not an adopter", processStat{pid: 206, ppid: 77, session: 206, start: 120}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mayBeOrphanOfTree(tc.p, byPID, adopters, 100); got != tc.want {
				t.Fatalf("mayBeOrphanOfTree(%+v) = %v, want %v", tc.p, got, tc.want)
			}
		})
	}
	delete(byPID, 40)
	if !mayBeOrphanOfTree(processStat{pid: 207, ppid: 40, session: 40, start: 120}, byPID, adopters, 100) {
		t.Fatal("an adopter missing from the scan proved a process unrelated")
	}
}

func TestOrphanAdoptersCoverTheLeaderAncestryAndInit(t *testing.T) {
	leader, _ := startStopFixture(t)
	adopters, err := orphanAdopters(leader.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if !adopters[1] || !adopters[os.Getpid()] || !adopters[os.Getppid()] || adopters[leader.Process.Pid] {
		t.Fatalf("adopters %v miss init or the leader's ancestors, or include the leader", adopters)
	}
}
