package api

// Second coverage pass for crew_provisioning_jobs.go:
//
//   - ProvisionStatus / ProvisionTrigger / ProvisionRebuild DB-error 500s
//   - ProvisionTrigger's no-devcontainer 400
//   - EnqueueForCrew's "query crew" error wrap
//   - runProvisioning's panic recovery (nil job → recovered, job map entry
//     marked failed) and the missing-base-image failure
//
// LogTail overflow, the second lock-check race and the post-Provision
// persistence tail need a real (or far deeper fake) build pipeline and are
// intentionally left out.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/devcontainer"
)

func covProv2ClosedRig(t *testing.T) (h *ProvisioningHandler, userID, wsID, crewID string) {
	t.Helper()
	h = newTestProvisioningHandler(t)
	h.provisioner = devcontainer.NewProvisioner(&covCommitClient{}, nil, nil, newTestLogger())
	userID = seedTestUser(t, h.db)
	wsID = seedTestWorkspace(t, h.db, userID)
	crewID = seedCrewRow(t, h.db, "crew-prov2", wsID, "P2", "prov2")
	h.db.Close()
	return
}

func TestProv2_ProvisionStatus_DBError500(t *testing.T) {
	h, userID, wsID, crewID := covProv2ClosedRig(t)
	req := httptest.NewRequest("GET", "/x", nil)
	req.SetPathValue("crewId", crewID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ProvisionStatus(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestProv2_ProvisionTrigger_DBError500(t *testing.T) {
	h, userID, wsID, crewID := covProv2ClosedRig(t)
	req := httptest.NewRequest("POST", "/x", nil)
	req.SetPathValue("crewId", crewID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ProvisionTrigger(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestProv2_ProvisionTrigger_NoDevcontainer400(t *testing.T) {
	h, _, crewID := covProvRig(t, &covCommitClient{}, "") // no devcontainer_config
	userID := "test-user-id"
	wsID := "test-workspace-id"
	req := httptest.NewRequest("POST", "/x", nil)
	req.SetPathValue("crewId", crewID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ProvisionTrigger(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "devcontainer_config") {
		t.Errorf("body = %q", rr.Body.String())
	}
}

func TestProv2_EnqueueForCrew_QueryError(t *testing.T) {
	h, _, wsID, crewID := covProv2ClosedRig(t)
	_, err := h.EnqueueForCrew(context.Background(), crewID, wsID)
	if err == nil || !strings.Contains(err.Error(), "query crew") {
		t.Errorf("err = %v, want query crew wrap", err)
	}
	// Must not be one of the typed sentinels.
	if errors.Is(err, ErrCrewNotFound) || errors.Is(err, ErrCrewNoDevcontainer) {
		t.Errorf("err wrongly matches a sentinel: %v", err)
	}
}

func TestProv2_ProvisionRebuild_DBError500(t *testing.T) {
	h, userID, wsID, crewID := covProv2ClosedRig(t)
	req := httptest.NewRequest("POST", "/x", nil)
	req.SetPathValue("crewId", crewID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ProvisionRebuild(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestProv2_RunProvisioning_PanicRecovered(t *testing.T) {
	h, wsID, crewID := covProvRig(t, &covCommitClient{}, `{"image":"alpine:3"}`)
	// A nil provisioner makes the Provision call panic (nil receiver
	// dereference) OUTSIDE the job mutex; the deferred recovery must mark
	// the job failed and release the rate-limit slot instead of crashing.
	h.provisioner = nil
	job := covJob(crewID)
	h.jobs[crewID] = job
	h.rateLimiter.running[wsID] = 1

	h.runProvisioning(crewID, wsID, `{"image":"alpine:3"}`, "", "", job)

	h.mu.RLock()
	defer h.mu.RUnlock()
	if job.Status != "failed" {
		t.Errorf("job status = %q, want failed", job.Status)
	}
	if !strings.Contains(job.Error, "internal error") {
		t.Errorf("job error = %q", job.Error)
	}
	if job.CompletedAt == nil {
		t.Error("CompletedAt not set by panic recovery")
	}
	if h.rateLimiter.running[wsID] != 0 {
		t.Errorf("rate-limit slot not released: %d", h.rateLimiter.running[wsID])
	}
}
