package design

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeEngine runs a shell script in place of node. The script may write its
// PID to $PIDFILE so the test can prove the process is gone afterwards.
func fakeEngine(t *testing.T, startup, work time.Duration, script string) (Engine, string) {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "pid")
	return Engine{StartupTimeout: startup, WorkTimeout: work, command: func(ctx context.Context) (*exec.Cmd, error) {
		cmd := exec.CommandContext(ctx, "sh", "-c", script)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "PIDFILE=" + pidFile, "READY=" + readyMarker}
		return cmd, nil
	}}, pidFile
}

func assertGone(t *testing.T, pidFile string) {
	t.Helper()
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("engine never recorded its pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if err = syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("engine process %d still exists after Run returned: %v", pid, err)
	}
}

func TestEngineSeparatesStartupFromWorkBudget(t *testing.T) {
	const printReady = `printf '%s' "$READY"; `
	tests := []struct {
		name      string
		startup   time.Duration
		work      time.Duration
		script    string
		wantPhase Phase // empty: success
	}{
		// Start-up longer than the whole work budget is still accepted.
		{"slow startup within budget", 3 * time.Second, 200 * time.Millisecond,
			`sleep 1; ` + printReady + `cat >/dev/null; printf '{"order":["slow"]}'`, ""},
		{"hang before ready", 200 * time.Millisecond, 5 * time.Second,
			`echo $$ > "$PIDFILE"; exec sleep 30`, PhaseStartup},
		{"hang after ready", 5 * time.Second, 200 * time.Millisecond,
			`echo $$ > "$PIDFILE"; ` + printReady + `exec sleep 30`, PhaseWork},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine, pidFile := fakeEngine(t, tt.startup, tt.work, tt.script)
			started := time.Now()
			result, err := engine.Run(t.Context(), Request{})
			elapsed := time.Since(started)
			if tt.wantPhase == "" {
				if err != nil {
					t.Fatalf("Run: %v", err)
				}
				if len(result.Order) != 1 || result.Order[0] != "slow" {
					t.Fatalf("result = %+v", result)
				}
				return
			}
			var timeout *TimeoutError
			if !errors.As(err, &timeout) || timeout.Phase != tt.wantPhase {
				t.Fatalf("Run error = %v, want %s timeout", err, tt.wantPhase)
			}
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("timeout %v must remain ErrUnavailable for API mapping", err)
			}
			if !strings.Contains(err.Error(), string(tt.wantPhase)) {
				t.Fatalf("error %q does not name the %s phase", err, tt.wantPhase)
			}
			if bound := tt.startup + tt.work + killGrace + 2*time.Second; elapsed > bound {
				t.Fatalf("hang ended after %s, want within %s", elapsed, bound)
			}
			assertGone(t, pidFile)
		})
	}
}

// A descendant that inherits stdout must not hold Run beyond its budget.
func TestEngineTimeoutIsBoundedWhenChildKeepsOutputOpen(t *testing.T) {
	engine, _ := fakeEngine(t, time.Second, 200*time.Millisecond, `printf '%s' "$READY"; sleep 3; true`)
	started := time.Now()
	_, err := engine.Run(t.Context(), Request{})
	var timeout *TimeoutError
	if !errors.As(err, &timeout) || timeout.Phase != PhaseWork {
		t.Fatalf("Run error = %v, want work timeout", err)
	}
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond+killGrace+time.Second {
		t.Fatalf("Run returned after %s; the inherited pipe held it", elapsed)
	}
}

func TestEngineCallerCancellationIsUnavailableNotTimeout(t *testing.T) {
	engine, pidFile := fakeEngine(t, 5*time.Second, 5*time.Second, `echo $$ > "$PIDFILE"; exec sleep 30`)
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	_, err := engine.Run(ctx, Request{})
	var timeout *TimeoutError
	if !errors.Is(err, ErrUnavailable) || errors.As(err, &timeout) {
		t.Fatalf("Run error = %v, want caller cancellation as ErrUnavailable", err)
	}
	assertGone(t, pidFile)
}

func TestEngineWithoutReadyMarkerIsUnavailable(t *testing.T) {
	engine, _ := fakeEngine(t, time.Second, time.Second, `printf '{"order":["x"]}'`)
	if _, err := engine.Run(t.Context(), Request{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Run error = %v, want ErrUnavailable", err)
	}
}
