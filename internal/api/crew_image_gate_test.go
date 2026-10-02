package api

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/crewstart"
	"github.com/crewship-ai/crewship/internal/provider"
)

// EnsureCrewImage is the gate crewstart consults on every crew start
// (cmd_start wires it). The paths that reach it know only the crew id — a
// routine step, a scheduled run, a webhook — so it resolves the workspace
// itself, then waits for the build exactly like a dispatch would.

func TestEnsureCrewImageWaitsForTheBuildOfACrewWithoutAnImage(t *testing.T) {
	h := newTestProvisioningHandler(t)
	h.provisioner = testProvisioner()
	h.provisionPollInterval = 5 * time.Millisecond
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	crewID := seedCrewRow(t, h.db, "crew-gate-build", wsID, "G", "gate-build")

	// A build already in flight: EnqueueForCrew answers AlreadyRunning and
	// the gate waits for it instead of spawning a real one.
	h.mu.Lock()
	h.jobs[crewID] = &ProvisionJob{CrewID: crewID, Status: "running", StartedAt: time.Now()}
	h.mu.Unlock()
	done := make(chan error, 1)
	go func() { _, err := h.EnsureCrewImage(context.Background(), crewID); done <- err }()

	select {
	case err := <-done:
		t.Fatalf("gate returned %v before the build finished", err)
	case <-time.After(30 * time.Millisecond):
	}
	h.mu.Lock()
	h.jobs[crewID].Status = "completed"
	h.mu.Unlock()
	if err := <-done; err != nil {
		t.Fatalf("want nil once the build completes, got %v", err)
	}
}

func TestEnsureCrewImageReportsAFailedBuild(t *testing.T) {
	h := newTestProvisioningHandler(t)
	h.provisioner = testProvisioner()
	h.provisionPollInterval = 5 * time.Millisecond
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	crewID := seedCrewRow(t, h.db, "crew-gate-fail", wsID, "G", "gate-fail")
	// In flight when the gate looks, failed by the time it polls.
	h.mu.Lock()
	h.jobs[crewID] = &ProvisionJob{CrewID: crewID, Status: "running", StartedAt: time.Now()}
	h.mu.Unlock()
	go func() {
		time.Sleep(10 * time.Millisecond)
		h.mu.Lock()
		h.jobs[crewID].Status = "failed"
		h.jobs[crewID].Error = "npm ERR"
		h.mu.Unlock()
	}()
	_, err := h.EnsureCrewImage(context.Background(), crewID)
	if err == nil || !strings.Contains(err.Error(), "npm ERR") {
		t.Fatalf("err = %v, want the build failure", err)
	}
}

func TestEnsureCrewImageLeavesUnknownAndDeletedCrewsToTheStart(t *testing.T) {
	h := newTestProvisioningHandler(t)
	h.provisioner = testProvisioner()
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	crewID := seedCrewRow(t, h.db, "crew-gate-del", wsID, "G", "gate-del")
	if _, err := h.db.Exec(`UPDATE crews SET deleted_at = CURRENT_TIMESTAMP WHERE id = ?`, crewID); err != nil {
		t.Fatalf("delete crew: %v", err)
	}
	for _, id := range []string{"no-such-crew", crewID} {
		if img, err := h.EnsureCrewImage(context.Background(), id); err != nil || img != "" {
			t.Errorf("crew %s: want (\"\", nil) — nothing to provision against, got (%q, %v)", id, img, err)
		}
		h.mu.Lock()
		job := h.jobs[id]
		h.mu.Unlock()
		if job != nil {
			t.Errorf("crew %s: a build was enqueued", id)
		}
	}
}

func TestEnsureCrewImageIsANoOpWithoutProvisioning(t *testing.T) {
	var h *ProvisioningHandler
	if _, err := h.EnsureCrewImage(context.Background(), "x"); err != nil {
		t.Fatalf("nil handler: %v", err)
	}
}

// After the image gate, crewstart refuses any completer error that is not
// marked partial. An undecodable services column must stay partial, or every
// crew with a broken services_json would stop starting at all.
func TestCompleterMarksUnresolvedServicesAsAPartialConfig(t *testing.T) {
	h := newTestProvisioningHandler(t)
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	crewID := seedCrewRow(t, h.db, "crew-gate-partial", wsID, "P", "gate-partial")
	if _, err := h.db.Exec(`UPDATE crews SET cached_image = ?, services_json = ? WHERE id = ?`,
		"crewship-cache:partial", `[{"name":`, crewID); err != nil {
		t.Fatal(err)
	}
	resolved, err := NewCrewConfigCompleter(h.db).CompleteCrewConfig(context.Background(), provider.CrewConfig{ID: crewID})
	if !errors.Is(err, ErrCrewServicesUnresolved) || !errors.Is(err, crewstart.ErrPartialConfig) {
		t.Fatalf("err = %v, want ErrCrewServicesUnresolved marked crewstart.ErrPartialConfig", err)
	}
	if resolved.CachedImage != "crewship-cache:partial" {
		t.Errorf("CachedImage = %q, want the resolved image alongside the partial error", resolved.CachedImage)
	}
}

func TestEnsureCrewImageReturnsTheImageAfterTheBuild(t *testing.T) {
	// The tag the build wrote is what the gate hands to a caller without a
	// completer; the one the crew had before the build is stale.
	h := newTestProvisioningHandler(t)
	h.provisioner = testProvisioner()
	h.provisionPollInterval = 5 * time.Millisecond
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	crewID := seedCrewRow(t, h.db, "crew-gate-tag", wsID, "G", "gate-tag")
	h.mu.Lock()
	h.jobs[crewID] = &ProvisionJob{CrewID: crewID, Status: "running", StartedAt: time.Now()}
	h.mu.Unlock()
	go func() {
		time.Sleep(20 * time.Millisecond)
		if _, err := h.db.Exec(`UPDATE crews SET cached_image = ? WHERE id = ?`, "crewship-cache:rebuilt", crewID); err != nil {
			t.Error(err)
		}
		h.mu.Lock()
		h.jobs[crewID].Status = "completed"
		h.mu.Unlock()
	}()
	img, err := h.EnsureCrewImage(context.Background(), crewID)
	if err != nil || img != "crewship-cache:rebuilt" {
		t.Fatalf("EnsureCrewImage = (%q, %v), want crewship-cache:rebuilt", img, err)
	}
}
