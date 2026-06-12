package api

// Coverage for crew_provisioning_jobs.go — runProvisioning's failure and
// success tails, ProvisionTrigger's sentinel→status mapping,
// ProvisionRebuild, EnqueueForCrew's idempotency, and ProvisionStatus's
// in-memory job snapshot.
//
// The Provisioner accepts any devcontainer.CommitClient, so a small fake
// stands in for Docker: ImageList decides the cache check, and
// ContainerCreate failing aborts the real-build path right after the
// plan/progress callbacks have fired — every callback inside
// runProvisioning runs without a daemon.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	dockernetwork "github.com/docker/docker/api/types/network"
	dockerclient "github.com/docker/docker/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/crewship-ai/crewship/internal/devcontainer"
)

// covCommitClient is a minimal devcontainer.CommitClient fake.
type covCommitClient struct {
	listErr   error
	createErr error
}

func (c *covCommitClient) ContainerCreate(_ context.Context, _ *container.Config, _ *container.HostConfig, _ *dockernetwork.NetworkingConfig, _ *ocispec.Platform, _ string) (container.CreateResponse, error) {
	if c.createErr != nil {
		return container.CreateResponse{}, c.createErr
	}
	return container.CreateResponse{ID: "tmp-1"}, nil
}
func (c *covCommitClient) ContainerStart(_ context.Context, _ string, _ container.StartOptions) error {
	return nil
}
func (c *covCommitClient) ContainerStop(_ context.Context, _ string, _ container.StopOptions) error {
	return nil
}
func (c *covCommitClient) ContainerRemove(_ context.Context, _ string, _ container.RemoveOptions) error {
	return nil
}
func (c *covCommitClient) ContainerCommit(_ context.Context, _ string, _ container.CommitOptions) (container.CommitResponse, error) {
	return container.CommitResponse{ID: "sha256:x"}, nil
}
func (c *covCommitClient) ImageList(_ context.Context, _ image.ListOptions) ([]image.Summary, error) {
	if c.listErr != nil {
		return nil, c.listErr
	}
	return nil, nil // no cached images
}
func (c *covCommitClient) ImagePull(_ context.Context, _ string, _ image.PullOptions) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (c *covCommitClient) ImageInspect(_ context.Context, _ string, _ ...dockerclient.ImageInspectOption) (image.InspectResponse, error) {
	return image.InspectResponse{}, errors.New("no such image")
}

// covProvRig builds a ProvisioningHandler whose provisioner runs against
// the fake CommitClient, plus a seeded crew carrying the given
// devcontainer_config.
func covProvRig(t *testing.T, fake *covCommitClient, devcontainerCfg string) (h *ProvisioningHandler, wsID, crewID string) {
	t.Helper()
	h = newTestProvisioningHandler(t)
	if fake != nil {
		h.provisioner = devcontainer.NewProvisioner(fake, nil, nil, newTestLogger())
	}
	userID := seedTestUser(t, h.db)
	wsID = seedTestWorkspace(t, h.db, userID)
	crewID = seedCrewRow(t, h.db, "crew-prov-cov", wsID, "Prov", "prov-cov")
	if devcontainerCfg != "" {
		if _, err := h.db.Exec(`UPDATE crews SET devcontainer_config = ? WHERE id = ?`, devcontainerCfg, crewID); err != nil {
			t.Fatalf("set devcontainer_config: %v", err)
		}
	}
	return h, wsID, crewID
}

func covJob(crewID string) *ProvisionJob {
	return &ProvisionJob{CrewID: crewID, Status: "pending", StartedAt: time.Now()}
}

// ---- runProvisioning (called synchronously for determinism) ----

func TestRunProvisioning_ParseError_MarksFailed(t *testing.T) {
	h, wsID, crewID := covProvRig(t, &covCommitClient{}, "")
	job := covJob(crewID)
	h.jobs[crewID] = job
	h.rateLimiter.running[wsID] = 1 // simulate the slot EnqueueForCrew acquires

	h.runProvisioning(crewID, wsID, "{not-json", "", "", job)

	h.mu.RLock()
	defer h.mu.RUnlock()
	if job.Status != "failed" {
		t.Errorf("status = %q, want failed", job.Status)
	}
	if !strings.Contains(job.Error, "parse devcontainer_config") {
		t.Errorf("error = %q", job.Error)
	}
	if job.CompletedAt == nil {
		t.Error("CompletedAt not set")
	}
	if h.rateLimiter.running[wsID] != 0 {
		t.Errorf("rate-limit slot not released: %d", h.rateLimiter.running[wsID])
	}
}

// covLocalRef is an image reference whose "registry" is a closed loopback
// port. The provisioner's best-effort remote-digest HEAD fails instantly
// with connection-refused instead of reaching out to a real registry,
// keeping the non-skip build path fast and offline.
const covLocalRef = "127.0.0.1:1/cov/test-img:1"

func TestRunProvisioning_ProvisionError_PlanAndProgressEmitted(t *testing.T) {
	fake := &covCommitClient{createErr: errors.New("daemon exploded")}
	h, wsID, crewID := covProvRig(t, fake, "")
	job := covJob(crewID)
	h.jobs[crewID] = job

	// containerEnv forces the non-skip build path: plan fires, the pull
	// progress step fires, then createTempContainer fails.
	cfg := `{"image":"` + covLocalRef + `","containerEnv":{"FOO":"bar"}}`
	h.runProvisioning(crewID, wsID, cfg, "", "", job)

	if job.Status != "failed" || !strings.Contains(job.Error, "daemon exploded") {
		t.Fatalf("status=%q error=%q", job.Status, job.Error)
	}
	if len(job.Steps) == 0 {
		t.Error("plan callback never populated job.Steps")
	}
	if job.Total == 0 || job.Step == 0 {
		t.Errorf("progress callback never ran: step=%d total=%d", job.Step, job.Total)
	}
	if len(job.LogTail) == 0 {
		t.Error("LogTail empty — progress message not recorded")
	} else if !strings.Contains(job.LogTail[0], "Pulling base image "+covLocalRef) {
		t.Errorf("LogTail[0] = %q", job.LogTail[0])
	}
}

func TestRunProvisioning_RuntimeImageOverride(t *testing.T) {
	fake := &covCommitClient{createErr: errors.New("stop here")}
	h, wsID, crewID := covProvRig(t, fake, "")
	job := covJob(crewID)
	h.jobs[crewID] = job

	override := "127.0.0.1:1/cov/override-img:2"
	cfg := `{"image":"` + covLocalRef + `","containerEnv":{"A":"b"}}`
	h.runProvisioning(crewID, wsID, cfg, "", override, job)

	if len(job.LogTail) == 0 || !strings.Contains(job.LogTail[0], override) {
		t.Errorf("runtime_image override not used as base: tail=%v", job.LogTail)
	}
}

func TestRunProvisioning_SkipPath_CompletesAndPersists(t *testing.T) {
	// No features / postCreate / containerEnv / mise → the provisioner
	// skips the build and returns CachedImage "" — runProvisioning's full
	// success tail (DB update, completed status) runs without Docker.
	h, wsID, crewID := covProvRig(t, &covCommitClient{}, "")
	job := covJob(crewID)
	h.jobs[crewID] = job

	h.runProvisioning(crewID, wsID, `{"image":"ubuntu:22.04"}`, "", "", job)

	if job.Status != "completed" {
		t.Fatalf("status = %q (err=%q), want completed", job.Status, job.Error)
	}
	if job.CompletedAt == nil {
		t.Error("CompletedAt not set")
	}
	if job.ConfigHash == "" {
		t.Error("ConfigHash not recorded on job")
	}
	var dbHash string
	if err := h.db.QueryRow(`SELECT COALESCE(config_hash,'') FROM crews WHERE id = ?`, crewID).Scan(&dbHash); err != nil {
		t.Fatalf("query crew: %v", err)
	}
	if dbHash != job.ConfigHash {
		t.Errorf("db config_hash = %q, want %q", dbHash, job.ConfigHash)
	}
}

func TestRunProvisioning_ImageListError_MarksFailed(t *testing.T) {
	fake := &covCommitClient{listErr: errors.New("daemon down")}
	h, wsID, crewID := covProvRig(t, fake, "")
	job := covJob(crewID)
	h.jobs[crewID] = job

	h.runProvisioning(crewID, wsID, `{"image":"ubuntu:22.04"}`, "", "", job)

	if job.Status != "failed" || !strings.Contains(job.Error, "daemon down") {
		t.Errorf("status=%q error=%q", job.Status, job.Error)
	}
}

// ---- EnqueueForCrew ----

func TestEnqueueForCrew_SentinelErrors(t *testing.T) {
	t.Run("no provisioner", func(t *testing.T) {
		h := newTestProvisioningHandler(t)
		_, err := h.EnqueueForCrew(context.Background(), "c", "w")
		if !errors.Is(err, ErrProvisionerUnavailable) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("empty crew id", func(t *testing.T) {
		h, _, _ := covProvRig(t, &covCommitClient{}, "")
		_, err := h.EnqueueForCrew(context.Background(), "", "w")
		if !errors.Is(err, ErrInvalidCrewID) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("crew not found", func(t *testing.T) {
		h, wsID, _ := covProvRig(t, &covCommitClient{}, "")
		_, err := h.EnqueueForCrew(context.Background(), "ghost", wsID)
		if !errors.Is(err, ErrCrewNotFound) {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("no devcontainer config", func(t *testing.T) {
		h, wsID, crewID := covProvRig(t, &covCommitClient{}, "")
		_, err := h.EnqueueForCrew(context.Background(), crewID, wsID)
		if !errors.Is(err, ErrCrewNoDevcontainer) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestEnqueueForCrew_AlreadyRunningFastPath(t *testing.T) {
	h, wsID, crewID := covProvRig(t, &covCommitClient{}, `{"image":"ubuntu:22.04"}`)
	h.jobs[crewID] = &ProvisionJob{CrewID: crewID, Status: "running", StartedAt: time.Now()}
	res, err := h.EnqueueForCrew(context.Background(), crewID, wsID)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !res.AlreadyRunning || res.Status != "running" || res.Started {
		t.Errorf("res = %+v", res)
	}
}

func TestEnqueueForCrew_RateLimited(t *testing.T) {
	h, wsID, crewID := covProvRig(t, &covCommitClient{}, `{"image":"ubuntu:22.04"}`)
	h.rateLimiter.running[wsID] = maxConcurrentProvisionsPerWorkspace
	_, err := h.EnqueueForCrew(context.Background(), crewID, wsID)
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("err = %v, want ErrRateLimited", err)
	}
}

// ---- ProvisionTrigger ----

func TestProvisionTrigger_StatusMapping(t *testing.T) {
	h, wsID, crewID := covProvRig(t, &covCommitClient{}, `{"image":"ubuntu:22.04"}`)
	userID := "test-user-id"

	run := func(crew, role string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/x", nil)
		req.SetPathValue("crewId", crew)
		req = withWorkspaceUser(req, userID, wsID, role)
		rr := httptest.NewRecorder()
		h.ProvisionTrigger(rr, req)
		return rr
	}

	t.Run("viewer forbidden", func(t *testing.T) {
		if rr := run(crewID, "VIEWER"); rr.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", rr.Code)
		}
	})
	t.Run("crew not found 404", func(t *testing.T) {
		if rr := run("ghost", "OWNER"); rr.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rr.Code)
		}
	})
	t.Run("missing crew id 400", func(t *testing.T) {
		if rr := run("", "OWNER"); rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})
	t.Run("started 202 then conflict 409", func(t *testing.T) {
		rr := run(crewID, "OWNER")
		if rr.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202; body=%s", rr.Code, rr.Body.String())
		}
		// Wait for the async skip-path build to reach a terminal state so
		// re-marking the job below can't race the goroutine's own writes.
		deadline := time.Now().Add(3 * time.Second)
		for {
			h.mu.RLock()
			st := h.jobs[crewID].Status
			h.mu.RUnlock()
			if st == "completed" || st == "failed" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("job never finished (status=%s)", st)
			}
			time.Sleep(5 * time.Millisecond)
		}
		// Force the job to look in-flight for the conflict branch.
		h.mu.Lock()
		h.jobs[crewID].Status = "running"
		h.mu.Unlock()
		rr2 := run(crewID, "OWNER")
		if rr2.Code != http.StatusConflict {
			t.Fatalf("second trigger status = %d, want 409; body=%s", rr2.Code, rr2.Body.String())
		}
		var prob map[string]any
		if err := json.NewDecoder(rr2.Body).Decode(&prob); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if prob["job_status"] != "running" {
			t.Errorf("job_status = %v", prob["job_status"])
		}
	})
	t.Run("rate limited 429", func(t *testing.T) {
		h2, wsID2, crewID2 := covProvRig(t, &covCommitClient{}, `{"image":"ubuntu:22.04"}`)
		h2.rateLimiter.running[wsID2] = maxConcurrentProvisionsPerWorkspace
		req := httptest.NewRequest("POST", "/x", nil)
		req.SetPathValue("crewId", crewID2)
		req = withWorkspaceUser(req, userID, wsID2, "OWNER")
		rr := httptest.NewRecorder()
		h2.ProvisionTrigger(rr, req)
		if rr.Code != http.StatusTooManyRequests {
			t.Errorf("status = %d, want 429", rr.Code)
		}
	})
	t.Run("no provisioner 503", func(t *testing.T) {
		h3 := newTestProvisioningHandler(t)
		userID3 := seedTestUser(t, h3.db)
		wsID3 := seedTestWorkspace(t, h3.db, userID3)
		crewID3 := seedCrewRow(t, h3.db, "crew-noprov", wsID3, "NP", "np")
		req := httptest.NewRequest("POST", "/x", nil)
		req.SetPathValue("crewId", crewID3)
		req = withWorkspaceUser(req, userID3, wsID3, "OWNER")
		rr := httptest.NewRecorder()
		h3.ProvisionTrigger(rr, req)
		if rr.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d, want 503", rr.Code)
		}
	})
}

// ---- ProvisionRebuild ----

func TestProvisionRebuild_Matrix(t *testing.T) {
	h, wsID, crewID := covProvRig(t, &covCommitClient{}, `{"image":"ubuntu:22.04"}`)
	userID := "test-user-id"
	if _, err := h.db.Exec(`UPDATE crews SET cached_image = 'crewship-cache:old', config_hash = 'oldhash' WHERE id = ?`, crewID); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	run := func(crew, role string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/x", nil)
		req.SetPathValue("crewId", crew)
		req = withWorkspaceUser(req, userID, wsID, role)
		rr := httptest.NewRecorder()
		h.ProvisionRebuild(rr, req)
		return rr
	}

	t.Run("viewer forbidden", func(t *testing.T) {
		if rr := run(crewID, "VIEWER"); rr.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", rr.Code)
		}
	})
	t.Run("missing crew id 400", func(t *testing.T) {
		if rr := run("", "OWNER"); rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})
	t.Run("happy path clears cache and starts", func(t *testing.T) {
		rr := run(crewID, "OWNER")
		if rr.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202; body=%s", rr.Code, rr.Body.String())
		}
		var cached, hash sql.NullString
		if err := h.db.QueryRow(`SELECT cached_image, config_hash FROM crews WHERE id = ?`, crewID).Scan(&cached, &hash); err != nil {
			t.Fatalf("query: %v", err)
		}
		// The async rebuild may have already completed (skip path writes a
		// fresh hash) — but the OLD cache values must be gone either way.
		if cached.Valid && cached.String == "crewship-cache:old" {
			t.Errorf("cached_image still old: %v", cached.String)
		}
		if hash.Valid && hash.String == "oldhash" {
			t.Errorf("config_hash still old: %v", hash.String)
		}
	})
}

// ---- ProvisionStatus ----

func TestProvisionStatus_JobSnapshotAndFallbacks(t *testing.T) {
	h, wsID, crewID := covProvRig(t, &covCommitClient{}, `{"image":"ubuntu:22.04"}`)
	userID := "test-user-id"

	run := func(crew string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/x", nil)
		req.SetPathValue("crewId", crew)
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.ProvisionStatus(rr, req)
		return rr
	}

	t.Run("missing crew id 400", func(t *testing.T) {
		if rr := run(""); rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})
	t.Run("unknown crew 404", func(t *testing.T) {
		if rr := run("ghost"); rr.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rr.Code)
		}
	})
	t.Run("idle without job or cache", func(t *testing.T) {
		rr := run(crewID)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d", rr.Code)
		}
		var out map[string]any
		_ = json.NewDecoder(rr.Body).Decode(&out)
		if out["status"] != "idle" {
			t.Errorf("status = %v, want idle", out["status"])
		}
	})
	t.Run("running job snapshot with steps and log tail", func(t *testing.T) {
		done := time.Now()
		h.mu.Lock()
		h.jobs[crewID] = &ProvisionJob{
			CrewID:      crewID,
			Status:      "failed",
			StartedAt:   time.Now().Add(-1 * time.Minute),
			CompletedAt: &done,
			Error:       "boom",
			Step:        2,
			Total:       3,
			Message:     "Installing python",
			Steps:       []string{"Pulling base image", "Installing python", "Committing image"},
			LogTail:     []string{"Pulling base image", "Installing python"},
		}
		h.mu.Unlock()
		rr := run(crewID)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d", rr.Code)
		}
		var out map[string]any
		if err := json.NewDecoder(rr.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if out["status"] != "failed" || out["error"] != "boom" {
			t.Errorf("status=%v error=%v", out["status"], out["error"])
		}
		if out["step"] != float64(2) || out["total"] != float64(3) || out["message"] != "Installing python" {
			t.Errorf("progress = %v/%v %v", out["step"], out["total"], out["message"])
		}
		if steps, _ := out["steps"].([]any); len(steps) != 3 {
			t.Errorf("steps = %v", out["steps"])
		}
		if tail, _ := out["log_tail"].([]any); len(tail) != 2 {
			t.Errorf("log_tail = %v", out["log_tail"])
		}
		if out["started_at"] == nil || out["completed_at"] == nil {
			t.Errorf("timestamps missing: %v / %v", out["started_at"], out["completed_at"])
		}
	})
	t.Run("cached image without job reports completed", func(t *testing.T) {
		h.mu.Lock()
		delete(h.jobs, crewID)
		h.mu.Unlock()
		if _, err := h.db.Exec(`UPDATE crews SET cached_image = 'crewship-cache:abc' WHERE id = ?`, crewID); err != nil {
			t.Fatalf("update: %v", err)
		}
		rr := run(crewID)
		var out map[string]any
		_ = json.NewDecoder(rr.Body).Decode(&out)
		if out["status"] != "completed" {
			t.Errorf("status = %v, want completed", out["status"])
		}
		if out["cached_image"] != "crewship-cache:abc" {
			t.Errorf("cached_image = %v", out["cached_image"])
		}
	})
}
