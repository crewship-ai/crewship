package main

import (
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func newWatchTestCmd() *cobra.Command {
	c := &cobra.Command{Use: "watchtest"}
	addWatchFlag(c)
	return c
}

func TestWatchWrap_PassthroughWhenFlagEmpty(t *testing.T) {
	c := newWatchTestCmd()

	var calls int
	sentinel := errors.New("inner sentinel")
	wrapped := watchWrap(func(cmd *cobra.Command, args []string) error {
		calls++
		if cmd != c {
			t.Errorf("inner received wrong cmd: %v", cmd)
		}
		if len(args) != 1 || args[0] != "arg1" {
			t.Errorf("inner args: got %v", args)
		}
		return sentinel
	})

	err := wrapped(c, []string{"arg1"})
	if !errors.Is(err, sentinel) {
		t.Errorf("got %v; want inner error passed through", err)
	}
	if calls != 1 {
		t.Errorf("inner called %d times, want 1", calls)
	}
}

func TestWatchWrap_InvalidDuration(t *testing.T) {
	c := newWatchTestCmd()
	if err := c.Flags().Set("watch", "banana"); err != nil {
		t.Fatal(err)
	}

	var calls int
	wrapped := watchWrap(func(*cobra.Command, []string) error {
		calls++
		return nil
	})
	err := wrapped(c, nil)
	if err == nil || !strings.Contains(err.Error(), "--watch") {
		t.Errorf("got %v; want --watch parse error", err)
	}
	if calls != 0 {
		t.Errorf("inner must not run on a bad duration; ran %d times", calls)
	}
}

// TestWatchWrap_LoopRendersAndExitsOnSignal drives one full loop
// iteration: the sub-second --watch value exercises the 1s clamp, the
// first render returns an error (error-render branch), the ticker fires
// once (tick branch), and the second render raises SIGINT so the
// signal-bound context cancels and the wrapper returns nil.
func TestWatchWrap_LoopRendersAndExitsOnSignal(t *testing.T) {
	c := newWatchTestCmd()
	if err := c.Flags().Set("watch", "50ms"); err != nil { // clamped to 1s
		t.Fatal(err)
	}

	var calls atomic.Int32
	wrapped := watchWrap(func(*cobra.Command, []string) error {
		n := calls.Add(1)
		if n == 1 {
			// First render: exercise the inner-error branch; the wrapper
			// must swallow it and keep watching.
			return errors.New("transient render failure")
		}
		// Second and later renders: ask the wrapper to exit. By now
		// signal.NotifyContext is registered, so SIGINT cancels the
		// watch context instead of killing the test process.
		_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
		return nil
	})

	done := make(chan error, 1)
	go func() { done <- wrapped(c, nil) }()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("got %v; want nil on Ctrl-C exit", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("watchWrap did not exit after SIGINT")
	}

	if n := calls.Load(); n < 2 {
		t.Errorf("inner rendered %d times, want >=2 (initial + first tick)", n)
	}
}
