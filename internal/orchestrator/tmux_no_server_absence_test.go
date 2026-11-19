package orchestrator

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// #5510: a detached run whose tmux session was the last one leaves no tmux
// server behind (exit-empty). The client then answers "no server running",
// and the probe used to print UNKNOWN for ever, so the detached hold, the
// agent stop and the wrapper's own wait loop never saw the runtime end.

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

// requireNoVisibleTmux skips when the host itself runs tmux: the absence
// evidence is the container's process table, which here is the host's.
func requireNoVisibleTmux(t *testing.T) {
	t.Helper()
	comms, _ := filepath.Glob("/proc/[0-9]*/comm")
	for _, c := range comms {
		if b, err := os.ReadFile(c); err == nil && strings.HasPrefix(string(b), "tmux") {
			t.Skip("a tmux process is visible on this host; the no-server evidence cannot be exercised here")
		}
	}
}

func TestTmuxNoServerIsConfirmedAbsence(t *testing.T) {
	requireNoVisibleTmux(t)
	cases := []struct {
		name       string
		tmux       string
		wantAbsent bool
	}{
		{"no server on existing socket", `echo "no server running on /tmp/tmux-1001/default" >&2; exit 1`, true},
		{"socket removed", `echo "error connecting to /tmp/tmux-1001/default (No such file or directory)" >&2; exit 1`, true},
		{"broken client", `exit 2`, false},
		{"permission denied", `echo "error connecting to /tmp/tmux-1001/default (Permission denied)" >&2; exit 1`, false},
		{"protocol mismatch", `echo "protocol version mismatch (client 8, server 7)" >&2; exit 1`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := New(hostProbeContainer(t, tc.tmux), newMemState(), covQuietLogger())
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
				t.Fatalf("RunIsAliveAt answered %v from an unusable client; want an error", alive)
			}
			if stopErr == nil || stopped {
				t.Fatalf("StopRunAt = %v, %v from an unusable client; want an error", stopped, stopErr)
			}
		})
	}
}

// The detached hold must settle once the last session is gone, instead of
// holding the agent busy for ever (#5510).
func TestDetachedHoldSettlesWhenTmuxServerExited(t *testing.T) {
	requireNoVisibleTmux(t)
	o := New(hostProbeContainer(t, `echo "no server running on /tmp/tmux-1001/default" >&2; exit 1`), newMemState(), covQuietLogger())
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
		t.Fatal("detached hold never settled although no tmux server exists")
	}
	if _, held := o.detachedHoldRunID(req.AgentID); held {
		t.Fatal("agent still refused after the hold settled")
	}
}

// The no-server answer is absence only with a complete, tmux-free process
// table. Every other shape stays UNKNOWN.
func TestTmuxNoServerProbeRequiresCompleteProcessTable(t *testing.T) {
	const noServer = `echo "no server running on /tmp/tmux-1001/default" >&2; exit 1`
	plainMounts := "25 29 0:23 / /proc rw,nosuid,nodev,noexec,relatime shared:12 - proc proc rw\n"
	cases := []struct {
		name   string
		tmux   string
		mounts string
		comms  map[string]string
		noPID1 bool
		want   string
	}{
		{name: "no tmux process", tmux: noServer, mounts: plainMounts, comms: map[string]string{"7": "sleep\n", "9": "crewship-sideca\n"}, want: "ABSENT"},
		{name: "server process still visible", tmux: noServer, mounts: plainMounts, comms: map[string]string{"7": "tmux: server\n"}, want: "UNKNOWN"},
		{name: "plain tmux process visible", tmux: noServer, mounts: plainMounts, comms: map[string]string{"7": "tmux\n"}, want: "UNKNOWN"},
		{name: "hidepid hides other users", tmux: noServer, mounts: "25 29 0:23 / /proc rw - proc proc rw,hidepid=2\n", want: "UNKNOWN"},
		{name: "hidepid invisible", tmux: noServer, mounts: "25 29 0:23 / /proc rw - proc proc rw,hidepid=invisible\n", want: "UNKNOWN"},
		{name: "pid subset", tmux: noServer, mounts: "25 29 0:23 / /proc rw - proc proc rw,subset=pid\n", want: "UNKNOWN"},
		{name: "hidepid off", tmux: noServer, mounts: "25 29 0:23 / /proc rw - proc proc rw,hidepid=0\n", want: "ABSENT"},
		{name: "process table unreadable", tmux: noServer, mounts: plainMounts, noPID1: true, want: "UNKNOWN"},
		{name: "client failure is not no-server", tmux: `exit 2`, mounts: plainMounts, want: "UNKNOWN"},
		{name: "roster without session", tmux: `case "$1" in has-session) exit 1;; list-sessions) echo other; exit 0;; esac`, mounts: plainMounts, want: "ABSENT"},
		{name: "roster with session", tmux: `case "$1" in has-session) exit 1;; list-sessions) echo crewship-fixture-run_x; exit 0;; esac`, mounts: plainMounts, want: "PRESENT"},
		{name: "session present", tmux: `exit 0`, mounts: plainMounts, want: "PRESENT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
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
			write("self/mountinfo", tc.mounts)
			if !tc.noPID1 {
				write("1/comm", "entrypoint\n")
			}
			for pid, comm := range tc.comms {
				write(pid+"/comm", comm)
			}
			bin := t.TempDir()
			if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte("#!/bin/sh\n"+tc.tmux+"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			shells := [][]string{{"sh"}}
			if busybox, err := exec.LookPath("busybox"); err == nil {
				shells = append(shells, []string{busybox, "ash"})
			}
			for _, shell := range shells {
				args := append(append([]string(nil), shell[1:]...), "-c", tmuxSessionProbeAt("crewship-fixture-run_x", proc))
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
