//go:build linux && !clionly

package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/restricteddispatch"
	"github.com/crewship-ai/crewship/internal/restrictedruntime"
)

var restrictedImageDigest = regexp.MustCompile(`^(sha256:[a-f0-9]{64}|[a-z0-9][a-z0-9._:/-]*@sha256:[a-f0-9]{64})$`)

// A restricted worker is an operator-installed, immutable local image. The
// manager never pulls an image or accepts runtime configuration from a request.
// Without this explicit opt-in, restricted execution remains unavailable.
func startRestrictedTextRuntime(ctx context.Context, db *sql.DB, databasePath string, router *api.Router, noDocker bool, logger *slog.Logger) (func(), error) {
	image := os.Getenv("CREWSHIP_RESTRICTED_RUNTIME_IMAGE")
	if image == "" {
		return func() {}, nil
	}
	if noDocker || router == nil || db == nil || !restrictedImageDigest.MatchString(image) {
		return nil, fmt.Errorf("restricted runtime requires Docker, an API router and an immutable image digest")
	}
	absoluteDB, err := filepath.Abs(databasePath)
	if err != nil || databasePath == "" || databasePath == ":memory:" {
		return nil, fmt.Errorf("restricted runtime requires a persistent instance database")
	}
	authority := restricteddispatch.Authority{Store: access.Store{DB: db}}
	manager, err := restrictedruntime.New(filepath.Join(filepath.Dir(absoluteDB), "restricted-runtime"), restrictedruntime.Docker{Image: image}, authority, authority,
		restrictedruntime.Limits{MemoryBytes: 128 << 20, NanoCPUs: 500000000, PIDs: 32})
	if err != nil {
		return nil, err
	}
	if err = manager.Reconcile(ctx); err != nil {
		_ = manager.Close()
		return nil, fmt.Errorf("reconcile restricted runtime: %w", err)
	}
	router.SetRestrictedTextRunner(&restricteddispatch.TextRunner{Authority: authority, Manager: manager, MaxOutputTokens: 4096})
	logger.Info("restricted text runtime enabled", "image", image)
	return func() {
		if err := manager.Close(); err != nil {
			logger.Error("restricted runtime shutdown failed", "error", err)
		}
	}, nil
}
