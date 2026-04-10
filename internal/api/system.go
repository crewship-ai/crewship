package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/provider/apple"
	"github.com/crewship-ai/crewship/internal/provider/docker"
)

// SystemHandler provides endpoints for system-level health and runtime detection.
type SystemHandler struct {
	logger *slog.Logger
}

// NewSystemHandler creates a SystemHandler with the given logger.
func NewSystemHandler(logger *slog.Logger) *SystemHandler {
	return &SystemHandler{logger: logger}
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
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
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
