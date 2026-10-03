package orchestrator

import (
	"context"
	"errors"
	"github.com/crewship-ai/crewship/internal/provider"
	"io"
	"strings"
	"testing"
	"time"
)

const idleCensus = "30\n1001 1 0 docker-init\n1001 7 1 sleep\n1001 30 0 sh\n1002 12 0 crewship-sideca\n"

type idleProbeContainer struct {
	provider.ContainerProvider
	output  string
	running bool
	code    int
	err     error
}

func (c idleProbeContainer) Exec(context.Context, provider.ExecConfig) (*provider.ExecResult, error) {
	return &provider.ExecResult{ExecID: "probe", Reader: io.NopCloser(strings.NewReader(c.output))}, nil
}
func (c idleProbeContainer) ExecInspect(context.Context, string) (bool, int, error) {
	return c.running, c.code, c.err
}
func TestVerifyRuntimeIdlePreservesUnresolvedWork(t *testing.T) {
	for _, tc := range []struct {
		name, record, extra                string
		held, local, exposed, probeRunning bool
		code                               int
		wantErr                            bool
	}{
		{name: "idle"},
		{name: "sidecar_supervisor", extra: "1002 36 1 sh\n1002 37 36 sleep\n"},
		{name: "terminal", record: `{"status":"completed","container_id":"runtime"}`},
		{name: "failed_cli", record: `{"status":"error","container_id":"runtime"}`},
		{name: "other_runtime", record: `{"status":"running","container_id":"other"}`},
		{name: "recovered", record: `{"status":"running","container_id":"runtime"}`, wantErr: true},
		{name: "unknown", record: `{"status":"unknown","container_id":"runtime"}`, wantErr: true},
		{name: "unknown_location", record: `{"status":"running"}`, wantErr: true},
		{name: "corrupt", record: `{`, wantErr: true},
		{name: "held", held: true, wantErr: true},
		{name: "local", local: true, wantErr: true},
		{name: "exposed", exposed: true, wantErr: true},
		{name: "detached", extra: "1001 40 1 tmux: server\n", wantErr: true},
		{name: "background", extra: "1001 40 1 python\n", wantErr: true},
		{name: "root_background", extra: "0 40 1 bash\n", wantErr: true},
		{name: "unfinished_probe", probeRunning: true, wantErr: true},
		{name: "failed_probe", code: 127, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := newMemState()
			if tc.record != "" {
				_ = state.Set(t.Context(), "agent_runs", "run", []byte(tc.record))
			}
			o := New(idleProbeContainer{output: idleCensus + tc.extra + "CREWSHIP_IDLE_CENSUS_END\n", running: tc.probeRunning, code: tc.code}, state, quietLifecycleLogger())
			if tc.held {
				o.crews["crew"] = &crewState{containerID: "runtime", holds: 1}
			}
			if tc.local {
				o.agentRuns.Store("run", &agentRunControl{location: RunLocation{ContainerID: "runtime"}})
			}
			o.SetContainerBusyProbe(func(context.Context, string, string) bool { return tc.exposed })
			if err := o.VerifyRuntimeIdle(t.Context(), "crew", "runtime"); (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}
func TestVerifyRuntimeIdleUnknownStateFailsClosed(t *testing.T) {
	o := New(idleProbeContainer{output: idleCensus + "CREWSHIP_IDLE_CENSUS_END\n"}, stopReviewState{list: func(context.Context) (map[string][]byte, error) { return nil, errors.New("offline") }}, quietLifecycleLogger())
	if err := o.VerifyRuntimeIdle(t.Context(), "crew", "runtime"); err == nil {
		t.Fatal("state failure permitted replacement")
	}
}
func TestIdleProcessTreeRejectsIncompleteAndUnknownBaselines(t *testing.T) {
	if err := idleProcessTree(idleCensus); err == nil {
		t.Fatal("truncated census accepted without completion marker")
	}
	for _, data := range []string{"", "30\n", "30\n1001 30 0 sh\n", strings.Replace(idleCensus, "1001 30 0 sh\n", "", 1), strings.Replace(idleCensus, "1 0 docker-init", "1 0 custom-init", 1), idleCensus + "1001 7 1 sleep\n"} {
		if err := idleProcessTree(data + "CREWSHIP_IDLE_CENSUS_END\n"); err == nil {
			t.Fatalf("accepted incomplete census %q", data)
		}
	}
}

type idleStopContainer struct {
	provider.ContainerProvider
	calls   int
	stopped bool
	err     error
}

func (c *idleStopContainer) StopUnusedCrewRuntime(context.Context, string, string) (bool, error) {
	c.calls++
	return c.stopped, c.err
}
func TestTTLUsesManagedAdmissionAndRetainsDeferredCandidate(t *testing.T) {
	for _, stop := range []bool{false, true} {
		c := &idleStopContainer{stopped: stop}
		o := New(c, newMemState(), quietLifecycleLogger())
		o.crews["crew"] = &crewState{containerID: "runtime", ttl: time.Hour, lastActivity: time.Now().Add(-2 * time.Hour)}
		o.checkTTLs(t.Context())
		if c.calls != 1 {
			t.Fatal("managed stop was bypassed")
		}
		_, known := o.CrewActivity("crew")
		if known == stop {
			t.Fatalf("candidate known=%v stopped=%v", known, stop)
		}
	}
}
