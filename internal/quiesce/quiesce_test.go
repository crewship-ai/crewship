package quiesce

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestBeginDrainsThenHolds(t *testing.T) {
	cases := []struct {
		name     string
		busyFor  int // polls that still report busy
		busyWait time.Duration
		acquire  func(calls *int32) func() (func(), error)
		wantErr  error
		wantHeld bool
	}{
		{name: "idle holds at once", busyFor: 0, busyWait: time.Second, wantHeld: true},
		{name: "busy then drained holds", busyFor: 2, busyWait: time.Second, wantHeld: true},
		{name: "still busy after the wait is skipped", busyFor: 1 << 30, busyWait: 30 * time.Millisecond, wantErr: ErrBusy},
		{
			name: "a guard that refuses once is waited for", busyFor: 0, busyWait: time.Second, wantHeld: true,
			acquire: func(calls *int32) func() (func(), error) {
				return func() (func(), error) {
					if atomic.AddInt32(calls, 1) == 1 {
						return nil, errors.New("a run slipped in")
					}
					return func() {}, nil
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := New()
			var polls, calls int32
			opts := Options{
				BusyWait: tc.busyWait, Poll: 5 * time.Millisecond, HoldCap: time.Minute,
				Busy: func(context.Context) (int, string, error) {
					if int(atomic.AddInt32(&polls, 1)) <= tc.busyFor {
						return 1, "1 agent run", nil
					}
					return 0, "", nil
				},
			}
			if tc.acquire != nil {
				opts.Acquire = tc.acquire(&calls)
			}
			w, err := c.Begin(context.Background(), opts)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				if c.Holding() {
					t.Fatal("a skipped window is still holding")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.Holding() != tc.wantHeld {
				t.Fatalf("holding = %v", c.Holding())
			}
			if _, err := c.Begin(context.Background(), opts); !errors.Is(err, ErrAlreadyHeld) {
				t.Fatalf("second Begin = %v, want ErrAlreadyHeld", err)
			}
			w.Release()
			w.Release() // idempotent
			if c.Holding() {
				t.Fatal("still holding after Release")
			}
		})
	}
}

func TestHoldCapReleasesAndEndsTheCopy(t *testing.T) {
	c := New()
	var released int32
	w, err := c.Begin(context.Background(), Options{HoldCap: 20 * time.Millisecond,
		Acquire: func() (func(), error) { return func() { atomic.AddInt32(&released, 1) }, nil }})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.Context().Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the hold cap never ended the window's context")
	}
	// Cancellation stops the copy before release hooks finish and admission
	// reopens. Observe release completion before asserting those later effects.
	releasedCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.WaitReleased(releasedCtx); err != nil {
		t.Fatal(err)
	}
	if !w.Expired() || c.Holding() {
		t.Fatalf("expired=%v holding=%v", w.Expired(), c.Holding())
	}
	if atomic.LoadInt32(&released) != 1 {
		t.Fatal("the cap did not run the caller's release")
	}
}

func TestWaitReleasedBlocksSweepsUntilRelease(t *testing.T) {
	c := New()
	if err := c.WaitReleased(context.Background()); err != nil {
		t.Fatal("no window open must not block:", err)
	}
	w, err := c.Begin(context.Background(), Options{HoldCap: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = c.WaitReleased(context.Background()); close(done) }()
	select {
	case <-done:
		t.Fatal("a sweep ran during the window")
	case <-time.After(20 * time.Millisecond):
	}
	if held := w.Release(); held <= 0 {
		t.Fatalf("hold duration = %v", held)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("sweep never resumed")
	}
}

type fixedGate bool

func (g fixedGate) IsLeader() bool { return bool(g) }

func TestGatesReadHolds(t *testing.T) {
	h := DefaultHolds()
	t.Cleanup(func() { h.Replace(nil) })
	cases := []struct {
		name      string
		holds     []string
		inner     Gate
		schedOpen bool
		queueOpen bool
		webhooks  bool
	}{
		{name: "nothing held", inner: nil, schedOpen: true, queueOpen: true},
		{name: "follower replica never fires", inner: fixedGate(false), schedOpen: false, queueOpen: false},
		{name: "routines held", holds: []string{HoldRoutines}, schedOpen: false, queueOpen: true},
		{name: "queue held", holds: []string{HoldQueue}, schedOpen: true, queueOpen: false},
		{name: "webhooks held", holds: []string{HoldWebhooks}, schedOpen: true, queueOpen: true, webhooks: true},
		{name: "all held", holds: []string{HoldAll}, schedOpen: false, queueOpen: false, webhooks: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hs []Hold
			for _, k := range tc.holds {
				hs = append(hs, Hold{Key: k})
			}
			h.Replace(hs)
			if got := SchedulerGate(tc.inner).IsLeader(); got != tc.schedOpen {
				t.Errorf("scheduler gate = %v, want %v", got, tc.schedOpen)
			}
			if got := QueueGate(tc.inner).IsLeader(); got != tc.queueOpen {
				t.Errorf("queue gate = %v, want %v", got, tc.queueOpen)
			}
			if got := WebhooksPaused(); got != tc.webhooks {
				t.Errorf("webhooks paused = %v, want %v", got, tc.webhooks)
			}
		})
	}
}
