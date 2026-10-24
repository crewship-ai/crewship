package orchestrator

import (
	"testing"
	"time"
)

func TestReviewPrivateDetachedProbePanicCannotEscapeAfterRunReturns(t *testing.T) {
	const marker = "synthetic-private-detached-probe-panic"
	c := covNewRunContainer(covRunOpts{})
	c.inspect = func(string) (bool, int, error) { panic(marker) }
	o := New(c, newMemState(), covQuietLogger())
	req := covRunReq().WithStrictContentPrivacy()
	h := &detachedHold{runID: req.RunID, containerID: req.ContainerID, agentSlug: req.AgentSlug, execID: "private-exec", req: req, done: make(chan struct{}), alertAfter: time.Hour}
	var caught any
	func() { defer func() { caught = recover() }(); o.watchDetachedHold(h, time.Millisecond) }()
	if caught != nil {
		t.Fatal("private detached provider panic escaped after RunAgent's privacy defer ended")
	}
}
