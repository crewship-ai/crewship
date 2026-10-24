package orchestrator

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

func TestReviewPrivateDetachedProbePanicCannotEscapeAfterRunReturns(t *testing.T) {
	const marker = "synthetic-private-detached-probe-panic"
	c := covNewRunContainer(covRunOpts{})
	c.route = func(provider.ExecConfig) (*provider.ExecResult, error) { panic(marker) }
	o := New(c, newMemState(), covQuietLogger())
	req := covRunReq().WithStrictContentPrivacy()
	h := &detachedHold{runID: req.RunID, containerID: req.ContainerID, agentSlug: req.AgentSlug, req: req, done: make(chan struct{}), alertAfter: time.Hour}
	var caught any
	func() { defer func() { caught = recover() }(); o.watchDetachedHold(h, time.Millisecond) }()
	if strings.Contains(fmt.Sprint(caught), marker) {
		t.Fatal("private detached provider panic escaped after RunAgent's privacy defer ended")
	}
}
