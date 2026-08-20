package api

// Starting a crew, on purpose.
//
// Until this endpoint there was no way to. `crew provision` builds an
// image and stops — the container is created lazily on the crew's first
// agent run — so the only route to a running crew was to spend tokens
// running an agent at it. That gap had a cost with a name: writing a
// file into a crew-owned tree (`/crew/shared`, owned by uid 1001)
// requires the server to replay the write INSIDE the container, so on a
// stopped crew it answers 409 "start the crew and retry" — advice no
// command could follow. Deploys read `provisioned`, believed the crew
// was up, and retried the 409 until somebody guessed.
//
// The implementation is deliberately three lines of policy and no
// invention: EnsureProvisioned, buildCrewRuntimeConfig, crewstart.Start
// — byte for byte the sequence the dispatch path runs before an agent
// (assignments_run.go). internal/crewstart exists because thirteen call
// sites once each had their own idea of what "start a crew" meant and
// disagreed invisibly: one started declared sidecars, three ignored the
// provisioned image. A fourteenth idea is the one thing this endpoint
// must not add.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/crewstart"
)

// ContainerStart brings a crew's runtime container up and returns once
// it is running. POST /api/v1/crews/{crewId}/container-start.
//
// Idempotent: EnsureCrewRuntime is get-or-create, so calling it on a
// running crew returns the existing container rather than an error. A
// script that starts a crew before writing files should not have to
// branch on whether it was already up.
//
// Synchronous, because the caller's next action depends on the answer —
// an async 202 would put the "is it up yet?" poll back on every caller,
// which is the shape that made `provision` misleading. EnsureProvisioned
// returns immediately unless the crew genuinely has no image, so the
// common case is one round trip; a cold crew blocks on its build, and
// the request context bounds the wait.
func (h *CrewHandler) ContainerStart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	workspaceID := WorkspaceIDFromContext(ctx)

	if !canRole(RoleFromContext(ctx), "create") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	crewID := r.PathValue("crewId")
	if crewID == "" {
		replyError(w, http.StatusBadRequest, "crewId is required")
		return
	}

	// Scope to the caller's workspace before touching a runtime. Done
	// here rather than left to buildCrewRuntimeConfig so the refusal is
	// a clean 404 and so no provisioning job is enqueued for a crew the
	// caller cannot see.
	var slug string
	err := h.db.QueryRowContext(ctx,
		"SELECT slug FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		crewID, workspaceID).Scan(&slug)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "Crew not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "container start: crew lookup", err)
		return
	}

	if h.container == nil {
		// 503 rather than 500: nothing is broken, this instance simply
		// has no container runtime wired (--no-docker, a daemon that
		// was unreachable at boot). Naming it is what stops the reader
		// debugging their crew instead of their deployment.
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "no container runtime is configured on this server, so crews cannot be started here",
		})
		return
	}

	// Build the image first if the crew has none. A crew started from
	// the bare runtime image has no agent CLI, and the failure surfaces
	// much later as exit 127 — see assignments_run.go, which gates the
	// same way for the same reason. Returns immediately when the image
	// is already present, which is the common case.
	if h.provisioner != nil {
		if err := h.provisioner.EnsureProvisioned(ctx, crewID, workspaceID, 0); err != nil {
			h.logger.Error("container start: ensure provisioned", "error", err, "crew_id", crewID)
			replyError(w, http.StatusBadGateway, "preparing the crew container image failed: "+err.Error())
			return
		}
	}

	// Resolve the crew's FULL config — cached image, mounts, env, caps,
	// limits, declared sidecars. Fail closed: falling back to a bare
	// {slug, id} would start the crew from the base image and without
	// its datastores, which is the disagreement crewstart was written
	// to end.
	cfg, err := buildCrewRuntimeConfig(ctx, h.db, crewID, workspaceID)
	if err != nil {
		replyInternalError(w, h.logger, "container start: resolve runtime config", err)
		return
	}

	// Notices carry degradation the start survived — a provider with no
	// sidecar support, for instance. Returned rather than logged: the
	// operator asked for this crew to be up, and "up, but without its
	// postgres" is something they have to be told to their face.
	var notices []string
	containerID, err := crewstart.New(h.container, NewCrewConfigCompleter(h.db), h.logger).
		StartNotify(ctx, cfg, func(n crewstart.Notice) {
			notices = append(notices, n.Message)
		})
	if err != nil {
		h.logger.Error("container start", "error", err, "crew_id", crewID, "slug", slug)
		replyError(w, http.StatusBadGateway, "starting the crew container failed: "+err.Error())
		return
	}

	// Verify, do not assert. EnsureCrewRuntime is get-or-create and
	// normally restarts a stopped container, but it can return the id of a
	// container that is NOT running — reproducibly so right after two
	// consecutive stops, where the crew ends `exited` while this handler
	// was about to answer `"status": "running"`.
	//
	// That answer is the entire promise of this endpoint. A caller reads
	// it and immediately writes files, which is a 409 against a stopped
	// crew; and a command that reports a state it never checked is the
	// exact defect `crew start` exists to fix in `crew provision`. So the
	// last thing before success is asking the runtime.
	if !h.containerRunning(ctx, containerID) {
		h.logger.Error("container start: not running after start",
			"crew_id", crewID, "slug", slug, "container_id", containerID)
		replyError(w, http.StatusBadGateway,
			"the crew container did not come up — it was created but is not running. Retry, and if it persists check the container runtime's logs")
		return
	}

	// Tell the idle-TTL reaper this container exists and start its clock.
	// Every other EnsureCrewRuntime caller gets this for free because an
	// agent run follows; this one has no run behind it, so without the
	// call the container it just created is tracked by nothing and lives
	// until crewshipd restarts (orchestrator_lifecycle.go, #1662).
	if h.activity != nil {
		h.activity.NoteCrewActivity(crewID, containerID, cfg.TTLHours)
	}

	body := map[string]any{
		"crew_id":      crewID,
		"slug":         slug,
		"container_id": containerID,
		"status":       "running",
	}
	if len(notices) > 0 {
		body["notices"] = notices
	}
	writeJSON(w, http.StatusOK, body)
}

// ContainerStop stops a crew's runtime container and its sidecars.
// POST /api/v1/crews/{crewId}/container-stop.
//
// The counterpart to ContainerStart, and the reason it exists: before
// it, a container could be started deliberately but only stopped by
// waiting for its idle TTL or by changing a network policy as a side
// effect. An operator who started three crews to land a restore had no
// way to give the memory back.
//
// Idempotent in the direction that matters: stopping an
// already-stopped crew succeeds. "The container is not running" is the
// state the caller asked for, so reporting it as an error would make
// every script bracket the call in a status check.
func (h *CrewHandler) ContainerStop(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	workspaceID := WorkspaceIDFromContext(ctx)

	if !canRole(RoleFromContext(ctx), "create") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	crewID := r.PathValue("crewId")
	if crewID == "" {
		replyError(w, http.StatusBadRequest, "crewId is required")
		return
	}

	var slug string
	err := h.db.QueryRowContext(ctx,
		"SELECT slug FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		crewID, workspaceID).Scan(&slug)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "Crew not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "container stop: crew lookup", err)
		return
	}

	if h.socketPath == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "no container runtime is configured on this server, so crews cannot be stopped here",
		})
		return
	}

	status, err := h.stopCrewContainerIPC(ctx, crewID)
	if err != nil {
		h.logger.Error("container stop", "error", err, "crew_id", crewID, "slug", slug)
		replyError(w, http.StatusBadGateway, "stopping the crew container failed: "+err.Error())
		return
	}
	if status != http.StatusOK {
		h.logger.Error("container stop rejected", "crew_id", crewID, "slug", slug, "status", status)
		replyError(w, http.StatusBadGateway,
			fmt.Sprintf("stopping the crew container failed (crewshipd answered %d)", status))
		return
	}

	// Drop the reaper's entry: the container it points at is gone, and
	// leaving it would have the reaper log an idle expiry for something a
	// human stopped. Symmetric with the NoteCrewActivity on start.
	if h.activity != nil {
		h.activity.ForgetCrewActivity(crewID)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"crew_id": crewID,
		"slug":    slug,
		"status":  "stopped",
	})
}

// containerRunning polls the runtime until the container reports
// running, briefly.
//
// Bounded and short: the states worth waiting through are the ones a
// container passes through in the first moment of its life ("creating"),
// not a build — the image is already there by the time this is called.
// Anything still not running after the window is a real failure, and
// saying so beats reporting a state nobody checked.
//
// A provider that cannot answer (nil status, no ContainerStatus support)
// is treated as running: this check exists to catch a container that is
// definitely down, and refusing to confirm is not evidence of that.
// Failing closed here would break every provider whose status probe is
// weaker than Docker's.
func (h *CrewHandler) containerRunning(ctx context.Context, containerID string) bool {
	const (
		attempts = 10
		wait     = 300 * time.Millisecond
	)
	for i := 0; i < attempts; i++ {
		st, err := h.container.ContainerStatus(ctx, containerID)
		if err != nil || st == nil {
			return true // cannot tell — see above
		}
		switch st.State {
		case "running", "idle":
			return true
		case "creating":
			// Still coming up; keep waiting.
		default:
			// "stopped" / "error" — a later poll can still see it come up
			// if a start is racing us, so do not bail on the first look.
		}
		if i < attempts-1 {
			select {
			case <-ctx.Done():
				return false
			case <-time.After(wait):
			}
		}
	}
	return false
}
