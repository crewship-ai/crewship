package orchestrator

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// Different callers/sessions must not bypass the unverified adapter's serial
// limit. Exercise RunAgent rather than only the admission primitive.
func TestRunAgent_SameAgentWaitsWithoutTakingServerCapacity(t *testing.T) {
	probe := &concProbeContainer{arrived: make(chan struct{}, 4), release: make(chan struct{})}
	o := New(probe, newLockedMemState(), covQuietLogger(), WithMaxConcurrentRuns(4))
	req := covRunReq()
	req.RunID = "admission-first"
	var wg sync.WaitGroup
	var once sync.Once
	release := func() { once.Do(func() { close(probe.release) }) }
	defer func() { release(); wg.Wait() }()
	first := make(chan error, 1)
	wg.Add(1)
	go func() { defer wg.Done(); first <- o.RunAgent(context.Background(), req, nil) }()
	select {
	case <-probe.arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("first runtime never started")
	}
	secondReq := req
	secondReq.RunID = "admission-second"
	secondReq.ChatID = "unrelated-session"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	second := make(chan error, 1)
	wg.Add(1)
	go func() { defer wg.Done(); second <- o.RunAgent(ctx, secondReq, nil) }()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
waiting:
	for {
		select {
		case <-probe.arrived:
			t.Fatal("same agent started a second runtime")
		case <-deadline.C:
			t.Fatal("second caller never reached admission")
		case <-tick.C:
			o.agentAdmissionMu.Lock()
			a := o.agentAdmissions[req.AgentID]
			registered := a != nil && a.refs == 2
			o.agentAdmissionMu.Unlock()
			if registered {
				break waiting
			}
		}
	}
	if len(o.runSem) != 1 {
		t.Fatalf("waiting agent consumed server capacity: %d", len(o.runSem))
	}
	cancel()
	select {
	case err := <-second:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiting cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiting cancellation blocked")
	}
	release()
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	// Cancellation must not leave the agent blocked for the next run.
	secondReq.RunID = "admission-third"
	if err := o.RunAgent(context.Background(), secondReq, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRunAgent_SynchronousChildRefusesBusyAdmission(t *testing.T) {
	for _, sameAgent := range []bool{true, false} {
		name := "server-capacity"
		if sameAgent {
			name = "parent-agent"
		}
		t.Run(name, func(t *testing.T) {
			probe := &concProbeContainer{arrived: make(chan struct{}, 4), release: make(chan struct{})}
			o := New(probe, newLockedMemState(), covQuietLogger(), WithMaxConcurrentRuns(1))
			parent := covRunReq()
			parent.RunID = "admission-parent-" + name
			done := make(chan error, 1)
			go func() { done <- o.RunAgent(context.Background(), parent, nil) }()
			defer func() { close(probe.release); <-done }()
			select {
			case <-probe.arrived:
			case <-time.After(5 * time.Second):
				t.Fatal("parent never started")
			}
			child := parent
			child.RunID = "admission-child-" + name
			child.NoAdmissionWait = true
			if !sameAgent {
				child.AgentID = "different-child-agent"
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := o.RunAgent(ctx, child, nil); !errors.Is(err, ErrAdmissionBusy) {
				t.Fatalf("child must refuse busy capacity without waiting for parent: %v", err)
			}
			select {
			case <-probe.arrived:
				t.Fatal("busy child started a runtime")
			default:
			}
			if !sameAgent {
				// Failure to acquire server capacity must return the child's
				// agent reservation, even while the parent remains alive.
				release, err := o.acquireAgentAdmission(ctx, child.AgentID)
				if err != nil {
					t.Fatalf("child leaked agent reservation: %v", err)
				}
				release()
			}
		})
	}
}

// A synchronous query must refuse occupied capacity before approval or hooks
// can block, enqueue a request, or perform side effects.
func TestRunAgent_NoWaitBusyPrecedesApprovalAndHooks(t *testing.T) {
	for _, busy := range []string{"agent", "server"} {
		t.Run(busy, func(t *testing.T) {
			o := New(&concProbeContainer{}, newLockedMemState(), covQuietLogger(), WithMaxConcurrentRuns(1))
			req := covRunReq()
			req.NoAdmissionWait = true
			req.ApprovalMode = "sync"
			gate := &covGate{err: errors.New("approval must not be called")}
			hooks := &covHooks{failOn: "pre_agent_start"}
			o.SetApprovalGate(gate)
			o.SetHooksDispatcher(hooks)
			var release func()
			var err error
			if busy == "agent" {
				release, err = o.acquireAgentAdmission(t.Context(), req.AgentID)
			} else {
				release, err = o.acquireServerAdmission(t.Context(), true)
			}
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			if err := o.RunAgent(t.Context(), req, nil); !errors.Is(err, ErrAdmissionBusy) {
				t.Fatalf("busy query reached a blocking gate: %v", err)
			}
			if len(gate.got) != 0 {
				t.Fatal("busy query requested approval")
			}
			if len(hooks.events) != 0 {
				t.Fatalf("busy query dispatched hooks: %v", hooks.events)
			}
		})
	}
}
