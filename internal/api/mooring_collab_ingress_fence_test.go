package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func collaborationIngressTestContext(t *testing.T, f *collaborationIngressFence, ctx context.Context) context.Context {
	t.Helper()
	var stamped context.Context
	recorder := httptest.NewRecorder()
	f.Wrap(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { stamped = r.Context() })).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/", nil).WithContext(ctx))
	if recorder.Code != http.StatusOK || stamped == nil {
		t.Fatalf("stamp failed: %d", recorder.Code)
	}
	return stamped
}

func TestCollaborationIngressOriginalRequestDoesNotSurviveRestore(t *testing.T) {
	f, err := newCollaborationIngressFence()
	if err != nil {
		t.Fatal(err)
	}
	ctx := collaborationIngressTestContext(t, f, t.Context())
	original, err := f.capture(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.capture(t.Context()); err == nil {
		t.Fatal("unstamped ingress accepted")
	}
	other, err := newCollaborationIngressFence()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.acquire(ctx, original); err == nil {
		t.Fatal("foreign fence accepted")
	}
	if err := f.ResumeAfterRestore(); err == nil {
		t.Fatal("resume without suspend accepted")
	}
	f.SuspendBeforeRestore()
	recorder := httptest.NewRecorder()
	f.Wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("suspended handler ran") })).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatal(recorder.Code)
	}
	if _, err := f.acquireOriginal(original); err == nil {
		t.Fatal("suspended task accepted")
	}
	if err := f.ResumeAfterRestore(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.capture(ctx); err == nil {
		t.Fatal("pre-restore authenticated request recaptured")
	}
	fresh := collaborationIngressTestContext(t, f, t.Context())
	if _, err := f.acquire(fresh, original); err == nil {
		t.Fatal("old capability adopted fresh ingress")
	}
	if _, err := f.acquireOriginal(original); err == nil {
		t.Fatal("old dispatcher capability revived")
	}
	current, err := f.capture(fresh)
	if err != nil {
		t.Fatal(err)
	}
	release, err := f.acquire(fresh, current)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestCollaborationIngressRestoreDrainsBoundedOperations(t *testing.T) {
	f, err := newCollaborationIngressFence()
	if err != nil {
		t.Fatal(err)
	}
	ctx := collaborationIngressTestContext(t, f, t.Context())
	original, err := f.capture(ctx)
	if err != nil {
		t.Fatal(err)
	}
	release, err := f.acquire(ctx, original)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	completed := make(chan struct{})
	go func() { close(entered); f.SuspendBeforeRestore(); close(completed) }()
	<-entered
	select {
	case <-completed:
		t.Fatal("restore crossed an active operation")
	default:
	}
	release()
	<-completed
	if _, err := f.acquireOriginal(original); err == nil {
		t.Fatal("old lease revived after drain")
	}
	if err := f.ResumeAfterRestore(); err != nil {
		t.Fatal(err)
	}
	// Race fresh operations against a reset. A lease either belongs wholly to
	// the old incarnation or is denied; none can silently adopt the new one.
	fresh := collaborationIngressTestContext(t, f, t.Context())
	stamp, err := f.capture(fresh)
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for i := 0; i < 32; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if done, err := f.acquire(fresh, stamp); err == nil {
				done()
			}
		}()
	}
	f.SuspendBeforeRestore()
	group.Wait()
	if _, err := f.acquireOriginal(stamp); err == nil {
		t.Fatal("concurrent reset retained old stamp")
	}
}
