package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/provider/apple"
	"github.com/crewship-ai/crewship/internal/provider/docker"
	"github.com/crewship-ai/crewship/internal/update"
)

// SystemHandler provides endpoints for system-level health and runtime detection.
type SystemHandler struct {
	logger  *slog.Logger
	version string
}

// NewSystemHandler creates a SystemHandler with the given logger and the
// current binary version (used by GET /api/v1/system/version to surface
// "update available" to the web UI).
func NewSystemHandler(logger *slog.Logger, version string) *SystemHandler {
	return &SystemHandler{logger: logger, version: version}
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

// Version reports the running binary's version plus the latest available
// release (cached 24h by internal/update). The web UI uses this to render a
// "Crewship vX.Y.Z available" banner; the CLI does its own check at boot.
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
