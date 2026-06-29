package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/devcontainer"
)

// featuresCfg is a devcontainer.json that both crewNeedsProvision and
// EnqueueForCrew accept (non-empty config WITH features), so EnsureProvisioned
// reaches its wait loop instead of short-circuiting.
const featuresCfg = `{"image":"mcr.microsoft.com/devcontainers/base:bookworm","features":{"ghcr.io/devcontainers/features/github-cli:1":{}}}`

// testProvisioner returns a non-nil provisioner backed by the in-package fake
// commit client, so EnsureProvisioned passes its "provisioning enabled" gate
// without touching Docker.
func testProvisioner() *devcontainer.Provisioner {
	return devcontainer.NewProvisioner(&covCommitClient{}, nil, nil, newTestLogger())
}

func TestCrewNeedsProvision(t *testing.T) {
	cases := []struct {
		name       string
		devcontain string
		mise       string
		want       bool
	}{
		{"empty", "", "", false},
		{"mise only", "", `{"node":"22"}`, true},
		{"mise whitespace", "", "   ", false},
		{"features", featuresCfg, "", true},
		{"postCreate", `{"image":"debian:bookworm","postCreateCommand":"echo hi"}`, "", true},
		{"image only (no-op)", `{"image":"debian:bookworm"}`, "", false},
		{"containerEnv only (no-op)", `{"image":"debian:bookworm","containerEnv":{"FOO":"bar"}}`, "", false},
		{"unparseable → false", `{not json`, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := crewNeedsProvision(tc.devcontain, tc.mise); got != tc.want {
				t.Errorf("crewNeedsProvision(%q,%q) = %v, want %v", tc.devcontain, tc.mise, got, tc.want)
			}
		})
	}
}

func TestBuildCrewRuntimeConfig(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-brc", wsID, "BRC", "brc")

	// cached_requirements carries feature-aggregated env + a privileged flag
	// (e.g. docker-in-docker), plus the crew's own devcontainer containerEnv
	// which must override the feature default.
	if _, err := db.Exec(`UPDATE crews SET
			runtime_image = ?, cached_image = ?,
			devcontainer_config = ?, mise_config = ?,
			cached_requirements = ?
		WHERE id = ?`,
		"mcr.microsoft.com/devcontainers/javascript-node:22-bookworm",
		"crewship-cache:deadbeef",
		`{"image":"mcr.microsoft.com/devcontainers/javascript-node:22-bookworm","containerEnv":{"SHARED":"crew","ONLY_CREW":"1"}}`,
		`{"node":"22"}`,
		`{"containerEnv":{"SHARED":"feature","ONLY_FEATURE":"1"},"privileged":true,"capAdd":["SYS_PTRACE"]}`,
		crewID,
	); err != nil {
		t.Fatalf("update crew: %v", err)
	}

	cfg, err := buildCrewRuntimeConfig(context.Background(), db, crewID, wsID)
	if err != nil {
		t.Fatalf("buildCrewRuntimeConfig: %v", err)
	}

	if cfg.CachedImage != "crewship-cache:deadbeef" {
		t.Errorf("CachedImage = %q, want crewship-cache:deadbeef", cfg.CachedImage)
	}
	if cfg.Image != "mcr.microsoft.com/devcontainers/javascript-node:22-bookworm" {
		t.Errorf("Image = %q", cfg.Image)
	}
	if cfg.Slug != "brc" {
		t.Errorf("Slug = %q, want brc", cfg.Slug)
	}
	if !cfg.Privileged {
		t.Error("Privileged = false, want true (from cached_requirements)")
	}
	if len(cfg.CapAdd) != 1 || cfg.CapAdd[0] != "SYS_PTRACE" {
		t.Errorf("CapAdd = %v, want [SYS_PTRACE]", cfg.CapAdd)
	}
	// devcontainer.json containerEnv overrides feature-aggregated value.
	if cfg.ContainerEnv["SHARED"] != "crew" {
		t.Errorf("ContainerEnv[SHARED] = %q, want crew (crew overrides feature)", cfg.ContainerEnv["SHARED"])
	}
	if cfg.ContainerEnv["ONLY_FEATURE"] != "1" {
		t.Errorf("ContainerEnv[ONLY_FEATURE] = %q, want 1", cfg.ContainerEnv["ONLY_FEATURE"])
	}
	if cfg.ContainerEnv["ONLY_CREW"] != "1" {
		t.Errorf("ContainerEnv[ONLY_CREW] = %q, want 1", cfg.ContainerEnv["ONLY_CREW"])
	}
}

// fakeEnqueuer records EnqueueForCrew calls for the proactive-provision tests.
type fakeEnqueuer struct {
	calls int
	last  string
	err   error
}

func (f *fakeEnqueuer) EnqueueForCrew(_ context.Context, crewID, _ string) (EnqueueResult, error) {
	f.calls++
	f.last = crewID
	if f.err != nil {
		return EnqueueResult{}, f.err
	}
	return EnqueueResult{Started: true}, nil
}

func TestMaybeAutoProvision(t *testing.T) {
	cases := []struct {
		name       string
		hasProv    bool
		devcontain string
		mise       string
		wantCalls  int
	}{
		{"needs provision → enqueues", true, featuresCfg, "", 1},
		{"mise-only needs provision → enqueues", true, "", `{"node":"22"}`, 1},
		{"no provision needed → skips", true, `{"image":"debian:bookworm"}`, "", 0},
		{"nil provisioner → skips (no panic)", false, featuresCfg, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &CrewHandler{logger: newTestLogger()}
			var fe *fakeEnqueuer
			if tc.hasProv {
				fe = &fakeEnqueuer{}
				h.SetProvisioner(fe)
			}
			h.maybeAutoProvision("crew-x", "ws-x", tc.devcontain, tc.mise)
			got := 0
			if fe != nil {
				got = fe.calls
			}
			if got != tc.wantCalls {
				t.Errorf("enqueue calls = %d, want %d", got, tc.wantCalls)
			}
		})
	}
}

func TestMaybeAutoProvision_EnqueueErrorIsNonFatal(t *testing.T) {
	h := &CrewHandler{logger: newTestLogger()}
	h.SetProvisioner(&fakeEnqueuer{err: errors.New("rate limited")})
	// Must not panic / must return cleanly even when enqueue fails — the
	// dispatch-time gate is the safety net.
	h.maybeAutoProvision("crew-x", "ws-x", featuresCfg, "")
}

func TestEnsureProvisioned_NilProvisioner_NoOp(t *testing.T) {
	h := newTestProvisioningHandler(t)
	h.provisioner = nil // disabled
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	crewID := seedCrewRow(t, h.db, "crew-ep-nil", wsID, "N", "ep-nil")
	if err := h.EnsureProvisioned(context.Background(), crewID, wsID, time.Second); err != nil {
		t.Fatalf("want nil with provisioning disabled, got %v", err)
	}
}

func TestEnsureProvisioned_NoProvisionNeeded(t *testing.T) {
	h := newTestProvisioningHandler(t)
	h.provisioner = testProvisioner()
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	crewID := seedCrewRow(t, h.db, "crew-ep-noop", wsID, "N", "ep-noop")
	// No features, no mise → crewNeedsProvision=false → returns immediately.
	if err := h.EnsureProvisioned(context.Background(), crewID, wsID, time.Second); err != nil {
		t.Fatalf("want nil when no provisioning needed, got %v", err)
	}
}

func TestEnsureProvisioned_CachedImagePresent(t *testing.T) {
	h := newTestProvisioningHandler(t)
	h.provisioner = testProvisioner()
	// h.docker is nil in tests → imagePresentLocally returns true, so a crew
	// that needs provisioning but already has a cached image short-circuits.
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	crewID := seedCrewRow(t, h.db, "crew-ep-cached", wsID, "C", "ep-cached")
	if _, err := h.db.Exec(`UPDATE crews SET devcontainer_config = ?, cached_image = ? WHERE id = ?`,
		featuresCfg, "crewship-cache:already", crewID); err != nil {
		t.Fatalf("update crew: %v", err)
	}
	if err := h.EnsureProvisioned(context.Background(), crewID, wsID, time.Second); err != nil {
		t.Fatalf("want nil when cached image present, got %v", err)
	}
}

func TestEnsureProvisioned_WaitsForCompletion(t *testing.T) {
	h := newTestProvisioningHandler(t)
	h.provisioner = testProvisioner()
	h.provisionPollInterval = 5 * time.Millisecond
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	crewID := seedCrewRow(t, h.db, "crew-ep-wait", wsID, "W", "ep-wait")
	if _, err := h.db.Exec(`UPDATE crews SET devcontainer_config = ? WHERE id = ?`, featuresCfg, crewID); err != nil {
		t.Fatalf("update crew: %v", err)
	}
	// Pre-seed an in-flight job so EnqueueForCrew fast-paths to AlreadyRunning
	// (no real build goroutine). The test then flips it to completed.
	h.mu.Lock()
	h.jobs[crewID] = &ProvisionJob{CrewID: crewID, Status: "running", StartedAt: time.Now()}
	h.mu.Unlock()

	go func() {
		time.Sleep(20 * time.Millisecond)
		h.mu.Lock()
		h.jobs[crewID].Status = "completed"
		h.mu.Unlock()
	}()

	if err := h.EnsureProvisioned(context.Background(), crewID, wsID, 5*time.Second); err != nil {
		t.Fatalf("want nil after job completes, got %v", err)
	}
}

func TestEnsureProvisioned_FailedBuild(t *testing.T) {
	h := newTestProvisioningHandler(t)
	h.provisioner = testProvisioner()
	h.provisionPollInterval = 5 * time.Millisecond
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	crewID := seedCrewRow(t, h.db, "crew-ep-fail", wsID, "F", "ep-fail")
	if _, err := h.db.Exec(`UPDATE crews SET devcontainer_config = ? WHERE id = ?`, featuresCfg, crewID); err != nil {
		t.Fatalf("update crew: %v", err)
	}
	h.mu.Lock()
	h.jobs[crewID] = &ProvisionJob{CrewID: crewID, Status: "running", StartedAt: time.Now()}
	h.mu.Unlock()

	go func() {
		time.Sleep(20 * time.Millisecond)
		h.mu.Lock()
		h.jobs[crewID].Status = "failed"
		h.jobs[crewID].Error = "feature download failed"
		h.mu.Unlock()
	}()

	if err := h.EnsureProvisioned(context.Background(), crewID, wsID, 5*time.Second); err == nil {
		t.Fatal("want error when build fails, got nil")
	}
}

func TestEnsureProvisioned_ContextCancel(t *testing.T) {
	h := newTestProvisioningHandler(t)
	h.provisioner = testProvisioner()
	h.provisionPollInterval = 5 * time.Millisecond
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	crewID := seedCrewRow(t, h.db, "crew-ep-ctx", wsID, "X", "ep-ctx")
	if _, err := h.db.Exec(`UPDATE crews SET devcontainer_config = ? WHERE id = ?`, featuresCfg, crewID); err != nil {
		t.Fatalf("update crew: %v", err)
	}
	h.mu.Lock()
	h.jobs[crewID] = &ProvisionJob{CrewID: crewID, Status: "running", StartedAt: time.Now()}
	h.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	if err := h.EnsureProvisioned(ctx, crewID, wsID, 5*time.Second); err == nil {
		t.Fatal("want ctx error on cancel, got nil")
	}
}
