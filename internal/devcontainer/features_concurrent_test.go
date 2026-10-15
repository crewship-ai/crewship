package devcontainer

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/registry"
)

func TestDownloadConcurrentCacheMissPreservesResolvedFiles(t *testing.T) {
	var enabled atomic.Bool
	var manifests atomic.Int32
	firstStarted, secondStarted := make(chan struct{}), make(chan struct{})
	firstRelease, secondRelease := make(chan struct{}), make(chan struct{})
	registryHandler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if enabled.Load() && r.Method == "GET" && strings.Contains(r.URL.Path, "/manifests/") {
			switch manifests.Add(1) {
			case 1:
				close(firstStarted)
				select {
				case <-firstRelease:
				case <-r.Context().Done():
					return
				}
			case 2:
				close(secondStarted)
				select {
				case <-secondRelease:
				case <-r.Context().Done():
					return
				}
			}
		}
		registryHandler.ServeHTTP(w, r)
	}))
	defer server.Close()
	ref, _ := covPushFeature(t, strings.TrimPrefix(server.URL, "http://"), "features/concurrent:1", map[string]string{"install.sh": "echo install", "devcontainer-feature.json": `{"id":"concurrent","version":"1"}`, "main.sh": "echo main", "bin/code": "echo code"})
	cache := t.TempDir()
	downloaders := []*FeatureDownloader{NewFeatureDownloader(cache, testLogger()), NewFeatureDownloader(cache, testLogger())}
	type result struct {
		feature *ResolvedFeature
		err     error
	}
	first, second := make(chan result, 1), make(chan result, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	enabled.Store(true)
	go func() { f, e := downloaders[0].Download(ctx, ref, nil); first <- result{f, e} }()
	select {
	case <-firstStarted:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	go func() { f, e := downloaders[1].Download(ctx, ref, nil); second <- result{f, e} }()
	// The original implementation reaches the second pull while the first is
	// blocked. A coordinated implementation waits locally instead.
	select {
	case <-secondStarted:
	case <-time.After(150 * time.Millisecond):
	}
	close(firstRelease)
	a := <-first
	if a.err != nil {
		t.Fatal(a.err)
	}
	before, err := os.Stat(filepath.Join(a.feature.Dir, "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	close(secondRelease)
	b := <-second
	if b.err != nil {
		t.Fatal(b.err)
	}
	after, err := os.Stat(filepath.Join(a.feature.Dir, "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Error("concurrent cache miss replaced files already returned to another build")
	}
	if manifests.Load() != 1 {
		t.Errorf("same cache key pulled %d times, want 1", manifests.Load())
	}
	if a.feature.Dir != b.feature.Dir || a.feature.Digest != b.feature.Digest {
		t.Fatal("concurrent callers resolved different cache generations")
	}
	contextDir, _, _, err := stageBuildContext("ubuntu:22.04", []*ResolvedFeature{a.feature}, nil, nil)
	if err != nil {
		t.Fatalf("stage retained feature: %v", err)
	}
	defer os.RemoveAll(contextDir)
	if _, err := os.Stat(filepath.Join(contextDir, "features", "concurrent", "bin", "code")); err != nil {
		t.Fatal(err)
	}
}

func TestDownloadWaitingCallerCancellationAndUnrelatedKey(t *testing.T) {
	var enabled atomic.Bool
	var blockedRequests atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if enabled.Load() && r.Method == "GET" && strings.Contains(r.URL.Path, "/features/blocked/manifests/") {
			if blockedRequests.Add(1) == 1 {
				close(started)
			}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	// Unblock before closing the fixture even if an assertion fails.
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	host := strings.TrimPrefix(server.URL, "http://")
	files := map[string]string{"install.sh": "echo install", "devcontainer-feature.json": `{"id":"fixture","version":"1"}`}
	blocked, _ := covPushFeature(t, host, "features/blocked:1", files)
	unrelated, _ := covPushFeature(t, host, "features/unrelated:1", files)
	cache := t.TempDir()
	first := NewFeatureDownloader(cache, testLogger())
	other := NewFeatureDownloader(cache, testLogger())
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	enabled.Store(true)
	done := make(chan error, 1)
	go func() { _, err := first.Download(ctx, blocked, nil); done <- err }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	waitingCtx, cancelWaiting := context.WithCancel(ctx)
	waiting := make(chan error, 1)
	go func() { _, err := other.Download(waitingCtx, blocked, nil); waiting <- err }()
	// Observe the waiter inside the cache coordinator before cancelling, so
	// this exercises cancellation while queued rather than before entry.
	key, err := filepath.Abs(other.cachePathFor(blocked))
	if err != nil {
		t.Fatal(err)
	}
	for {
		featureCacheLocks.Lock()
		entry := featureCacheLocks.entries[key]
		queued := entry != nil && entry.users == 2
		featureCacheLocks.Unlock()
		if queued {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("waiter never queued")
		case <-time.After(time.Millisecond):
		}
	}
	cancelWaiting()
	select {
	case err := <-waiting:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled waiter: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("waiter ignored cancellation")
	}
	// A different feature must not queue behind the blocked feature's pull.
	if _, err := other.Download(ctx, unrelated, nil); err != nil {
		t.Fatalf("unrelated feature blocked: %v", err)
	}
	if blockedRequests.Load() != 1 {
		t.Fatalf("cancelled waiter started another pull: %d", blockedRequests.Load())
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
