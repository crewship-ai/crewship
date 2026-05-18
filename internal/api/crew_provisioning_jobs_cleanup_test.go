package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

// ---------------------------------------------------------------------------
// crew_provisioning_jobs.go — cleanupOldJobs, startJobCleanupRoutine,
// markJobFailed. The production trigger path needs a real Docker daemon
// (hence the existing tests stop at ProvisionStatus); these cover the
// in-memory job-map lifecycle that runs alongside provisioning.
// ---------------------------------------------------------------------------

// newJobsTestHandler builds a ProvisioningHandler with the journal set to
// noop (default) and a real ws.Hub so markJobFailed's broadcast can fire.
func newJobsTestHandler(t *testing.T) *ProvisioningHandler {
	t.Helper()
	logger := newTestLogger()
	hub := ws.NewHub(logger, nil, ws.NopValidatorForTests, ws.NopSessionsForTests)
	h := NewProvisioningHandler(setupTestDB(t), logger, nil, nil, nil, "", hub)
	t.Cleanup(h.Stop)
	return h
}

func TestCleanupOldJobs_DropsExpiredTerminalJobsKeepsFreshAndInflight(t *testing.T) {
	h := newJobsTestHandler(t)

	now := time.Now()
	staleCompletion := now.Add(-2 * time.Hour) // > 1h TTL
	freshCompletion := now.Add(-30 * time.Minute)

	// Stale completed → drop.
	h.jobs["stale-done"] = &ProvisionJob{
		CrewID: "stale-done", Status: "completed", CompletedAt: &staleCompletion,
	}
	// Stale failed → drop.
	h.jobs["stale-failed"] = &ProvisionJob{
		CrewID: "stale-failed", Status: "failed", CompletedAt: &staleCompletion,
	}
	// Fresh completed → keep.
	h.jobs["fresh-done"] = &ProvisionJob{
		CrewID: "fresh-done", Status: "completed", CompletedAt: &freshCompletion,
	}
	// Fresh failed → keep.
	h.jobs["fresh-failed"] = &ProvisionJob{
		CrewID: "fresh-failed", Status: "failed", CompletedAt: &freshCompletion,
	}
	// In-flight job old enough but no CompletedAt → keep (never expires
	// while still running; otherwise a long build would vanish from the
	// progress UI mid-flight).
	h.jobs["inflight"] = &ProvisionJob{
		CrewID: "inflight", Status: "running", StartedAt: now.Add(-3 * time.Hour),
	}
	// Pending old → keep.
	h.jobs["pending"] = &ProvisionJob{
		CrewID: "pending", Status: "pending", StartedAt: now.Add(-3 * time.Hour),
	}
	// Terminal but CompletedAt nil → keep (defensive: if completion
	// hasn't been recorded yet, cleanup must not race the writer).
	h.jobs["terminal-no-ts"] = &ProvisionJob{
		CrewID: "terminal-no-ts", Status: "completed", CompletedAt: nil,
	}

	h.cleanupOldJobs()

	for _, want := range []string{"fresh-done", "fresh-failed", "inflight", "pending", "terminal-no-ts"} {
		if _, ok := h.jobs[want]; !ok {
			t.Errorf("cleanupOldJobs dropped %q (must keep)", want)
		}
	}
	for _, gone := range []string{"stale-done", "stale-failed"} {
		if _, ok := h.jobs[gone]; ok {
			t.Errorf("cleanupOldJobs kept %q (must drop, > 1h TTL)", gone)
		}
	}
}

func TestCleanupOldJobs_NoJobs_NoOp(t *testing.T) {
	h := newJobsTestHandler(t)
	// Empty map — must not panic.
	h.cleanupOldJobs()
	if len(h.jobs) != 0 {
		t.Errorf("jobs map non-empty after no-op cleanup: %v", h.jobs)
	}
}

func TestCleanupOldJobs_HoldsLockSerially(t *testing.T) {
	// Light concurrency check — many readers + a concurrent cleanup must
	// not race or panic. Detector picks up unsynchronised access if the
	// implementation drops the mutex.
	h := newJobsTestHandler(t)
	old := time.Now().Add(-3 * time.Hour)
	for i := 0; i < 20; i++ {
		id := "j-" + time.Duration(i).String()
		h.jobs[id] = &ProvisionJob{CrewID: id, Status: "completed", CompletedAt: &old}
	}
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			h.mu.RLock()
			_ = len(h.jobs)
			h.mu.RUnlock()
		}
		close(done)
	}()
	for i := 0; i < 10; i++ {
		h.cleanupOldJobs()
	}
	<-done
}

func TestStartJobCleanupRoutine_StopsOnContextCancel(t *testing.T) {
	h := newJobsTestHandler(t)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		h.startJobCleanupRoutine(ctx)
		close(done)
	}()

	// The ticker fires every 10 minutes — there's no test surface to wait
	// for a tick. What we CAN verify is that the routine exits promptly
	// when its context is cancelled; that's the shutdown contract Stop()
	// relies on to keep test suites from leaking goroutines.
	cancel()
	select {
	case <-done:
		// graceful
	case <-time.After(2 * time.Second):
		t.Fatal("startJobCleanupRoutine did not exit within 2s of ctx cancel")
	}
}

func TestMarkJobFailed_RecordsStatusAndCompletedAt(t *testing.T) {
	h := newJobsTestHandler(t)

	job := &ProvisionJob{
		CrewID:    "crew-fail",
		Status:    "running",
		StartedAt: time.Now().Add(-10 * time.Second),
	}
	h.jobs[job.CrewID] = job

	failErr := errors.New("docker daemon hung up")
	before := time.Now()
	h.markJobFailed(job, "ws-x", failErr)
	after := time.Now()

	if job.Status != "failed" {
		t.Errorf("status = %q, want failed", job.Status)
	}
	if job.Error != failErr.Error() {
		t.Errorf("error = %q, want %q", job.Error, failErr.Error())
	}
	if job.CompletedAt == nil {
		t.Fatal("CompletedAt = nil, want set")
	}
	if job.CompletedAt.Before(before) || job.CompletedAt.After(after) {
		t.Errorf("CompletedAt = %v, want in [%v, %v]", *job.CompletedAt, before, after)
	}
}

func TestMarkJobFailed_BroadcastsAndDoesNotPanic(t *testing.T) {
	// Same as the above but with a fresh handler; this test exists to
	// document that the broadcast path (h.wsHub.BroadcastWorkspace) is
	// safe to call with a real hub, even with no subscribers. A nil hub
	// would panic — that nil-safety gap is intentionally left untested
	// here because the production wiring always provides a hub.
	h := newJobsTestHandler(t)
	job := &ProvisionJob{CrewID: "crew-bcast", Status: "running"}
	h.jobs[job.CrewID] = job
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("markJobFailed panicked: %v", r)
		}
	}()
	h.markJobFailed(job, "ws-y", errors.New("boom"))
	if job.Status != "failed" {
		t.Errorf("status = %q, want failed", job.Status)
	}
}

func TestMarkJobFailed_OverwritesPriorError(t *testing.T) {
	// markJobFailed is invoked once per provision attempt — but if a
	// future retry path calls it on a job that already failed, the
	// latest error must win (not be appended/ignored).
	h := newJobsTestHandler(t)
	completed := time.Now().Add(-time.Minute)
	job := &ProvisionJob{
		CrewID:      "crew-2nd-fail",
		Status:      "failed",
		Error:       "first failure",
		CompletedAt: &completed,
	}
	h.jobs[job.CrewID] = job
	h.markJobFailed(job, "ws-z", errors.New("second failure"))
	if job.Error != "second failure" {
		t.Errorf("error = %q, want \"second failure\"", job.Error)
	}
	if job.CompletedAt.Equal(completed) {
		t.Error("CompletedAt was not refreshed on second failure")
	}
}
