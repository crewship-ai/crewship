package api

// The delegation / mission path had the same defect the chat path did: when
// RunAgentForAssignment returns an error, runAssignment called finishAssignment
// with result="", so `result_summary` was written NULL and every text event the
// sub-agent emitted was discarded. Before the in-band gate that path returned
// nil for an exit-0 refusal and the output WAS delivered as the task result, so
// the gate turned "wrong status, output kept" into "right status, output lost".
//
// These tests pin the fix end-to-end through runAssignment: an in-band failure
// keeps the assignment FAILED *and* persists what the sub-agent said.

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
)

// inbandAsgState is the minimal in-memory provider.StateProvider the
// orchestrator needs to record run state during these runs.
type inbandAsgState struct {
	mu   sync.Mutex
	data map[string]map[string][]byte
}

func newInbandAsgState() *inbandAsgState {
	return &inbandAsgState{data: map[string]map[string][]byte{}}
}
func (s *inbandAsgState) Get(_ context.Context, bucket, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data[bucket][key], nil
}
func (s *inbandAsgState) Set(_ context.Context, bucket, key string, v []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data[bucket] == nil {
		s.data[bucket] = map[string][]byte{}
	}
	s.data[bucket][key] = v
	return nil
}
func (s *inbandAsgState) Delete(_ context.Context, bucket, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data[bucket], key)
	return nil
}
func (s *inbandAsgState) List(_ context.Context, bucket string) (map[string][]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data[bucket], nil
}
func (s *inbandAsgState) ListByPrefix(_ context.Context, bucket, prefix string) (map[string][]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string][]byte{}
	for k, v := range s.data[bucket] {
		if strings.HasPrefix(k, prefix) {
			out[k] = v
		}
	}
	return out, nil
}
func (s *inbandAsgState) Close() error { return nil }

// inbandAsgProvider streams `stream` from the agent exec and exits 0 from every
// exec. The agent exec is told apart by carrying Env — RunAgent always stamps
// CREWSHIP_SECRETS_DIR / CREWSHIP_OUTPUT_DIR on it and never on the setup execs
// (the same discriminator chatbridge's scriptedContainer uses).
type inbandAsgProvider struct{ stream string }

func (inbandAsgProvider) EnsureCrewRuntime(context.Context, provider.CrewConfig) (string, error) {
	return "container-inband", nil
}
func (inbandAsgProvider) StopCrewRuntime(context.Context, string) error   { return nil }
func (inbandAsgProvider) RemoveCrewRuntime(context.Context, string) error { return nil }
func (inbandAsgProvider) ContainerStatus(context.Context, string) (*provider.ContainerStatus, error) {
	return &provider.ContainerStatus{State: "running"}, nil
}
func (inbandAsgProvider) ContainerStats(context.Context, string) (*provider.ContainerMetrics, error) {
	return nil, nil
}
func (p inbandAsgProvider) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	if len(cfg.Env) > 0 {
		return &provider.ExecResult{
			ExecID: "agent-exec",
			Reader: io.NopCloser(strings.NewReader(p.stream)),
		}, nil
	}
	return &provider.ExecResult{ExecID: "noop", Reader: io.NopCloser(strings.NewReader(""))}, nil
}
func (inbandAsgProvider) ExecInspect(context.Context, string) (bool, int, error) {
	return false, 0, nil // exit 0 — the whole point
}
func (inbandAsgProvider) CrewContainerName(_ string, slug string) string { return "crew-" + slug }
func (inbandAsgProvider) CopyToContainer(context.Context, string, string, io.Reader) error {
	return nil
}

var _ provider.StateProvider = (*inbandAsgState)(nil)
var _ provider.ContainerProvider = inbandAsgProvider{}

// runInbandAssignment drives runAssignment against a sub-agent whose CLI exits 0
// and streams `stream`, then returns the assignment's terminal row.
func runInbandAssignment(t *testing.T, asgID, stream string) (status, result, errMsg string) {
	t.Helper()
	h, wsID, crewID, leadID, workerID, chatID := covAsgRig(t)
	h.orch = orchestrator.New(inbandAsgProvider{stream: stream}, newInbandAsgState(), newTestLogger())
	insertAssignment(t, h.db, asgID, wsID, chatID, leadID, workerID, "PENDING")

	h.runAssignment(context.Background(), asgID, createAssignmentBody{
		TargetSlug: "asg-worker", Task: "t", CrewID: crewID, WorkspaceID: wsID, ChatID: chatID,
	}, targetAgentInfo{ID: workerID, Slug: "asg-worker", Name: "Worker", CrewSlug: "asg"})

	if err := h.db.QueryRow(
		`SELECT status, COALESCE(result_summary,''), COALESCE(error_message,'')
		   FROM assignments WHERE id = ?`, asgID).Scan(&status, &result, &errMsg); err != nil {
		t.Fatalf("query assignment: %v", err)
	}
	return status, result, errMsg
}

func TestRunAssignment_InBandFailure_KeepsSubAgentOutput(t *testing.T) {
	const said = "I looked at three of the five files and then refused to continue."
	status, result, errMsg := runInbandAssignment(t, "asg-inband-1",
		`{"type":"stream_event","event":{"type":"content_block_delta",`+
			`"delta":{"type":"text_delta","text":"`+said+`"}}}`+"\n"+
			`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"refused"}`+"\n")

	// The status must be the honest one — this is the gate working.
	if status != "FAILED" {
		t.Errorf("assignment status = %q, want FAILED", status)
	}
	if !strings.Contains(errMsg, "agent reported a failed run") {
		t.Errorf("error_message = %q, want the in-band failure reason", errMsg)
	}
	// ...and the sub-agent's words must not be thrown away with it. The
	// delegating agent reads result_summary; NULL here means the delegation
	// produced nothing it can act on or report.
	if !strings.Contains(result, said) {
		t.Errorf("result_summary = %q, want the sub-agent's output preserved", result)
	}
}

// The control: a transport-class failure (no in-band signal) keeps the previous
// behaviour. There is no agent output to preserve in that case, and widening the
// change to every error class is a bigger decision than this fix.
func TestRunAssignment_ExecFailure_StillHasNoResult(t *testing.T) {
	h, wsID, crewID, leadID, workerID, chatID := covAsgRig(t)
	// A real orchestrator with no container provider fails before the exec.
	h.orch = orchestrator.New(nil, newInbandAsgState(), newTestLogger())
	insertAssignment(t, h.db, "asg-inband-2", wsID, chatID, leadID, workerID, "PENDING")

	h.runAssignment(context.Background(), "asg-inband-2", createAssignmentBody{
		TargetSlug: "asg-worker", Task: "t", CrewID: crewID, WorkspaceID: wsID, ChatID: chatID,
	}, targetAgentInfo{ID: workerID, Slug: "asg-worker", Name: "Worker", CrewSlug: "asg"})

	var status, result string
	if err := h.db.QueryRow(
		`SELECT status, COALESCE(result_summary,'') FROM assignments WHERE id = 'asg-inband-2'`).
		Scan(&status, &result); err != nil {
		t.Fatalf("query assignment: %v", err)
	}
	if status != "FAILED" {
		t.Errorf("assignment status = %q, want FAILED", status)
	}
	if result != "" {
		t.Errorf("result_summary = %q, want empty for a non-in-band failure", result)
	}
}
