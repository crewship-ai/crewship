package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestServeExitsWithErrorAfterEngineEscape(t *testing.T) {
	escaped := make(chan struct{})
	stopped := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- serve(t.Context(), escaped, func(context.Context) error { close(stopped); return nil })
	}()
	close(escaped)
	select {
	case err := <-result:
		if !errors.Is(err, errEngineEscaped) {
			t.Fatalf("escape must end the worker with an error, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker kept serving after an engine escape")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("listener was not stopped before exit")
	}
}

func TestServeStopsCleanlyOnShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := serve(ctx, make(chan struct{}), func(context.Context) error { return nil }); err != nil {
		t.Fatalf("graceful shutdown returned %v", err)
	}
}

func TestWorkerCapacityDefaultsAndBounds(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  int
		ok    bool
	}{
		{"", defaultWorkerCapacity, true},
		{"1", 1, true},
		{"4", 4, true},
		{fmt.Sprint(maxWorkerCapacity), maxWorkerCapacity, true},
		{fmt.Sprint(maxWorkerCapacity + 1), 0, false},
		{"0", 0, false},
		{"-1", 0, false},
		{" 2", 0, false},
		{"2x", 0, false},
	} {
		got, err := workerCapacity(func(name string) string {
			if name != capacityEnv {
				t.Fatalf("unexpected variable %s", name)
			}
			return tc.value
		})
		if (err == nil) != tc.ok || got != tc.want {
			t.Fatalf("%q: got %d, %v", tc.value, got, err)
		}
	}
	if defaultWorkerCapacity < 1 || defaultWorkerCapacity > maxWorkerCapacity {
		t.Fatalf("default capacity %d outside 1..%d", defaultWorkerCapacity, maxWorkerCapacity)
	}
}
