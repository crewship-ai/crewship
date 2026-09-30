//go:build !linux && !clionly

package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"

	"github.com/crewship-ai/crewship/internal/api"
)

func startRestrictedTextRuntime(_ context.Context, _ *sql.DB, _ string, _ *api.Router, _ bool, _ *slog.Logger) (func(), error) {
	if os.Getenv("CREWSHIP_RESTRICTED_RUNTIME_IMAGE") != "" {
		return nil, fmt.Errorf("restricted text runtime requires Linux and Docker")
	}
	return func() {}, nil
}
