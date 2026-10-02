package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProvisionRebuildConflictPreservesWorkingCache(t *testing.T) {
	h, wsID, crewID := covProvRig(t, &covCommitClient{}, `{"image":"ubuntu:22.04"}`)
	if _, err := h.db.Exec(`UPDATE crews SET cached_image = 'crewship-cache:previous', config_hash = 'previous' WHERE id = ?`, crewID); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	h.jobs[crewID] = &ProvisionJob{CrewID: crewID, Status: "running"}
	h.mu.Unlock()
	req := httptest.NewRequest("POST", "/x", nil)
	req.SetPathValue("crewId", crewID)
	req = withWorkspaceUser(req, "test-user-id", wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ProvisionRebuild(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var image, hash string
	if err := h.db.QueryRow(`SELECT cached_image, config_hash FROM crews WHERE id = ?`, crewID).Scan(&image, &hash); err != nil {
		t.Fatal(err)
	}
	if image != "crewship-cache:previous" || hash != "previous" {
		t.Fatalf("rejected rebuild erased cache: %q %q", image, hash)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.jobs[crewID].forceRebuild {
		t.Fatal("a rebuild must not mutate an admitted job")
	}
}

func TestProvisionRebuildAdmitsForcedJob(t *testing.T) {
	h, wsID, crewID := covProvRig(t, &covCommitClient{}, `{"image":"ubuntu:22.04"}`)
	req := httptest.NewRequest("POST", "/x", nil)
	req.SetPathValue("crewId", crewID)
	req = withWorkspaceUser(req, "test-user-id", wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ProvisionRebuild(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	job := h.jobs[crewID]
	if job == nil || !job.forceRebuild {
		t.Fatal("explicit rebuild did not carry cache bypass into the admitted job")
	}
}
