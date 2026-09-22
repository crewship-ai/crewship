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
	select {
	case <-probe.arrived:
		t.Fatal("same agent started a second runtime")
	case <-time.After(100 * time.Millisecond):
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
