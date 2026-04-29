package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
)

// TestRestartCrewAgents_RealDocker exercises the full restart path against a
// live Docker daemon: spin up a fake crew container with the canonical name
// (crewship-team-{slug}), call the endpoint, and assert the container is
// actually gone. Skipped when no daemon is reachable so unit CI still runs;
// nightly e2e workflow exercises this branch on ubuntu-latest.
//
// Real, not mocked. The failure mode this catches — wrong container-name
// prefix, Docker label filters, role check off-by-one — only shows up when
// you actually talk to the daemon.
func TestRestartCrewAgents_RealDocker(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-Docker restart test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	docker, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("docker client unavailable: %v", err)
	}
	if _, err := docker.Ping(ctx); err != nil {
		t.Skipf("docker daemon not reachable: %v", err)
	}
	defer func() { _ = docker.Close() }()

	// Pull alpine if missing — it's tiny and we need a base image to run.
	if _, err := docker.ImageInspect(ctx, "alpine:3"); err != nil {
		rc, pullErr := docker.ImagePull(ctx, "alpine:3", image.PullOptions{})
		if pullErr != nil {
			t.Skipf("cannot pull alpine: %v", pullErr)
		}
		_, _ = io.Copy(io.Discard, rc)
		_ = rc.Close()
	}

	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewProvisioningHandler(db, logger, nil, nil, docker, "", nil)

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	// Use a unique slug per run so parallel CI doesn't collide on the
	// container name. crewship-team-{slug} is the canonical pattern.
	slug := "restart-test-" + time.Now().Format("150405")
	crewID := "crew-restart-1"
	if _, err := db.Exec(
		`INSERT INTO crews (id, workspace_id, name, slug, devcontainer_config, cached_image)
		 VALUES (?, ?, 'Restartable', ?, ?, 'alpine:3')`,
		crewID, wsID, slug, `{"image":"alpine:3"}`,
	); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	// Two agents in this crew so the response counter is non-trivial.
	for _, agentID := range []string{"agent-r1", "agent-r2"} {
		if _, err := db.Exec(
			`INSERT INTO agents (id, workspace_id, crew_id, name, slug, system_prompt, agent_role)
			 VALUES (?, ?, ?, ?, ?, '', 'AGENT')`,
			agentID, wsID, crewID, agentID, agentID,
		); err != nil {
			t.Fatalf("seed agent %s: %v", agentID, err)
		}
	}

	containerName := crewContainerPrefix + slug
	createResp, err := docker.ContainerCreate(ctx, &container.Config{
		Image: "alpine:3",
		Cmd:   []string{"sleep", "60"},
	}, nil, nil, nil, containerName)
	if err != nil {
		t.Fatalf("create test container: %v", err)
	}
	createdID := createResp.ID
	t.Cleanup(func() {
		// Best-effort cleanup if the test fails before RestartCrewAgents
		// removed the container.
		_ = docker.ContainerRemove(context.Background(), createdID, container.RemoveOptions{Force: true})
	})
	if err := docker.ContainerStart(ctx, createdID, container.StartOptions{}); err != nil {
		t.Fatalf("start test container: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/crews/{crewId}/restart-agents", h.RestartCrewAgents)

	req := httptest.NewRequest("POST", "/api/v1/crews/"+crewID+"/restart-agents", nil)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Restarted int `json:"restarted"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Restarted != 2 {
		t.Errorf("restarted = %d, want 2 (we seeded 2 agents)", resp.Restarted)
	}

	// The container must actually be gone — that's the whole point.
	if _, err := docker.ContainerInspect(ctx, createdID); err == nil {
		t.Errorf("container %s still exists after restart", createdID[:12])
	}
}

// TestRestartCrewAgents_NoContainer is the "nothing to do" path — endpoint
// should return 200 with restarted=0 even if the crew has no live container.
// Always runs; no Docker needed because we pass nil docker (and the handler
// returns 503 in that case, not the no-container path). So we install a
// stub-friendly variant here: skip if no Docker, else assert the no-name path.
func TestRestartCrewAgents_NoContainer(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real-Docker restart test in short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	docker, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("docker client unavailable: %v", err)
	}
	if _, err := docker.Ping(ctx); err != nil {
		t.Skipf("docker daemon not reachable: %v", err)
	}
	defer func() { _ = docker.Close() }()

	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewProvisioningHandler(db, logger, nil, nil, docker, "", nil)

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	crewID := "crew-restart-nocnt"
	if _, err := db.Exec(
		`INSERT INTO crews (id, workspace_id, name, slug, devcontainer_config)
		 VALUES (?, ?, 'Quiet', 'quiet-no-container', ?)`,
		crewID, wsID, `{"image":"alpine:3"}`,
	); err != nil {
		t.Fatalf("seed crew: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/crews/{crewId}/restart-agents", h.RestartCrewAgents)

	req := httptest.NewRequest("POST", "/api/v1/crews/"+crewID+"/restart-agents", nil)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Restarted int `json:"restarted"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Restarted != 0 {
		t.Errorf("restarted = %d, want 0 (no container existed)", resp.Restarted)
	}
}

// TestRestartCrewAgents_ForbiddenRole locks down the authorization gate. Roles
// below MANAGER must get 403 — destroying a live crew container is a
// privileged action even when the workspace is otherwise readable. No Docker
// needed: the role check fires before any container work.
func TestRestartCrewAgents_ForbiddenRole(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewProvisioningHandler(db, logger, nil, nil, nil, "", nil)

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/crews/{crewId}/restart-agents", h.RestartCrewAgents)

	for _, role := range []string{"MEMBER", "VIEWER", ""} {
		t.Run("role="+role, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/v1/crews/whatever/restart-agents", nil)
			req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, role))
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)
			if rr.Code != http.StatusForbidden {
				t.Errorf("role %q: status = %d, want 403; body: %s", role, rr.Code, rr.Body.String())
			}
		})
	}
}

// TestRestartCrewAgents_NoDocker covers the 503 path. With a permitted role
// but a nil Docker client (e.g. server started without container runtime),
// the handler must return 503 — not 200, not 500. Catches regressions where
// the nil-check is reordered after the DB call.
func TestRestartCrewAgents_NoDocker(t *testing.T) {
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewProvisioningHandler(db, logger, nil, nil, nil /* docker */, "", nil)

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/crews/{crewId}/restart-agents", h.RestartCrewAgents)

	req := httptest.NewRequest("POST", "/api/v1/crews/anything/restart-agents", nil)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body: %s", rr.Code, rr.Body.String())
	}
}

