package orchestrator

import (
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

func TestPrivateDetachedPanicNilLoggerKeepsUncertainty(t *testing.T) {
	testDetachedPanicNilLogger(t, true)
}

func TestOrdinaryDetachedPanicNilLoggerPreservesPropagation(t *testing.T) {
	testDetachedPanicNilLogger(t, false)
}

func testDetachedPanicNilLogger(t *testing.T, private bool) {
	t.Helper()
	const marker = "synthetic-private-nil-logger-probe"
	c := covNewRunContainer(covRunOpts{})
	c.route = func(provider.ExecConfig) (*provider.ExecResult, error) { panic(marker) }
	o := New(c, newMemState(), nil)
	req := covRunReq()
	if private {
		req = req.WithStrictContentPrivacy()
	}
	o.runSem = make(chan struct{}, 1)
	release, err := o.acquireRunSlot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	released, finalized := false, false
	h := &detachedHold{runID: req.RunID, containerID: req.ContainerID, agentSlug: req.AgentSlug, req: req, done: make(chan struct{}), alertAfter: time.Hour, release: func() { released = true; release() }, finalize: func() { finalized = true }}
	o.detached[h.runID] = h
	var escaped any
	func() { defer func() { escaped = recover() }(); o.watchDetachedHold(h, time.Millisecond) }()
	if private && escaped != nil || !private && escaped != marker {
		t.Fatal("unexpected detached panic boundary with logging disabled")
	}
	if released || finalized || len(o.runSem) != 1 {
		t.Fatal("failed supervision released uncertain runtime ownership")
	}
	if id, held := o.detachedHoldRunID(req.AgentID); !held || id != req.RunID {
		t.Fatal("failed supervision removed admission fence")
	}
	select {
	case <-h.done:
		t.Fatal("failed supervision reported terminal completion")
	default:
	}
}
