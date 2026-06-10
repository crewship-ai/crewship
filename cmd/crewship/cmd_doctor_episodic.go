//go:build !clionly

package main

// Episodic recall mode check for `crewship doctor` (W2, release-1.0
// hardening). The server's /healthz reports whether episodic recall is
// running with a vector embedder ("vector") or degraded to keyword/FTS
// only ("sparse-only" — no embedder configured). Surfacing it in doctor
// gives operators a place to discover the degradation that previously
// only manifested as silently empty recall injections.

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

// runCheckEpisodicRecallMode wires the production server URL into the
// testable helper below. Same wrapper/helper split as the other doctor
// probes — the helper takes the URL as a parameter so unit tests drive
// every branch against an httptest server.
func runCheckEpisodicRecallMode(ctx context.Context) checkResult {
	return checkEpisodicRecallMode(ctx, cli.ResolveServer(flagServer, cliCfg))
}

// checkEpisodicRecallMode GETs <serverURL>/healthz and inspects the
// "episodic" field. Reachability problems are INFO, not FAIL — the
// dedicated "server reachable" check already reports a down daemon and
// doctor must not double-count the same root cause. A missing field
// means a pre-1.0 server that doesn't report the mode yet.
func checkEpisodicRecallMode(ctx context.Context, serverURL string) checkResult {
	const name = "episodic recall mode"

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
		Episodic string `json:"episodic"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return checkResult{
			name:   name,
			status: "INFO",
			detail: fmt.Sprintf("could not parse /healthz response: %v", err),
		}
	}

	switch body.Episodic {
	case "vector":
		return checkResult{
			name:   name,
			status: "PASS",
			detail: "vector + sparse recall (embedder configured, indexer running)",
		}
	case "sparse-only":
		return checkResult{
			name:   name,
			status: "WARN",
			detail: "sparse-only — no embedder configured, vector recall disabled",
			hint:   "set KEEPER_OLLAMA_URL to an Ollama host serving nomic-embed-text to enable vector recall",
		}
	case "":
		return checkResult{
			name:   name,
			status: "INFO",
			detail: "server does not report episodic mode (older crewshipd)",
		}
	default:
		return checkResult{
			name:   name,
			status: "INFO",
			detail: fmt.Sprintf("unknown episodic mode %q reported by server", body.Episodic),
		}
	}
}
