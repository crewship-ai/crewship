package main

import (
	"context"
	"errors"
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
