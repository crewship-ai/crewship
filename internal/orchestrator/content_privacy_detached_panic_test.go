package orchestrator

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

func TestDetachedProbePanicRetainsUncertainRuntimeOwnership(t *testing.T) {
	const marker = "synthetic-private-detached-panic-value"
	for _, private := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "private"}[private], func(t *testing.T) {
			c := covNewRunContainer(covRunOpts{})
			c.route = func(provider.ExecConfig) (*provider.ExecResult, error) { panic(marker) }
			var logs bytes.Buffer
			state := newMemState()
			o := New(c, state, slog.New(slog.NewJSONHandler(&logs, nil)))
			j := &covJournal{}
			o.SetJournal(j)
			req := covRunReq()
			if private {
				req = req.WithStrictContentPrivacy()
			}
			outcomeRecord(t, state, RunState{ID: req.RunID, AgentID: req.AgentID, Status: "running"})
			o.runSem = make(chan struct{}, 1)
			release, err := o.acquireRunSlot(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			released, finalized := false, false
			h := &detachedHold{runID: req.RunID, containerID: req.ContainerID, agentSlug: req.AgentSlug, req: req, done: make(chan struct{}), alertAfter: time.Hour,
				release: func() { released = true; release() }, finalize: func() { finalized = true }}
			o.detached[h.runID] = h
			var caught any
			func() { defer func() { caught = recover() }(); o.watchDetachedHold(h, time.Millisecond) }()
			if private && caught != nil || !private && caught != marker {
				t.Fatal("unexpected panic boundary behavior")
			}
			if released || finalized || len(o.runSem) != 1 {
				t.Fatal("unconfirmed runtime lost capacity or cleanup ownership")
			}
			if id, held := o.detachedHoldRunID(req.AgentID); !held || id != req.RunID {
				t.Fatal("unconfirmed runtime lost admission fence")
			}
			select {
			case <-h.done:
				t.Fatal("panic signaled terminal completion")
			default:
			}
			raw, err := state.Get(t.Context(), "agent_runs", req.RunID)
			var run RunState
			if err != nil || json.Unmarshal(raw, &run) != nil || run.Status != "running" || len(j.byType("exec.command")) != 0 {
				t.Fatal("panic emitted terminal state or journal entry")
			}
			if private && (!strings.Contains(logs.String(), "private detached supervision interrupted") || strings.Contains(logs.String(), marker) || strings.Contains(logs.String(), "goroutine")) {
				t.Fatal("missing opaque supervision signal or private diagnostic leaked")
			}
		})
	}
}
