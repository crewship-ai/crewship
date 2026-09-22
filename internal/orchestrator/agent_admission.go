package orchestrator

import (
	"context"
	"sync"
)

// agentAdmission enforces the serial profile at the one runtime entry point.
// It is a process-local safety fence while producers migrate to durable work
// admission, not a durable queue or the verified one-chat-plus-one-background
// release profile. There is intentionally no production knob to enable that
// profile before its real-provider acceptance tests pass.
type agentAdmission struct {
	slot chan struct{}
	refs int // owner plus waiters; removed after the last reference leaves
}

func (o *Orchestrator) acquireAgentAdmission(ctx context.Context, agentID string) (func(), error) {
	o.agentAdmissionMu.Lock()
	if o.agentAdmissions == nil {
		o.agentAdmissions = make(map[string]*agentAdmission)
	}
	a := o.agentAdmissions[agentID]
	if a == nil {
		a = &agentAdmission{slot: make(chan struct{}, 1)}
		o.agentAdmissions[agentID] = a
	}
	a.refs++
	o.agentAdmissionMu.Unlock()

	drop := func() {
		o.agentAdmissionMu.Lock()
		defer o.agentAdmissionMu.Unlock()
		a.refs--
		if a.refs == 0 {
			delete(o.agentAdmissions, agentID)
		}
	}
	select {
	case a.slot <- struct{}{}:
		// A ready token and cancellation can win the same select. Do not
		// admit an already cancelled request in that case.
		if err := ctx.Err(); err != nil {
			<-a.slot
			drop()
			return nil, err
		}
		var once sync.Once
		return func() {
			once.Do(func() { <-a.slot; drop() })
		}, nil
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	}
}
