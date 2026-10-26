package orchestrator

import (
	"errors"
	"sync/atomic"
	"testing"
)

func TestP3PrivatePostExecPanicRetainsUnconfirmedRuntimeAdmission(t *testing.T) {
	c := covNewRunContainer(covRunOpts{})
	inspect := c.inspect
	var created atomic.Bool
	c.inspect = func(id string) (bool, int, error) {
		if id == "agent-exec" {
			created.Store(true)
			panic("p3-private-unconfirmed-post-exec")
		}
		return inspect(id)
	}
	o := New(c, newMemState(), covQuietLogger())
	o.runSem = make(chan struct{}, 1)
	req := covRunReq().WithStrictContentPrivacy()
	err := o.RunAgent(t.Context(), req, nil)
	if !created.Load() || !errors.Is(err, ErrPrivateRunUnqualified) {
		t.Fatal("did not reach actual post-exec panic boundary", created.Load(), err)
	}
	// No provider stop or inspection established process death. A fixed opaque
	// error must not release this original runtime's admission/capacity.
	if len(o.runSem) != 1 {
		t.Fatal("unconfirmed process lost its runtime capacity")
	}
	if id, held := o.detachedHoldRunID(req.AgentID); !held || id != req.RunID {
		t.Fatal("unconfirmed process lost its admission fence")
	}
	release, admissionErr := o.acquireAgentAdmissionMode(t.Context(), req.AgentID, true)
	if admissionErr == nil {
		release()
		t.Fatal("a second runtime entered while original process was unconfirmed")
	}
}
