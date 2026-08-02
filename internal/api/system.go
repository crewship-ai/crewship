package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/buildinfo"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/provider/apple"
	"github.com/crewship-ai/crewship/internal/provider/docker"
	"github.com/crewship-ai/crewship/internal/update"
)

// SystemHandler provides endpoints for system-level health and runtime detection.
type SystemHandler struct {
	logger  *slog.Logger
	version string
	build   buildinfo.Info
}

// NewSystemHandler creates a SystemHandler with the given logger and the
// current binary version (used by GET /api/v1/system/version to surface
// "update available" to the web UI).
//
// The build identity defaults to whatever this binary can work out about
// itself — for an ldflags-less `go build` (how dev.sh builds every dev slot)
// that is the VCS stamp, which is the only source that answers at all there.
// Wiring that has the ldflags values calls WithBuild to override.
func NewSystemHandler(logger *slog.Logger, version string) *SystemHandler {
	return &SystemHandler{
		logger:  logger,
		version: version,
		build:   buildinfo.Resolve(version, "", ""),
	}
}

// WithBuild replaces the resolved build identity — used by the router so the
// ldflags-injected commit and date from package main reach the endpoint
// (internal/api cannot reference package main's vars directly).
func (h *SystemHandler) WithBuild(b buildinfo.Info) *SystemHandler {
	h.build = b
	return h
}

var installLinks = map[string]string{
	"docker":   "https://docs.docker.com/get-docker/",
	"podman":   "https://podman.io/docs/installation",
	"colima":   "https://github.com/abiosoft/colima",
	"orbstack": "https://orbstack.dev/",
	"apple":    "https://github.com/apple/container",
}

// Runtime probes for a Docker-compatible container runtime and returns its status.
// GET /api/v1/system/runtime
// Accessible to any authenticated user (no workspace role required).
func (h *SystemHandler) Runtime(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// Build list of all available runtimes
	var runtimes []map[string]interface{}

	// Check Docker-compatible runtimes
	dockerResult, dockerErr := docker.Detect(ctx)
	if dockerErr == nil {
		runtimes = append(runtimes, map[string]interface{}{
			"runtime": dockerResult.Runtime,
			"version": dockerResult.Version,
			"socket":  dockerResult.Socket,
		})
	}

	// Check Apple Containers
	appleResult, appleErr := apple.Detect(ctx)
	if appleErr == nil {
		runtimes = append(runtimes, map[string]interface{}{
			"runtime": "apple",
			"version": appleResult.Version,
			"socket":  "",
		})
	}

	if len(runtimes) == 0 {
		h.logger.Debug("no container runtime found", "docker_error", dockerErr, "apple_error", appleErr)
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"available":     false,
			"runtime":       nil,
			"version":       nil,
			"socket":        nil,
			"runtimes":      []interface{}{},
			"install_links": installLinks,
		})
		return
	}

	// Redact host detail for non-admins (#865). Container versions and socket
	// paths are host infrastructure info; the runtime banner, onboarding, and
	// crew docker tab only need to know whether a runtime is available. ADMIN+
	// callers — resolved by OptionalWorkspaceRole when the request carries a
	// workspace_id (the admin console passes X-Workspace-ID) — get the full
	// detail. A role-less or below-ADMIN caller gets the bare availability flag
	// instead of a 403 so those non-admin surfaces keep working.
	if !canRole(RoleFromContext(r.Context()), "manage") {
		writeJSON(w, http.StatusOK, map[string]interface{}{"available": true})
		return
	}

	// Primary runtime is the first detected one
	primary := runtimes[0]
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"available": true,
		"runtime":   primary["runtime"],
		"version":   primary["version"],
		"socket":    primary["socket"],
		"runtimes":  runtimes,
	})
}

// Version reports the running binary's build identity plus the latest
// available release (cached on disk by internal/update — 24h on the stable
// channel, 1h on nightly, so per-request polling never exhausts GitHub's
// unauthenticated API quota). The web UI uses `current`/`latest`/`newer`/`url`
// to render a "Crewship vX.Y.Z available" banner; the CLI does its own check
// at boot.
//
// `commit` / `build_time` / `dirty` / `go_version` / `os` / `arch` /
// `schema_version` were added by #1645: this is the only way to ask a running
// server WHICH BUILD it is. `current` alone cannot answer that — every build a
// dev slot has ever made reports "dev", which is how dev1 sat a full day
// behind main with nothing able to say so. `dirty` is deliberately nullable:
// a build with ldflags but no VCS stamping does not know, and reporting that
// as `false` would be a confident wrong answer.
//
// Failures from the update package surface as `latest: null` so the client
// can render gracefully (no scary error UI for a transient GitHub outage).
// GET /api/v1/system/version
func (h *SystemHandler) Version(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	resp := map[string]interface{}{
		"current": h.version,
		"latest":  nil,
		"newer":   false,
		"url":     nil,

		"commit":     h.build.Commit,
		"build_time": h.build.BuildTime,
		"dirty":      h.build.Dirty,
		"go_version": h.build.GoVersion,
		"os":         h.build.OS,
		"arch":       h.build.Arch,
		// The schema this BINARY expects, not the one its DB happens to be
		// at. For a server that has booted the two are the same number:
		// database.Migrate applies every registered migration on start and
		// its skew guard refuses to run against anything higher. Reporting
		// the binary's ceiling keeps the endpoint free of a DB round-trip
		// and answers the question a remote CLI actually has — "is the
		// schema I know about the schema you're serving?".
		"schema_version": database.MaxKnownMigrationVersion(),
	}

	// 4s upper bound: the update.Check call itself has a 5s internal HTTP
	// timeout, but we want the API response to feel snappy. If the cache is
	// warm this returns instantly; if it's cold and GitHub is slow, we'd
	// rather respond with "no info" than block the UI render.
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
	defer cancel()

	result, err := update.Check(ctx, h.version)
	if err != nil {
		h.logger.Debug("system version check failed", "error", err)
		writeJSON(w, http.StatusOK, resp)
		return
	}
	if result == nil {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp["latest"] = result.Latest
	resp["newer"] = result.Newer
	resp["url"] = result.URL
	writeJSON(w, http.StatusOK, resp)
}
