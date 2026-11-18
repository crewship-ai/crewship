//go:build unix

package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/provider/bbolt"
	dockerprovider "github.com/crewship-ai/crewship/internal/provider/docker"
)

type removedContainerStatus struct {
	provider.ContainerProvider
	state string
	err   error
}

// Lose the owned Docker API endpoint after constructing the real provider.
// This exercises a transport outage without stopping a shared Docker daemon.
func TestStopRunAtHostRunDockerAPIOutage(t *testing.T) {
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("API-Version", "1.44")
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			_, _ = w.Write([]byte("OK"))
		case strings.HasSuffix(r.URL.Path, "/version"):
			_, _ = w.Write([]byte(`{"Version":"owned-test","ApiVersion":"1.44","Os":"linux"}`))
		case strings.HasSuffix(r.URL.Path, "/info"):
			_, _ = w.Write([]byte(`{"DockerRootDir":"/owned-daemon-fixture","CgroupVersion":"2"}`))
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(daemon.Close)
	t.Setenv("DOCKER_HOST", "tcp://"+strings.TrimPrefix(daemon.URL, "http://"))
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	// Provider startup can discover other runtimes. Never reconcile them in
	// a test, and use a unique container namespace even in report-only mode.
	t.Setenv("CREWSHIP_STRANDED_CREWS", "report")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	container, err := dockerprovider.New(ctx, dockerprovider.Config{OutputBasePath: t.TempDir(), ContainerPrefix: fmt.Sprintf("stop-outage-%d-%d", os.Getpid(), time.Now().UnixNano())}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = container.DockerClient().Close() })
	daemon.Close()
	root := t.TempDir()
	state, err := bbolt.New(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	run := RunState{ID: "docker-outage", Status: "running", ContainerID: "container", ExecID: "exec", AgentSlug: "agent", Output: &RunOutputState{
		Version: 2, Directory: filepath.Join(root, "docker-outage", "output"), Deadline: time.Now().Add(time.Hour), Adapter: "CLAUDE_CODE",
	}}
	raw, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	if err = state.Set(t.Context(), "agent_runs", run.ID, raw); err != nil {
		t.Fatal(err)
	}
	o := &Orchestrator{state: state, container: container}
	o.SetHostOutputRoot(root)
	stopCtx, stopCancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer stopCancel()
	stopped, err := o.StopRunAt(stopCtx, RunLocation{ContainerID: run.ContainerID, AgentSlug: run.AgentSlug, RunID: run.ID})
	refusal := classifyStopRefusal([]error{err})
	if stopped || !errors.Is(refusal, ErrRuntimeUnavailable) || errors.Is(refusal, ErrStopNotConfirmed) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		t.Fatalf("stopped=%v refusal=%v, want runtime unavailable without confirming stop", stopped, refusal)
	}
}

func (c *removedContainerStatus) ContainerStatus(context.Context, string) (*provider.ContainerStatus, error) {
	if c.err != nil {
		return nil, c.err
	}
	return &provider.ContainerStatus{State: c.state}, nil
}

// A protected run whose container was removed (agent deletion) has no
// restorable execution, so probing it fails. That used to be reported as an
// identity error and left the run pending forever. Removal of the whole
// container is authoritative proof that the run is gone, exactly as it already
// is on the tmux path; any other inspect failure is still no proof.
//
// The fixture holds no spool on purpose: the shared retained fixture skips on
// the workload UIDs ordinary CI runs as, and this contract needs only a host
// run whose probe fails, whichever step of the probe fails.
func TestStopRunAtHostRunRemovedContainerIsStopped(t *testing.T) {
	for _, tc := range []struct {
		name        string
		state       string
		status      error
		wantStop    bool
		wantErr     bool
		wantRefusal error
	}{
		{"removed container", "", provider.ErrContainerNotFound, true, false, nil},
		{"stopped container", "stopped", nil, true, false, nil},
		{"dead container", "error", nil, true, false, nil},
		{"unknown state is no proof", "paused", nil, false, true, nil},
		{"inspect unavailable", "", errors.New("daemon unreachable"), false, true, ErrRuntimeUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			state, err := bbolt.New(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = state.Close() })
			run := RunState{ID: "removed-container", Status: "running", ContainerID: "container", ExecID: "exec", AgentSlug: "agent", Output: &RunOutputState{
				Version: 2, Directory: filepath.Join(root, "removed-container", "output"), Deadline: time.Now().Add(time.Hour), Adapter: "CLAUDE_CODE",
			}}
			raw, _ := json.Marshal(run)
			if err = state.Set(t.Context(), "agent_runs", run.ID, raw); err != nil {
				t.Fatal(err)
			}
			o := &Orchestrator{state: state, container: &removedContainerStatus{state: tc.state, err: tc.status}}
			o.SetHostOutputRoot(root)
			stopped, err := o.StopRunAt(t.Context(), RunLocation{ContainerID: run.ContainerID, AgentSlug: "agent", RunID: run.ID})
			if stopped != tc.wantStop || (err != nil) != tc.wantErr {
				t.Fatalf("stopped=%v err=%v, want stopped=%v err=%v", stopped, err, tc.wantStop, tc.wantErr)
			}
			if tc.wantRefusal != nil {
				refusal := classifyStopRefusal([]error{err})
				if !errors.Is(refusal, tc.wantRefusal) || errors.Is(refusal, ErrStopNotConfirmed) {
					t.Fatalf("refusal=%v, want only %v", refusal, tc.wantRefusal)
				}
			}
		})
	}
}
