package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// devDefaultPassword is the fixed admin password used by `crewship seed` when
// the operator does not pass --password. It deliberately trades secrecy for
// developer ergonomics — the seed command is only intended for local dev and
// CI bootstrap, where a stable, memorable login for demo@crewship.ai is more
// useful than a random hex string nobody can recall. Production deployments
// MUST pass --password to override.
const devDefaultPassword = "password123"

var seedCmd = &cobra.Command{
	Use:   "seed",
	Short: "Seed demo data via the API (replaces prisma/seed.ts)",
	Long: `Creates a complete demo environment: admin user, workspace, crews,
agents with system prompts, credentials, integrations, and sample issues.

On a fresh database, automatically bootstraps the first admin user.
On an existing database, requires authentication (crewship login).

All data is created through the REST API, ensuring business logic
(validation, encryption, audit logging) is properly exercised.`,
	RunE: runSeed,
}

func init() {
	seedCmd.Flags().Bool("nuke", false, "Delete all workspace contents before seeding")
	seedCmd.Flags().Bool("skip-issues", false, "Skip issue/project/label seeding")
	seedCmd.Flags().String("password", "", "Admin password for bootstrap (defaults to devDefaultPassword)")
	seedCmd.Flags().Bool("smoke-test", false, "After seeding, send a test prompt to each agent to verify end-to-end")
	seedCmd.Flags().Int("smoke-timeout", 60, "Per-agent timeout (seconds) for smoke test")
	seedCmd.Flags().Int("provision-timeout", 900, "Per-crew provisioning timeout (seconds)")
	seedCmd.Flags().Bool("wait-provision", false, "Block until all crews finish provisioning (default: fire-and-forget, seed returns while provisioning runs in the background)")
}

func runSeed(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	nuke, _ := cmd.Flags().GetBool("nuke")
	skipIssues, _ := cmd.Flags().GetBool("skip-issues")
	password, _ := cmd.Flags().GetString("password")
	smokeTest, _ := cmd.Flags().GetBool("smoke-test")
	smokeTimeout, _ := cmd.Flags().GetInt("smoke-timeout")
	provisionTimeoutSec, _ := cmd.Flags().GetInt("provision-timeout")
	waitProvision, _ := cmd.Flags().GetBool("wait-provision")
	// Smoke test runs agents which require provisioned crews — force
	// synchronous wait even if the user didn't pass --wait-provision.
	if smokeTest {
		waitProvision = true
	}
	if password == "" {
		password = devDefaultPassword
		fmt.Fprintf(os.Stderr, "  Using dev default admin password: %s\n", password)
		fmt.Fprintln(os.Stderr, "  (override with --password for production)")
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	// ── Phase 1: Bootstrap / Auth ──
	client, userID, err := seedBootstrap(ctx, password)
	if err != nil {
		return err
	}

	// ── Phase 0: Nuke (after auth, before seed) ──
	if nuke {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := seedNuke(ctx, client); err != nil {
			return err
		}
	}

	// ── Phase 2: Crews + Member links ──
	if err := ctx.Err(); err != nil {
		return err
	}
	crewIDs, err := seedCrews(ctx, client, userID)
	if err != nil {
		return err
	}

	// ── Phase 2b: Provision crews with devcontainer config (parallel) ──
	// Without provisioning, `crewship run <agent>` fails with "Crew has
	// devcontainer configuration but no provisioned image". In default
	// (async) mode we fire triggers and let the server pull images / install
	// features in the background while the rest of the seed (agents, skills,
	// credentials, issues) runs. Users who need a runnable environment right
	// away pass --wait-provision (or --smoke-test, which implies it).
	if err := ctx.Err(); err != nil {
		return err
	}
	provisionTimeout := time.Duration(provisionTimeoutSec) * time.Second
	provisionTargets := collectProvisionTargets(crewIDs)
	startedTargets, triggerErr := triggerProvisions(ctx, client, provisionTargets, provisionTimeout)
	if triggerErr != nil && waitProvision {
		// In sync mode a trigger that never started is a hard fail — we
		// promised the caller a runnable env. In async mode we still
		// continue so the rest of the seed completes; the user sees the
		// "X <slug>" lines already printed.
		return triggerErr
	}

	// ── Phase 3: Agents ──
	if err := ctx.Err(); err != nil {
		return err
	}
	agentIDs, err := seedAgents(ctx, client, crewIDs)
	if err != nil {
		return err
	}

	// ── Phase 4–5: Skills + Assignments ──
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := seedSkills(ctx, client, agentIDs); err != nil {
		return err
	}

	// ── Phase 6–7: Credentials + Assignments ──
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := seedCredentials(ctx, client, agentIDs); err != nil {
		return err
	}

	// ── Phase 8–9: Integrations + Bindings ──
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := seedIntegrations(ctx, client, crewIDs, agentIDs); err != nil {
		return err
	}

	// ── Phase 10: Issues ──
	if !skipIssues {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := seedIssues(ctx, client, crewIDs, agentIDs); err != nil {
			return err
		}
	}

	// ── Phase 10b: Wait for background provisioning (only if requested) ──
	// Provisioning was triggered in Phase 2b; in async mode we skip the wait
	// entirely and tell the user how to check status. With --wait-provision
	// we block here so the other seed phases had a chance to run in parallel
	// with the image build.
	if waitProvision {
		// Poll only the targets whose triggers actually succeeded — polling
		// a crew whose trigger 429'd or errored would just time out idle.
		if err := waitForProvisions(ctx, client, startedTargets, provisionTimeout); err != nil {
			return err
		}
	} else if len(startedTargets) > 0 {
		// Report only the crews whose triggers actually landed. Failed triggers
		// were already logged with an "X <slug>" line by triggerProvisions, so
		// counting them here would lie to the user.
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintf(os.Stderr, "Provisioning %d crew(s) in the background.\n", len(startedTargets))
		fmt.Fprintln(os.Stderr, "  Agents in these crews become runnable once provisioning finishes (~few minutes).")
		fmt.Fprintln(os.Stderr, "  Status: crewship crew provision status <slug>   (or re-run `crewship seed --wait-provision`)")
	}

	// ── Phase 11: Summary ──
	fmt.Fprintln(os.Stderr, "")
	cli.PrintSuccess(fmt.Sprintf("Seed complete: %d crews, %d agents", len(crewIDs), len(agentIDs)))
	fmt.Fprintln(os.Stderr, "Login: demo@crewship.ai / "+password)

	// ── Phase 12: Smoke test (optional) ──
	if smokeTest {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := runSmokeTest(ctx, agentIDs, time.Duration(smokeTimeout)*time.Second); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "To test that agents work end-to-end:")
		fmt.Fprintln(os.Stderr, "  crewship seed --smoke-test")
	}
	return nil
}

// ════════════════════════════════════════════════════════════════════════════
// Phase 2b: Provisioning
// ════════════════════════════════════════════════════════════════════════════

// provisionTarget is one crew that needs devcontainer provisioning.
type provisionTarget struct {
	slug string
	id   string
}

// collectProvisionTargets returns the crews (from seeded data) that have a
// devcontainer_config and therefore need provisioning. Sorted by slug for
// deterministic logs.
func collectProvisionTargets(crewIDs map[string]string) []provisionTarget {
	hasDevcontainer := map[string]bool{}
	for _, c := range seeddata.Crews {
		if c.DevcontainerConfig != "" {
			hasDevcontainer[c.Slug] = true
		}
	}
	var targets []provisionTarget
	for slug, id := range crewIDs {
		if hasDevcontainer[slug] {
			targets = append(targets, provisionTarget{slug: slug, id: id})
		}
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].slug < targets[j].slug })
	return targets
}

// triggerProvisions fires the provision-start request for every target in
// parallel. It does NOT wait for builds to complete — the server handles the
// long-running work asynchronously. This is called early in the seed flow so
// the server can pull images / install features while the seed continues to
// create agents, skills, credentials, and issues via other endpoints.
//
// 429 rate-limit responses are retried with backoff so that a seed of N crews
// can cope with a server-side concurrency cap smaller than N. The per-target
// timeout bounds how long we're willing to keep retrying the trigger alone.
//
// Returns the subset of targets whose triggers actually succeeded and an
// aggregated error summarising the rest (nil if everything started). Callers
// should only poll the returned targets — polling a crew that never started
// would just time out on an idle status.
func triggerProvisions(ctx context.Context, client *cli.Client, targets []provisionTarget, timeout time.Duration) ([]provisionTarget, error) {
	if len(targets) == 0 {
		return nil, nil
	}
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Triggering crew provisioning (runs in background on the server)...")

	type result struct {
		t   provisionTarget
		err error
	}
	results := make(chan result, len(targets))
	var wg sync.WaitGroup
	for _, t := range targets {
		wg.Add(1)
		go func(t provisionTarget) {
			defer wg.Done()
			err := triggerProvisionOnce(ctx, client, t.id, timeout)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  X %s: trigger failed: %v\n", t.slug, err)
			} else {
				fmt.Fprintf(os.Stderr, "  + %s: provisioning started\n", t.slug)
			}
			results <- result{t: t, err: err}
		}(t)
	}
	wg.Wait()
	close(results)

	var started []provisionTarget
	var failedSlugs []string
	for r := range results {
		if r.err == nil {
			started = append(started, r.t)
			continue
		}
		failedSlugs = append(failedSlugs, r.t.slug)
	}
	sort.Slice(started, func(i, j int) bool { return started[i].slug < started[j].slug })
	sort.Strings(failedSlugs)

	if len(failedSlugs) == 0 {
		return started, nil
	}
	return started, fmt.Errorf("provisioning trigger failed for %d/%d crews: %s",
		len(failedSlugs), len(targets), strings.Join(failedSlugs, ", "))
}

// waitForProvisions polls each target's status until completion, failure, or
// timeout. Used only when the caller explicitly asked for sync behavior
// (--wait-provision or --smoke-test). Assumes triggerProvisions was already
// called — we only poll here, we don't re-trigger. Returns an aggregated
// error listing the slugs that failed; nil if everything completed.
func waitForProvisions(ctx context.Context, client *cli.Client, targets []provisionTarget, timeout time.Duration) error {
	if len(targets) == 0 {
		return nil
	}
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Waiting for crew provisioning to finish...")

	type result struct {
		slug string
		err  error
	}
	results := make(chan result, len(targets))
	var wg sync.WaitGroup
	for _, t := range targets {
		wg.Add(1)
		go func(t provisionTarget) {
			defer wg.Done()
			err := pollProvisionStatus(ctx, client, t.id, timeout)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  X %s: %v\n", t.slug, err)
			} else {
				fmt.Fprintf(os.Stderr, "  + %s provisioned\n", t.slug)
			}
			results <- result{slug: t.slug, err: err}
		}(t)
	}
	wg.Wait()
	close(results)

	var failedSlugs []string
	for r := range results {
		if r.err != nil {
			failedSlugs = append(failedSlugs, r.slug)
		}
	}
	sort.Strings(failedSlugs)

	if len(failedSlugs) == 0 {
		return nil
	}
	fmt.Fprintf(os.Stderr,
		"  WARNING: %d/%d crews failed to provision. Agents in those crews will not run.\n",
		len(failedSlugs), len(targets),
	)
	fmt.Fprintf(os.Stderr,
		"           Retry with: crewship crew provision <slug>\n")
	return fmt.Errorf("%d/%d crews failed to provision: %s",
		len(failedSlugs), len(targets), strings.Join(failedSlugs, ", "))
}

// triggerProvisionOnce POSTs the provision-start endpoint once and returns
// when the server has accepted the work. Handles 202/200/409 as success,
// retries 429 with exponential backoff (server caps concurrent provisions so
// firing >N triggers at once can 429 before all slots open). Any other status
// is a hard error. Does NOT poll for completion — that's pollProvisionStatus's
// job.
func triggerProvisionOnce(ctx context.Context, client *cli.Client, crewID string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	reqClient := client.WithContext(ctx)

	backoff := 3 * time.Second
	for {
		resp, err := reqClient.Post("/api/v1/crews/"+crewID+"/provision", nil)
		if err != nil {
			return fmt.Errorf("trigger provision: %w", err)
		}
		if resp.StatusCode == http.StatusAccepted ||
			resp.StatusCode == http.StatusOK ||
			resp.StatusCode == http.StatusConflict {
			resp.Body.Close()
			return nil
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			select {
			case <-ctx.Done():
				return fmt.Errorf("timeout waiting for provision slot")
			case <-time.After(backoff):
			}
			// Exponential backoff capped at 30s — slots usually free up in
			// tens of seconds once a peer provision completes. Doubling
			// before the cap check would overshoot (e.g. 24s → 48s stays
			// at 48s), so we compute the next value and clamp.
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return fmt.Errorf("trigger provision: HTTP %d: %s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}
}

// pollProvisionStatus polls the server status endpoint until the provision
// completes or fails. Call this only AFTER triggerProvisionOnce has returned
// nil for the same crewID — this function does not trigger work itself.
func pollProvisionStatus(ctx context.Context, client *cli.Client, crewID string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	pollClient := client.WithContext(ctx)

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	var lastStatus string
	for {
		st, err := fetchProvisionStatus(pollClient, crewID)
		if err != nil {
			return fmt.Errorf("poll status: %w", err)
		}
		lastStatus = st.Status
		switch st.Status {
		case "completed":
			return nil
		case "failed":
			if st.Error != "" {
				return fmt.Errorf("provision failed: %s", st.Error)
			}
			return fmt.Errorf("provision failed")
		}
		// pending / running / idle → keep polling
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout after %s (last status: %s)", timeout, lastStatus)
		case <-ticker.C:
		}
	}
}

// ════════════════════════════════════════════════════════════════════════════
// Phase 12: Smoke test
// ════════════════════════════════════════════════════════════════════════════

// smokeTestResult captures the outcome of a single agent smoke test.
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

func seedBootstrap(ctx context.Context, password string) (*cli.Client, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	fmt.Fprintln(os.Stderr, "Bootstrapping...")
	server := cli.ResolveServer(flagServer, cliCfg)

	// Try bootstrap (works only on empty DB). Bind ctx so Ctrl-C can
	// interrupt the in-flight HTTP call instead of blocking on the
	// 30 s client timeout.
	unauthClient := cli.NewClient(server, "", "").WithContext(ctx)
	resp, err := unauthClient.Post("/api/v1/bootstrap", map[string]string{
		"email":     "demo@crewship.ai",
		"full_name": "Demo User",
		"password":  password,
	})
	if err != nil {
		return nil, "", fmt.Errorf("bootstrap request failed (is the server running at %s?): %w", server, err)
	}

	if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusOK {
		// Fresh DB — extract token and workspace
		var result struct {
			UserID      string `json:"user_id"`
			WorkspaceID string `json:"workspace_id"`
			CLIToken    string `json:"cli_token"`
		}
		if err := cli.ReadJSON(resp, &result); err != nil {
			return nil, "", fmt.Errorf("read bootstrap response: %w", err)
		}

		// Save config for future commands
		cliCfg.Server = server
		cliCfg.Token = result.CLIToken
		cliCfg.Workspace = result.WorkspaceID
		if err := cli.SaveConfig(cliCfg); err != nil {
			cli.PrintWarning("could not save CLI config: " + err.Error())
		}

		fmt.Fprintf(os.Stderr, "  Bootstrapped admin: demo@crewship.ai\n")
		fmt.Fprintf(os.Stderr, "  Workspace: %s\n", result.WorkspaceID)
		return cli.NewClient(server, result.CLIToken, result.WorkspaceID).WithContext(ctx), result.UserID, nil
	}
	// Fall through to auth for 409 (already initialized) or any other status
	// whose body mentions the "already initialized" sentinel. Server currently
	// returns 403 with "Already initialized — …" for this case, so match
	// case-insensitively. Anything else is a real failure.
	if resp.StatusCode != http.StatusConflict {
		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		bodyStr := strings.TrimSpace(string(bodyBytes))
		if !strings.Contains(strings.ToLower(bodyStr), "already initialized") {
			return nil, "", fmt.Errorf("bootstrap failed: HTTP %d: %s", resp.StatusCode, bodyStr)
		}
	} else {
		resp.Body.Close()
	}

	// Already initialized — fall back to existing auth
	if err := requireAuth(); err != nil {
		return nil, "", fmt.Errorf("DB already initialized. %w", err)
	}
	if err := requireWorkspace(); err != nil {
		return nil, "", fmt.Errorf("DB already initialized. %w", err)
	}
	fmt.Fprintf(os.Stderr, "  Using existing auth\n")

	// Resolve user ID from CLI token validation
	client := newAPIClient().WithContext(ctx)
	userID := resolveCurrentUserID(client)
	return client, userID, nil
}

// resolveCurrentUserID gets the current user's ID from the CLI token validation endpoint.
func resolveCurrentUserID(client *cli.Client) string {
	resp, err := client.Get("/api/v1/auth/cli-token/validate")
	if err != nil {
		return ""
	}
	var info struct {
		UserID string `json:"user_id"`
	}
	if cli.ReadJSON(resp, &info) == nil {
		return info.UserID
	}
	return ""
}

// ════════════════════════════════════════════════════════════════════════════
// Helpers
// ════════════════════════════════════════════════════════════════════════════

// createOrResolve creates a resource via POST. On 409, resolves existing by slug.
func createOrResolve(client *cli.Client, createPath string, body interface{}, listPath, slug string) (string, error) {
	resp, err := client.Post(createPath, body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode == http.StatusConflict {
		resp.Body.Close()
		return resolveBySlug(client, listPath, slug)
	}
	if err := cli.CheckError(resp); err != nil {
		return "", err
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := cli.ReadJSON(resp, &created); err != nil {
		return "", err
	}
	return created.ID, nil
}

// resolveBySlug lists resources and finds one by slug.
func resolveBySlug(client *cli.Client, listPath, slug string) (string, error) {
	resp, err := client.Get(listPath)
	if err != nil {
		return "", err
	}
	var items []struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	}
	if err := cli.ReadJSON(resp, &items); err != nil {
		return "", err
	}
	for _, item := range items {
		if item.Slug == slug {
			return item.ID, nil
		}
	}
	return "", fmt.Errorf("resource with slug %q not found", slug)
}

// resolveByName lists resources and finds one by name.
func resolveByName(client *cli.Client, listPath, name string) (string, error) {
	resp, err := client.Get(listPath)
	if err != nil {
		return "", err
	}
	// Parse as raw JSON to handle both "name" and "slug" keys
	var raw []byte
	raw, err = readBody(resp)
	if err != nil {
		return "", err
	}
	var items []map[string]interface{}
	if err := json.Unmarshal(raw, &items); err != nil {
		return "", err
	}
	for _, item := range items {
		if n, ok := item["name"].(string); ok && n == name {
			if id, ok := item["id"].(string); ok {
				return id, nil
			}
		}
	}
	return "", fmt.Errorf("resource with name %q not found", name)
}

func readBody(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}
