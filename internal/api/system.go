package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/provider/docker"
)

type SystemHandler struct {
	logger *slog.Logger
}

func NewSystemHandler(logger *slog.Logger) *SystemHandler {
	return &SystemHandler{logger: logger}
}

var installLinks = map[string]string{
	"docker":   "https://docs.docker.com/get-docker/",
	"podman":   "https://podman.io/docs/installation",
	"colima":   "https://github.com/abiosoft/colima",
	"orbstack": "https://orbstack.dev/",
}

// Runtime probes for a Docker-compatible container runtime and returns its status.
// GET /api/v1/system/runtime
func (h *SystemHandler) Runtime(w http.ResponseWriter, _ *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := docker.Detect(ctx)
	if err != nil {
		h.logger.Debug("container runtime not found", "error", err)
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"available":     false,
			"runtime":       nil,
			"version":       nil,
			"socket":        nil,
			"install_links": installLinks,
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"available": true,
		"runtime":   result.Runtime,
		"version":   result.Version,
		"socket":    result.Socket,
	})
}
