package llmproxy

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTokenSyncerRetainsWorkingPoolAfterPeriodicFailure(t *testing.T) {
	pool := NewTokenPool(testLogger())
	syncer := NewTokenSyncer(pool, "http://fixture.invalid", "fixture", time.Millisecond, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	syncer.client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/v1/internal/credentials" || req.URL.Query().Get("include_values") != "true" || req.Header.Get("X-Internal-Token") != "fixture" {
			t.Errorf("unexpected credential request: %s", req.URL)
		}
		status, body := 200, `[{"id":"working","workspace_id":"ws","provider":"OPENAI","status":"ACTIVE"}]`
		if calls.Add(1) > 1 {
			status, body = 503, "temporarily unavailable"
			cancel()
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	done := make(chan struct{})
	go func() { defer close(done); syncer.Run(ctx) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("syncer did not stop after cancellation")
	}
	connections := pool.AllConnections()
	if calls.Load() < 2 || len(connections) != 1 || connections[0].ID != "working" {
		t.Fatalf("failed refresh erased the working pool: calls=%d, pool=%+v", calls.Load(), connections)
	}
}

func TestCredentialMonitorInvokesRefreshHookOnTick(t *testing.T) {
	pool := NewTokenPool(testLogger())
	monitor := NewCredentialMonitor(pool, "http://fixture.invalid", "fixture", time.Millisecond, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	monitor.SetRefreshHook(func(context.Context) { calls.Add(1); cancel() })
	done := make(chan struct{})
	go func() { defer close(done); monitor.Run(ctx) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("monitor did not stop after its refresh hook cancelled the run")
	}
	if calls.Load() == 0 {
		t.Fatal("monitor omitted credential refresh")
	}
	monitor.SetRefreshHook(nil)
	monitor.SetOnChange(func(string, ConnectionStatus, ConnectionStatus) { t.Error("removed callback invoked") })
	monitor.SetOnChange(nil)
	if monitor.refreshHook.Load() != nil || monitor.onChange.Load() != nil {
		t.Fatal("removed callbacks remained registered")
	}
}

func TestTokenSyncMalformedResponsesPreservePool(t *testing.T) {
	pool := NewTokenPool(testLogger())
	pool.Update([]ProviderConnection{{ID: "working", Status: StatusActive}})
	syncer := NewTokenSyncer(pool, "http://fixture.invalid", "fixture", time.Second, testLogger())
	syncer.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{")), Header: make(http.Header)}, nil
	})
	if err := syncer.SyncNow(context.Background()); err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Fatalf("malformed response = %v", err)
	}
	syncer.nextjsURL = ":bad-url"
	if err := syncer.SyncNow(context.Background()); err == nil || !strings.Contains(err.Error(), "create request") {
		t.Fatalf("invalid URL = %v", err)
	}
	if got := pool.AllConnections(); len(got) != 1 || got[0].ID != "working" {
		t.Fatalf("failed sync replaced pool: %+v", got)
	}
}
