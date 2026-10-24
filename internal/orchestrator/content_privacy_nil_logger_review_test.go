package orchestrator

import (
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

func TestReviewPrivateDetachedPanicCannotRepanicThroughNilDiagnosticLogger(t *testing.T) {
	c := covNewRunContainer(covRunOpts{})
	c.route = func(provider.ExecConfig) (*provider.ExecResult, error) {
		panic("synthetic-private-detached-nil-logger")
	}
	o := New(c, newMemState(), nil)
	req := covRunReq().WithStrictContentPrivacy()
	h := &detachedHold{runID: req.RunID, containerID: req.ContainerID, agentSlug: req.AgentSlug, req: req, done: make(chan struct{}), alertAfter: time.Hour}
	var escaped any
	func() { defer func() { escaped = recover() }(); o.watchDetachedHold(h, time.Millisecond) }()
	if escaped != nil {
		t.Fatal("private panic recovery raised a second panic while publishing its opaque signal")
	}
}
