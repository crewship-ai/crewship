package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// outcomeContainer answers the stop probe with a fixed token and the
// inspect with a fixed state, so each case names one runtime observation.
type outcomeContainer struct {
	provider.ContainerProvider
	probe      string // "" makes Exec fail
	state      string
	inspectErr error
}

func (c outcomeContainer) Exec(context.Context, provider.ExecConfig) (*provider.ExecResult, error) {
	if c.probe == "" {
		return nil, errors.New("exec unavailable")
	}
	return &provider.ExecResult{Reader: io.NopCloser(strings.NewReader(c.probe))}, nil
}

func (c outcomeContainer) ContainerStatus(context.Context, string) (*provider.ContainerStatus, error) {
	return &provider.ContainerStatus{State: c.state}, c.inspectErr
}

func outcomeRecord(t *testing.T, store *memState, run RunState) {
	t.Helper()
	raw, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set(t.Context(), "agent_runs", run.ID, raw); err != nil {
		t.Fatal(err)
	}
}

// #2879: "positively established" means the durable ownership records are
// readable, none of this agent's records is non-terminal, and this process
// owns no invocation of it. Anything short of that is a refusal, and the
// refusal says whether the runtime could not be read or a process may live.
func TestStopAgentOutcome(t *testing.T) {
	live := RunState{ID: "live-run", AgentID: "a", AgentSlug: "a", ContainerID: "c", Status: "running"}
	cases := []struct {
		name      string
		state     func(t *testing.T) provider.StateProvider
		container provider.ContainerProvider
		want      AgentStopOutcome
		wantErr   error
	}{
		{
			name:  "idle_no_records",
			state: func(*testing.T) provider.StateProvider { return newMemState() },
			want:  AgentStopAlreadyStopped,
		},
		{
			name: "idle_only_terminal_records_and_other_agents",
			state: func(t *testing.T) provider.StateProvider {
				s := newMemState()
				for i, status := range []string{"completed", "error", "failed", "cancelled", "stopped"} {
					outcomeRecord(t, s, RunState{ID: "done-" + string(rune('a'+i)), AgentID: "a", AgentSlug: "a", ContainerID: "c", Status: status})
				}
				outcomeRecord(t, s, RunState{ID: "other", AgentID: "b", AgentSlug: "b", ContainerID: "c", Status: "running"})
				_ = s.Set(t.Context(), "agent_runs", "corrupt-other", []byte(`{"agent_id":"b","started_at":7}`))
				return s
			},
			// Exec would fail: another agent's live run must not be probed.
			container: outcomeContainer{state: "running"},
			want:      AgentStopAlreadyStopped,
		},
		{
			name: "recovered_run_confirmed_absent",
			state: func(t *testing.T) provider.StateProvider {
				s := newMemState()
				outcomeRecord(t, s, live)
				return s
			},
			container: outcomeContainer{probe: "ABSENT", state: "running"},
			want:      AgentStopStopped,
		},
		{
			name: "live_unowned_process_persists",
			state: func(t *testing.T) provider.StateProvider {
				s := newMemState()
				outcomeRecord(t, s, live)
				return s
			},
			container: outcomeContainer{probe: "PRESENT", state: "running"},
			wantErr:   ErrStopNotConfirmed,
		},
		{
			name: "live_unowned_container_running_exec_refused",
			state: func(t *testing.T) provider.StateProvider {
				s := newMemState()
				outcomeRecord(t, s, live)
				return s
			},
			container: outcomeContainer{state: "running"},
			wantErr:   ErrStopNotConfirmed,
		},
		{
			name: "live_unowned_legacy_record_without_location",
			state: func(t *testing.T) provider.StateProvider {
				s := newMemState()
				outcomeRecord(t, s, RunState{ID: "legacy", AgentID: "a", Status: "running"})
				return s
			},
			container: outcomeContainer{state: "running"},
			wantErr:   ErrStopNotConfirmed,
		},
		{
			name: "unknown_non_terminal_status",
			state: func(t *testing.T) provider.StateProvider {
				s := newMemState()
				outcomeRecord(t, s, RunState{ID: "odd", AgentID: "a", AgentSlug: "a", ContainerID: "c", Status: "starting"})
				return s
			},
			container: outcomeContainer{probe: "ABSENT", state: "running"},
			wantErr:   ErrStopNotConfirmed,
		},
		{
			name: "unattributable_corrupt_record",
			state: func(t *testing.T) provider.StateProvider {
				s := newMemState()
				_ = s.Set(t.Context(), "agent_runs", "bad", []byte("invalid"))
				return s
			},
			wantErr: ErrStopNotConfirmed,
		},
		{
			name: "unavailable_state_list_fails",
			state: func(*testing.T) provider.StateProvider {
				return stopReviewState{list: func(context.Context) (map[string][]byte, error) {
					return nil, errors.New("disk gone")
				}}
			},
			wantErr: ErrRuntimeUnavailable,
		},
		{
			name:    "unavailable_no_state_store",
			state:   func(*testing.T) provider.StateProvider { return nil },
			wantErr: ErrRuntimeUnavailable,
		},
		{
			name: "unavailable_container_daemon_unreachable",
			state: func(t *testing.T) provider.StateProvider {
				s := newMemState()
				outcomeRecord(t, s, live)
				return s
			},
			container: outcomeContainer{inspectErr: errors.New("daemon unreachable")},
			wantErr:   ErrRuntimeUnavailable,
		},
		{
			name: "unavailable_no_container_provider_for_recovered_run",
			state: func(t *testing.T) provider.StateProvider {
				s := newMemState()
				outcomeRecord(t, s, live)
				return s
			},
			wantErr: ErrRuntimeUnavailable,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := New(tc.container, tc.state(t), slog.Default())
			ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
			defer cancel()
			got, err := o.StopAgentOutcome(ctx, "a")
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				other := ErrRuntimeUnavailable
				if tc.wantErr == ErrRuntimeUnavailable {
					other = ErrStopNotConfirmed
				}
				if errors.Is(err, other) {
					t.Fatalf("err = %v carries both refusal classes", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected refusal: %v", err)
			}
			if got != tc.want {
				t.Fatalf("outcome = %q, want %q", got, tc.want)
			}
		})
	}
}

// A possibly live process outranks an unreadable runtime: a mixed refusal is
// stop_not_confirmed, never runtime_unavailable alone.
func TestStopAgentOutcome_MixedRefusalIsNotConfirmed(t *testing.T) {
	s := newMemState()
	outcomeRecord(t, s, RunState{ID: "live-run", AgentID: "a", AgentSlug: "a", ContainerID: "c", Status: "running"})
	_ = s.Set(t.Context(), "agent_runs", "bad", []byte("invalid"))
	o := New(outcomeContainer{inspectErr: errors.New("daemon unreachable")}, s, slog.Default())
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	_, err := o.StopAgentOutcome(ctx, "a")
	if !errors.Is(err, ErrStopNotConfirmed) {
		t.Fatalf("err = %v, want ErrStopNotConfirmed", err)
	}
}

// An owned invocation is stopped, not "already stopped", even though no
// durable record exists yet (tracking precedes the durable running write).
func TestStopAgentOutcome_OwnedInvocationIsStopped(t *testing.T) {
	o := New(nil, newMemState(), slog.Default())
	req := AgentRunRequest{AgentID: "a", RunID: "preparation"}
	_, finish := o.trackAgentRun(context.Background(), &req)
	go func() {
		time.Sleep(20 * time.Millisecond)
		finish()
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	got, err := o.StopAgentOutcome(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if got != AgentStopStopped {
		t.Fatalf("outcome = %q, want %q", got, AgentStopStopped)
	}
}
