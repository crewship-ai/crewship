package orchestrator

import (
	"testing"
	"time"
)

func TestReviewPrivateDetachedPanicCannotRepanicThroughNilDiagnosticLogger(t *testing.T) {
	c := covNewRunContainer(covRunOpts{})
	c.inspect = func(string) (bool, int, error) {
		panic("synthetic-private-detached-nil-logger")
	}
	o := New(c, newMemState(), nil)
	req := covRunReq().WithStrictContentPrivacy()
	h := &detachedHold{runID: req.RunID, containerID: req.ContainerID, agentSlug: req.AgentSlug, execID: "private-exec", req: req, done: make(chan struct{}), alertAfter: time.Hour}
	var escaped any
	func() { defer func() { escaped = recover() }(); o.watchDetachedHold(h, time.Millisecond) }()
	if escaped != nil {
		t.Fatal("private panic recovery raised a second panic while publishing its opaque signal")
	}
}
