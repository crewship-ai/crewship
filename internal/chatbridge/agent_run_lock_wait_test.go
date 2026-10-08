package chatbridge

import (
	"context"
	"testing"
	"time"
)

func TestAgentRunLockAcquireWaitsForRelease(t *testing.T) {
	l := NewAgentRunLock()
	if !l.TryStart("agent") {
		t.Fatal("initial claim failed")
	}
	go func() {
		time.Sleep(30 * time.Millisecond)
		l.End("agent")
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if !l.Acquire(ctx, "agent") {
		t.Fatal("Acquire gave up although the holder released the agent")
	}
	if l.TryStart("agent") {
		t.Fatal("Acquire returned without claiming the agent")
	}
	l.End("agent")
	if l.InFlight("agent") {
		t.Fatal("released claim left the agent busy")
	}
}

func TestAgentRunLockAcquireGivesUpWithoutClaiming(t *testing.T) {
	l := NewAgentRunLock()
	if !l.TryStart("agent") {
		t.Fatal("initial claim failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if l.Acquire(ctx, "agent") {
		t.Fatal("Acquire claimed an agent that was never released")
	}
	// The holder's single release must free the agent: a waiter that gave up
	// left no claim behind.
	l.End("agent")
	if l.InFlight("agent") {
		t.Fatal("a waiter that gave up left a claim behind")
	}
}

// Several waiters, one release at a time: each release admits exactly one.
func TestAgentRunLockAcquireAdmitsOneWaiterPerRelease(t *testing.T) {
	l := NewAgentRunLock()
	l.TryStart("agent")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got := make(chan struct{}, 3)
	for i := 0; i < 3; i++ {
		go func() {
			if l.Acquire(ctx, "agent") {
				got <- struct{}{}
			}
		}()
	}
	for i := 0; i < 3; i++ {
		time.Sleep(20 * time.Millisecond)
		select {
		case <-got:
			t.Fatalf("release %d: a waiter ran while the agent was still held", i)
		default:
		}
		l.End("agent")
		select {
		case <-got:
		case <-time.After(time.Second):
			t.Fatalf("release %d admitted no waiter", i)
		}
	}
	l.End("agent")
	if l.InFlight("agent") {
		t.Fatal("agent still busy after every waiter finished")
	}
}
