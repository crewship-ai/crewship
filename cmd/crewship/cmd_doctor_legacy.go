//go:build !clionly

package main

// Legacy C1 resource check for `crewship doctor`. Reads the admin
// legacy-resources endpoint to report whether the docker daemon carries
// orphaned pre-C1 slug-only crew resources — volumes/containers left over from
// before the C1 naming change that survive nuke+reseed and make every agent in
// the affected crew fail to start, surfaced to users only as "failed to start
// agent container". Surfacing it here gives operators a proactive WARN (and the
// remediation command) before an agent run hits the wall.
//
// Unlike the episodic check this hits an AUTHENTICATED admin endpoint (the
// docker scan must not run on the unauthenticated /healthz hot path), so it is
// INFO-skipped when the CLI isn't logged in.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/crewship-ai/crewship/internal/cli"
)

// runCheckLegacyResources wires the production client into the testable helper.
func runCheckLegacyResources(ctx context.Context) checkResult {
	const name = "legacy crew resources"
	if cliCfg == nil || cliCfg.Token == "" {
		return checkResult{
			name:   name,
			status: "INFO",
			detail: "not logged in — run 'crewship login' to enable this check",
		}
	}
	return checkLegacyResources(ctx, newAPIClient())
}

// checkLegacyResources GETs /api/v1/admin/legacy-resources and maps the
// {present} response to a doctor result. Reachability / auth / non-docker
// conditions are INFO, not FAIL — the dedicated "server reachable" check
// already reports a down daemon and doctor must not double-count.
func checkLegacyResources(ctx context.Context, client *cli.Client) checkResult {
	const name = "legacy crew resources"

	resp, err := client.WithContext(ctx).Get("/api/v1/admin/legacy-resources")
	if err != nil {
		return checkResult{
			name:   name,
			status: "INFO",
			detail: "server not reachable — skipped (see 'server reachable' check)",
		}
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var body struct {
			Present bool `json:"present"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
			return checkResult{name: name, status: "INFO", detail: fmt.Sprintf("could not parse response: %v", err)}
		}
		if body.Present {
			return checkResult{
				name:   name,
				status: "WARN",
				detail: "orphaned pre-C1 slug-only crew resources detected — agents in affected crews will fail to start",
				hint:   "run 'crewship admin prune-legacy' to remove them",
			}
		}
		return checkResult{name: name, status: "PASS", detail: "no orphaned pre-C1 crew volumes/containers"}
	case http.StatusUnauthorized, http.StatusForbidden:
		return checkResult{name: name, status: "INFO", detail: "not authorized for this check — skipped"}
	case http.StatusServiceUnavailable:
		return checkResult{name: name, status: "INFO", detail: "server has no docker provider — skipped"}
	default:
		return checkResult{name: name, status: "INFO", detail: fmt.Sprintf("endpoint returned HTTP %d — skipped", resp.StatusCode)}
	}
}
