//go:build linux

package orchestrator

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// #5510: a detached run whose tmux session was the last one leaves no tmux
// server behind (exit-empty). The client then answers "no server running",
// and the probe used to print UNKNOWN for ever, so the detached hold, the
// agent stop and the wrapper's own wait loop never saw the runtime end.
//
// Absence now needs three pieces of evidence: no server per the client, no
// tmux process in a complete process table, and an empty pane process group.
// Every fixture below is private: a fake tmux client, a fixture /proc for
// the tmux scan, and real (test-owned) processes for the pane group.

const noServerTmux = `echo "no server running on /tmp/tmux-1001/default" >&2; exit 1`

// hostProbeContainer runs each probe script on the test host with a fake
// tmux client first on PATH. No Docker daemon or tmux server is involved.
func hostProbeContainer(t *testing.T, tmuxScript string) *mockContainer {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte("#!/bin/sh\n"+tmuxScript+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	m := &mockContainer{}
	m.execFn = func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, cfg.Cmd[0], cfg.Cmd[1:]...)
		cmd.Env = []string{"PATH=" + bin + ":" + os.Getenv("PATH")}
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("host probe failed: %w", err)
		}
		return &provider.ExecResult{ExecID: "host-probe", Reader: io.NopCloser(strings.NewReader(string(out)))}, nil
	}
	return m
}

// fixtureProc builds a process table for the tmux scan.
func fixtureProc(t *testing.T, mounts string, comms map[string]string, withPID1 bool) string {
	t.Helper()
	proc := t.TempDir()
	write := func(rel, body string) {
		path := filepath.Join(proc, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("self/mountinfo", mounts)
	if withPID1 {
		write("1/comm", "entrypoint\n")
	}
	for pid, comm := range comms {
		write(pid+"/comm", comm)
	}
	return proc
}

const plainMounts = "25 29 0:23 / /proc rw,nosuid,nodev,noexec,relatime shared:12 - proc proc rw\n"

// startPane runs the production pane identity writer as its own process
// group leader (as tmux's setsid does), optionally leaving a SIGHUP-ignoring
// child in the group, and returns the identity file once the leader exited.
func startPane(t *testing.T, survivingChild bool) (paneFile string, pgid int) {
	t.Helper()
	paneFile = filepath.Join(t.TempDir(), tmuxPaneIdentityName)
	script := tmuxPaneIdentityWriter(paneFile)
	if survivingChild {
		script += "\n(trap '' HUP; exec sleep 60) </dev/null >/dev/null 2>&1 &"
	}
	cmd := exec.Command("sh", "-c", script+"\nexit 0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	pgid = cmd.Process.Pid
	if survivingChild {
		t.Cleanup(func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })
	}
	if _, err := os.Stat(paneFile); err != nil {
		t.Fatalf("pane leader recorded no identity: %v", err)
	}
	return paneFile, pgid
}

func probeOrchestrator(t *testing.T, tmux, paneFile, proc string) *Orchestrator {
	t.Helper()
	o := New(hostProbeContainer(t, tmux), newMemState(), covQuietLogger())
	o.tmuxProbePaths = func(RunLocation) (string, string) { return paneFile, proc }
	return o
}

func TestTmuxNoServerIsConfirmedAbsence(t *testing.T) {
	deadPane, _ := startPane(t, false)
	cleanProc := fixtureProc(t, plainMounts, map[string]string{"7": "sleep\n"}, true)
	cases := []struct {
		name       string
		tmux       string
		pane       string
		wantAbsent bool
	}{
		{"no server on existing socket", noServerTmux, deadPane, true},
		{"socket removed", `echo "error connecting to /tmp/tmux-1001/default (No such file or directory)" >&2; exit 1`, deadPane, true},
		{"connection refused", `echo "error connecting to /tmp/tmux-1001/default (Connection refused)" >&2; exit 1`, deadPane, true},
		{"no server but no pane identity", noServerTmux, filepath.Join(t.TempDir(), "missing"), false},
		{"broken client", `exit 2`, deadPane, false},
		{"permission denied", `echo "error connecting to /tmp/tmux-1001/default (Permission denied)" >&2; exit 1`, deadPane, false},
		{"protocol mismatch", `echo "protocol version mismatch (client 8, server 7)" >&2; exit 1`, deadPane, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := probeOrchestrator(t, tc.tmux, tc.pane, cleanProc)
			loc := RunLocation{ContainerID: "host-fixture", AgentSlug: "fixture", RunID: fmt.Sprintf("run_noserver%d", time.Now().UnixNano())}
			alive, aliveErr := o.RunIsAliveAt(t.Context(), loc)
			stopped, stopErr := o.StopRunAt(t.Context(), loc)
			if tc.wantAbsent {
				if aliveErr != nil || alive {
					t.Fatalf("RunIsAliveAt = %v, %v; want confirmed absence", alive, aliveErr)
				}
				if stopErr != nil || !stopped {
					t.Fatalf("StopRunAt = %v, %v; want confirmed absence", stopped, stopErr)
				}
				return
			}
			if aliveErr == nil {
				t.Fatalf("RunIsAliveAt answered %v without proof; want an error", alive)
			}
			if stopErr == nil || stopped {
				t.Fatalf("StopRunAt = %v, %v without proof; want an error", stopped, stopErr)
			}
		})
	}
}

// Review S1: a pane child that ignores SIGHUP outlives a killed tmux server.
// The run is not gone until that child is.
func TestTmuxNoServerKeepsSurvivingPaneChild(t *testing.T) {
	paneFile, pgid := startPane(t, true)
	// What a killed server delivers to its panes.
	_ = syscall.Kill(-pgid, syscall.SIGHUP)
	o := probeOrchestrator(t, noServerTmux, paneFile, fixtureProc(t, plainMounts, nil, true))
	loc := RunLocation{ContainerID: "host-fixture", AgentSlug: "fixture", RunID: fmt.Sprintf("run_survivor%d", time.Now().UnixNano())}
	if alive, err := o.RunIsAliveAt(t.Context(), loc); err == nil {
		t.Fatalf("RunIsAliveAt = %v with a live pane child; want UNKNOWN (error)", alive)
	}
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		alive, err := o.RunIsAliveAt(t.Context(), loc)
		if err == nil && !alive {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pane group gone but probe still says %v, %v", alive, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The detached hold must settle once the last session and its pane group
// are gone, instead of holding the agent busy for ever (#5510).
func TestDetachedHoldSettlesWhenTmuxServerExited(t *testing.T) {
	deadPane, _ := startPane(t, false)
	o := probeOrchestrator(t, noServerTmux, deadPane, fixtureProc(t, plainMounts, nil, true))
	req := covRunReq()
	req.RunID = fmt.Sprintf("run_noserverhold%d", time.Now().UnixNano())
	released := make(chan struct{})
	h := &detachedHold{runID: req.RunID, containerID: req.ContainerID, agentSlug: req.AgentSlug, execID: "detached", req: req,
		release: func() { close(released) }, startedAt: time.Now()}
	if err := o.registerDetachedHold(h, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		o.closeDetachedHold(h, true)
		t.Fatal("detached hold never settled although no tmux server or pane process exists")
	}
	if _, held := o.detachedHoldRunID(req.AgentID); held {
		t.Fatal("agent still refused after the hold settled")
	}
}

// The no-server answer is absence only with a complete, tmux-free process
// table and an empty pane group. Every other shape stays UNKNOWN.
func TestTmuxNoServerProbeRequiresCompleteProcessTable(t *testing.T) {
	deadPane, _ := startPane(t, false)
	livePane, _ := startPane(t, true)
	cases := []struct {
		name   string
		tmux   string
		mounts string
		comms  map[string]string
		noPID1 bool
		pane   string
		want   string
	}{
		{name: "no tmux process, pane gone", tmux: noServerTmux, mounts: plainMounts, comms: map[string]string{"7": "sleep\n", "9": "crewship-sideca\n"}, pane: deadPane, want: "ABSENT"},
		{name: "pane child alive", tmux: noServerTmux, mounts: plainMounts, pane: livePane, want: "UNKNOWN"},
		{name: "no pane identity", tmux: noServerTmux, mounts: plainMounts, pane: "", want: "UNKNOWN"},
		{name: "server process still visible", tmux: noServerTmux, mounts: plainMounts, comms: map[string]string{"7": "tmux: server\n"}, pane: deadPane, want: "UNKNOWN"},
		{name: "plain tmux process visible", tmux: noServerTmux, mounts: plainMounts, comms: map[string]string{"7": "tmux\n"}, pane: deadPane, want: "UNKNOWN"},
		{name: "hidepid hides other users", tmux: noServerTmux, mounts: "25 29 0:23 / /proc rw - proc proc rw,hidepid=2\n", pane: deadPane, want: "UNKNOWN"},
		{name: "hidepid invisible", tmux: noServerTmux, mounts: "25 29 0:23 / /proc rw - proc proc rw,hidepid=invisible\n", pane: deadPane, want: "UNKNOWN"},
		{name: "pid subset", tmux: noServerTmux, mounts: "25 29 0:23 / /proc rw - proc proc rw,subset=pid\n", pane: deadPane, want: "UNKNOWN"},
		{name: "hidepid off", tmux: noServerTmux, mounts: "25 29 0:23 / /proc rw - proc proc rw,hidepid=0\n", pane: deadPane, want: "ABSENT"},
		{name: "process table unreadable", tmux: noServerTmux, mounts: plainMounts, noPID1: true, pane: deadPane, want: "UNKNOWN"},
		{name: "client failure is not no-server", tmux: `exit 2`, mounts: plainMounts, pane: deadPane, want: "UNKNOWN"},
		{name: "roster without session", tmux: `case "$1" in has-session) exit 1;; list-sessions) echo other; exit 0;; esac`, mounts: plainMounts, want: "ABSENT"},
		{name: "roster with session", tmux: `case "$1" in has-session) exit 1;; list-sessions) echo crewship-fixture-run_x; exit 0;; esac`, mounts: plainMounts, want: "PRESENT"},
		{name: "session present", tmux: `exit 0`, mounts: plainMounts, want: "PRESENT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proc := fixtureProc(t, tc.mounts, tc.comms, !tc.noPID1)
			bin := t.TempDir()
			if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte("#!/bin/sh\n"+tc.tmux+"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			shells := [][]string{{"sh"}}
			if busybox, err := exec.LookPath("busybox"); err == nil {
				shells = append(shells, []string{busybox, "ash"})
			}
			for _, shell := range shells {
				args := append(append([]string(nil), shell[1:]...), "-c", tmuxSessionProbeAt("crewship-fixture-run_x", tc.pane, proc))
				cmd := exec.CommandContext(t.Context(), shell[0], args...)
				cmd.Env = []string{"PATH=" + bin + ":" + os.Getenv("PATH")}
				out, err := cmd.Output()
				if err != nil {
					t.Fatalf("%v: probe failed: %v", shell, err)
				}
				if got := strings.TrimSpace(string(out)); got != tc.want {
					t.Fatalf("%v: probe printed %q, want %q", shell, got, tc.want)
				}
			}
		})
	}
}

// The pane writes its identity only when it leads its own process group;
// a shell inside someone else's group (the direct fallback) must not.
func TestTmuxPaneIdentityRequiresGroupLeader(t *testing.T) {
	paneFile := filepath.Join(t.TempDir(), tmuxPaneIdentityName)
	cmd := exec.Command("sh", "-c", "sh -c \"$1\"; true", "outer", tmuxPaneIdentityWriter(paneFile))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // the outer shell leads, not the writer
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paneFile); !os.IsNotExist(err) {
		t.Fatalf("non-leader recorded a pane identity (stat err %v)", err)
	}
}
