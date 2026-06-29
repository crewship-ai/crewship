//go:build !clionly

package main

// Legacy C1 resource check for `crewship doctor`. The server's /healthz
// reports whether the docker daemon carries orphaned pre-C1 slug-only crew
// resources ("present") — volumes/containers left over from before the C1
// naming change that survive nuke+reseed and make every agent in the affected
// crew fail to start, surfaced to users only as "failed to start agent
// container". Surfacing it here gives operators a proactive WARN (and the
// remediation command) before an agent run hits the wall.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
)

// runCheckLegacyResources wires the production server URL into the testable
// helper below. Same wrapper/helper split as the other doctor probes.
func runCheckLegacyResources(ctx context.Context) checkResult {
	return checkLegacyResources(ctx, cli.ResolveServer(flagServer, cliCfg))
}

// checkLegacyResources GETs <serverURL>/healthz and inspects the
// "legacy_resources" field. Reachability problems are INFO, not FAIL — the
// dedicated "server reachable" check already reports a down daemon and doctor
// must not double-count the same root cause. A missing/empty field means a
// pre-feature server, a non-docker provider, or an indeterminate scan.
func checkLegacyResources(ctx context.Context, serverURL string) checkResult {
	const name = "legacy crew resources"

	url := strings.TrimRight(serverURL, "/") + "/healthz"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return checkResult{
			name:   name,
			status: "INFO",
			detail: fmt.Sprintf("invalid server URL %q: %v", serverURL, err),
		}
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return checkResult{
			name:   name,
			status: "INFO",
			detail: "server not reachable — skipped (see 'server reachable' check)",
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return checkResult{
			name:   name,
			status: "INFO",
			detail: fmt.Sprintf("/healthz returned HTTP %d — skipped", resp.StatusCode),
		}
	}

	var body struct {
		LegacyResources string `json:"legacy_resources"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return checkResult{
			name:   name,
			status: "INFO",
			detail: fmt.Sprintf("could not parse /healthz response: %v", err),
		}
	}

	switch body.LegacyResources {
	case "clean":
		return checkResult{
			name:   name,
			status: "PASS",
			detail: "no orphaned pre-C1 crew volumes/containers",
		}
	case "present":
		return checkResult{
			name:   name,
			status: "WARN",
			detail: "orphaned pre-C1 slug-only crew resources detected — agents in affected crews will fail to start",
			hint:   "run 'crewship admin prune-legacy' to remove them",
		}
	case "":
		return checkResult{
			name:   name,
			status: "INFO",
			detail: "server does not report legacy-resource status (older crewshipd or non-docker runtime)",
		}
	default:
		return checkResult{
			name:   name,
			status: "INFO",
			detail: fmt.Sprintf("unknown legacy-resource status %q reported by server", body.LegacyResources),
		}
	}
}
