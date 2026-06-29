package main

// Smoke-test helpers that drive a hello-world agent run after seeding
// to prove the full stack works, plus the backup self-test that
// rides on the same infrastructure. Extracted from cmd_seed.go.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/cli"
)

func runBackupSelfTest(ctx context.Context, client *cli.Client, target provisionTarget, crewIDs map[string]string, agentIDs map[string]string) error {
	crewID, ok := crewIDs[target.slug]
	if !ok || crewID == "" {
		return fmt.Errorf("backup self-test: no crew id for slug %q", target.slug)
	}

	// Find one agent in the target crew to wake its container. Prefer the
	// LEAD for stable-ordering, fall back to any member.
	var warmupSlug string
	for _, a := range seeddata.Agents {
		if a.CrewSlug == target.slug {
			if a.AgentRole == "LEAD" {
				warmupSlug = a.Slug
				break
			}
			if warmupSlug == "" {
				warmupSlug = a.Slug
			}
		}
	}
	if warmupSlug == "" {
		return fmt.Errorf("backup self-test: no agent found in crew %q", target.slug)
	}
	if _, ok := agentIDs[warmupSlug]; !ok {
		return fmt.Errorf("backup self-test: warmup agent %q missing from seeded agents", warmupSlug)
	}

	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintf(os.Stderr, "Warming up %s container via %s...\n", target.slug, warmupSlug)
	if err := warmupAgentForBackupTest(ctx, warmupSlug); err != nil {
		return fmt.Errorf("backup self-test warmup: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Running backup self-test against %s...\n", target.slug)
	resp, err := client.WithContext(ctx).Post("/api/v1/admin/backups/self-test",
		map[string]string{"crew_id": crewID})
	if err != nil {
		return fmt.Errorf("backup self-test request: %w", err)
	}
	defer resp.Body.Close()
	// Check the HTTP status before decoding — on 4xx/5xx the server
	// returns {"error": ...} and decoding that into the success struct
	// would leave zero values and mask the real reason.
	if err := cli.CheckError(resp); err != nil {
		return fmt.Errorf("backup self-test request: %w", err)
	}

	var result struct {
		OK          bool   `json:"ok"`
		CrewSlug    string `json:"crew_slug"`
		BundleBytes int    `json:"bundle_bytes"`
		ElapsedMS   int64  `json:"elapsed_ms"`
		Error       string `json:"error,omitempty"`
	}
	if err := cli.ReadJSON(resp, &result); err != nil {
		return fmt.Errorf("backup self-test decode: %w", err)
	}
	if !result.OK {
		fmt.Fprintf(os.Stderr, "  X %s: %s\n", target.slug, result.Error)
		return fmt.Errorf("backup self-test failed on %s: %s", target.slug, result.Error)
	}
	fmt.Fprintf(os.Stderr, "  + %s: %d bytes, %dms\n", target.slug, result.BundleBytes, result.ElapsedMS)
	return nil
}

// ════════════════════════════════════════════════════════════════════════════
// Phase 2b: Provisioning
// ════════════════════════════════════════════════════════════════════════════

// provisionTarget is one crew that needs devcontainer provisioning.

type smokeTestResult struct {
	CrewSlug  string
	AgentSlug string
	OK        bool
	Timeout   bool
	Elapsed   time.Duration
	Output    string
	ErrMsg    string
}

// runSmokeTest sends a simple prompt to every agent that was seeded and reports
// per-agent success/failure. It execs the currently-running crewship binary as
// a subprocess (via `crewship run --no-stream --quiet`) so the smoke test
// exercises the real CLI path end-to-end rather than coupling to HTTP internals.

func runSmokeTest(ctx context.Context, agentIDs map[string]string, timeout time.Duration) error {
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Running smoke test (per-agent timeout: "+timeout.String()+")...")

	// Build slug → crew slug map from static seed data. Only test agents we
	// both seeded AND successfully created (present in agentIDs).
	crewBySlug := map[string]string{}
	for _, a := range seeddata.Agents {
		crewBySlug[a.Slug] = a.CrewSlug
	}

	// Stable ordering: crew slug, then agent slug.
	slugs := make([]string, 0, len(agentIDs))
	for slug := range agentIDs {
		slugs = append(slugs, slug)
	}
	sort.Slice(slugs, func(i, j int) bool {
		ci, cj := crewBySlug[slugs[i]], crewBySlug[slugs[j]]
		if ci != cj {
			return ci < cj
		}
		return slugs[i] < slugs[j]
	})

	server := cli.ResolveServer(flagServer, cliCfg)
	crewshipBin := os.Args[0]

	results := make([]smokeTestResult, 0, len(slugs))
	for _, slug := range slugs {
		if err := ctx.Err(); err != nil {
			return err
		}
		res := smokeTestAgent(ctx, crewshipBin, slug, crewBySlug[slug], server, timeout)
		results = append(results, res)
		printSmokeLine(res)
	}

	return printSmokeSummary(results)
}

// smokeTestAgent invokes `crewship run` as a subprocess and collects its
// stdout. Returns a result struct — errors are captured, not returned, so the
// caller can keep iterating through the remaining agents.
// warmupAgentForBackupTest runs one agent with a trivial prompt to force the
// orchestrator to start the crew container (`provisioned` only means the
// image exists; the container is created lazily on first run). Reuses the
// same subprocess pattern as the smoke test so we don't have to duplicate
// the HTTP dance the CLI already does.
//
// Seed hits the API hard enough that the auth-rate-limit (used for
// ws-token issuance on `crewship run`) can trip briefly. Retries a
// handful of times on 429 before giving up so we don't fail the whole
// --test-backup flow on transient rate-limiting.

func warmupAgentForBackupTest(ctx context.Context, agentSlug string) error {
	server := cli.ResolveServer(flagServer, cliCfg)
	backoff := 10 * time.Second
	for attempt := 1; attempt <= 4; attempt++ {
		ctx2, cancel := context.WithTimeout(ctx, 90*time.Second)
		args := []string{
			"run", agentSlug,
			"ready?",
			"--no-stream", "--quiet",
			"--timeout", "60",
			"--server", server,
		}
		cmd := exec.CommandContext(ctx2, os.Args[0], args...)
		out, err := cmd.CombinedOutput()
		cancel()
		if ctx2.Err() == context.DeadlineExceeded {
			return fmt.Errorf("warmup timed out after 90s (attempt %d)", attempt)
		}
		if err == nil {
			return nil
		}
		outStr := strings.TrimSpace(string(out))
		if (strings.Contains(outStr, "429") || strings.Contains(outStr, "Too Many Requests")) && attempt < 4 {
			fmt.Fprintf(os.Stderr, "  warmup 429 (attempt %d/4), waiting %s...\n", attempt, backoff)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		return fmt.Errorf("warmup exec: %w (output: %s)", err, outStr)
	}
	return fmt.Errorf("warmup exhausted retries")
}

func smokeTestAgent(ctx context.Context, crewshipBin, agentSlug, crewSlug, serverURL string, timeout time.Duration) smokeTestResult {
	start := time.Now()
	res := smokeTestResult{CrewSlug: crewSlug, AgentSlug: agentSlug}

	// Sub-context bounds the subprocess. When it expires the kernel delivers
	// SIGKILL via exec.CommandContext, so we can distinguish timeouts from
	// other failures by checking ctx2.Err() afterwards.
	ctx2, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{
		"run", agentSlug,
		"Hello, introduce yourself in one sentence.",
		"--no-stream", "--quiet",
		"--timeout", fmt.Sprintf("%d", int(timeout.Seconds())),
		"--server", serverURL,
	}
	cmd := exec.CommandContext(ctx2, crewshipBin, args...)
	out, err := cmd.CombinedOutput()
	res.Elapsed = time.Since(start)
	res.Output = strings.TrimSpace(string(out))

	if ctx2.Err() == context.DeadlineExceeded {
		res.Timeout = true
		res.ErrMsg = fmt.Sprintf("exceeded %s", timeout)
		return res
	}
	if err != nil {
		res.ErrMsg = err.Error()
		return res
	}
	if res.Output == "" {
		res.ErrMsg = "empty response"
		return res
	}
	res.OK = true
	return res
}

// printSmokeLine renders a single fixed-width result line.

func printSmokeLine(r smokeTestResult) {
	const (
		pathW   = 28
		statusW = 10
	)
	path := r.CrewSlug + "/" + r.AgentSlug
	if len(path) < pathW {
		path = path + strings.Repeat(" ", pathW-len(path))
	}

	var status, detail string
	switch {
	case r.OK:
		status = "OK"
		detail = fmt.Sprintf("(%.1fs)  %q", r.Elapsed.Seconds(), truncateForSmoke(r.Output, 80))
	case r.Timeout:
		status = "TIMEOUT"
		detail = "(" + r.ErrMsg + ")"
	default:
		status = "FAIL"
		detail = "(" + r.ErrMsg + ")"
		// The subprocess's captured output carries the REAL reason (e.g.
		// "agent error: failed to start agent container: <cause>"); ErrMsg
		// alone is just "exit status 1". Surface it so a failed smoke test is
		// self-diagnosing instead of forcing a re-run without --quiet.
		if out := truncateForSmoke(r.Output, 200); out != "" {
			detail += "  " + fmt.Sprintf("%q", out)
		}
	}
	if len(status) < statusW {
		status = status + strings.Repeat(" ", statusW-len(status))
	}
	fmt.Fprintf(os.Stderr, "  %s  %s  %s\n", path, status, detail)
}

// printSmokeSummary writes a passed/failed tally and returns an error if any
// agent failed so the process exits with a non-zero status.

func printSmokeSummary(results []smokeTestResult) error {
	passed, failed := 0, 0
	for _, r := range results {
		if r.OK {
			passed++
		} else {
			failed++
		}
	}
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintf(os.Stderr, "%d passed, %d failed, 0 skipped\n", passed, failed)
	if failed > 0 {
		return fmt.Errorf("smoke test: %d/%d agents failed", failed, len(results))
	}
	return nil
}

// truncateForSmoke collapses whitespace and hard-truncates at n runes for the
// one-line per-agent summary.

func truncateForSmoke(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-3]) + "..."
}

// ════════════════════════════════════════════════════════════════════════════
// Phase 1: Bootstrap
// ════════════════════════════════════════════════════════════════════════════
