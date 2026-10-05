package quiesce

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDefaultWriterLifecycleAndCancelledAdmission(t *testing.T) {
	ctx := t.Context()
	w, ok := Enter(nil) //nolint:staticcheck // Exercise the documented nil-context fallback.
	if !ok {
		t.Fatal("idle writer refused")
	}
	if err := Yield(w.Context()); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := Outside(w.Context(), func() {
		called = true
		if Default().Writers() != 0 {
			t.Error("outside callback still blocks drain")
		}
	}); err != nil {
		t.Fatal(err)
	}
	if !called || Default().Writers() != 1 {
		t.Fatal("writer did not reenter after outside work")
	}
	w.Leave()
	sentinel := errors.New("writer operation failed")
	if err := Do(ctx, func(inner context.Context) error {
		child, ok := Enter(inner)
		if !ok || Default().Writers() != 2 {
			t.Fatal("nested writer not counted")
		}
		child.Leave()
		return sentinel
	}); !errors.Is(err, sentinel) || Default().Writers() != 0 {
		t.Fatalf("failed operation leaked writer: %v", err)
	}
	window, err := Default().Begin(ctx, Options{Reason: "consistent backup", HoldCap: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer window.Release()
	if !WritesHeld() || !Closing() || Default().Reason() != "consistent backup" {
		t.Fatal("held window not visible to public predicates")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := EnterWait(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled writer admitted: %v", err)
	}
	called = false
	if err := Do(cancelled, func(context.Context) error { called = true; return nil }); !errors.Is(err, context.Canceled) || called {
		t.Fatalf("cancelled operation ran: %v", err)
	}
	if err := WaitReleased(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait: %v", err)
	}
	if err := Default().SetWriterOwner(func() error { return nil }); !errors.Is(err, ErrAlreadyHeld) {
		t.Fatalf("writer owner changed during copy: %v", err)
	}
	window.Release()
	if err := WaitReleased(ctx); err != nil || Closing() || WritesHeld() || Default().Reason() != "" || Default().RetryAfter() != 0 {
		t.Fatalf("release left public gates closed: %v", err)
	}
}

func TestDefaultRunAndWriterContextsAfterCompletion(t *testing.T) {
	r, ok := StartRun(nil) //nolint:staticcheck // Exercise the documented nil-context fallback.
	if !ok {
		t.Fatal("idle run refused")
	}
	r.Done()
	r.Done()
	r, err := StartRunWait(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	r.Done()
	w, ok := Enter(nil) //nolint:staticcheck // Exercise the documented nil-context fallback.
	if !ok {
		t.Fatal("idle writer refused")
	}
	w.Leave()
	called := false
	if err := Outside(w.Context(), func() { called = true }); err != nil || !called || Default().Writers() != 0 {
		t.Fatalf("completed writer reentered: %v", err)
	}
	if err := Yield(w.Context()); err != nil {
		t.Fatal(err)
	}
	if err := Yield(nil); err != nil { //nolint:staticcheck // A missing writer context must be a safe no-op.
		t.Fatal(err)
	}
	if err := Outside(context.Background(), func() {}); err != nil {
		t.Fatal(err)
	}
	var absentWriter *Writer
	absentWriter.Leave()
	var absentRun *Run
	absentRun.Done()
	if Default().Runs() != 0 || Default().Writers() != 0 {
		t.Fatal("completed admissions still counted")
	}
}
