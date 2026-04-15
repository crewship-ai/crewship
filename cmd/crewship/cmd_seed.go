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
}

func runSeed(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	nuke, _ := cmd.Flags().GetBool("nuke")
	skipIssues, _ := cmd.Flags().GetBool("skip-issues")
	password, _ := cmd.Flags().GetString("password")
	smokeTest, _ := cmd.Flags().GetBool("smoke-test")
	smokeTimeout, _ := cmd.Flags().GetInt("smoke-timeout")
	provisionTimeoutSec, _ := cmd.Flags().GetInt("provision-timeout")
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
	// Without this, `crewship run <agent>` fails with "Crew has devcontainer
	// configuration but no provisioned image". Seed is supposed to produce a
	// demo environment that works out of the box.
	if err := ctx.Err(); err != nil {
		return err
	}
	provisionCrews(ctx, client, crewIDs, time.Duration(provisionTimeoutSec)*time.Second)

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

// provisionCrews triggers devcontainer provisioning for every crew that has a
// devcontainer_config and waits for all to finish (in parallel). Errors are
// reported but do not abort the whole seed — a partially-provisioned demo env
// is still better than bailing out before agents/skills/issues are created.
func provisionCrews(ctx context.Context, client *cli.Client, crewIDs map[string]string, timeout time.Duration) {
	// Map slug → has devcontainer config, from static seed data. If the seed
	// data evolves to skip devcontainers we silently skip those crews.
	hasDevcontainer := map[string]bool{}
	for _, c := range seeddata.Crews {
		if c.DevcontainerConfig != "" {
			hasDevcontainer[c.Slug] = true
		}
	}

	// Collect only crews we actually need to provision.
	type target struct{ slug, id string }
	var targets []target
	for slug, id := range crewIDs {
		if hasDevcontainer[slug] {
			targets = append(targets, target{slug: slug, id: id})
		}
	}
	if len(targets) == 0 {
		return
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].slug < targets[j].slug })

	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Provisioning demo crews (pulling images, installing features)...")

	var wg sync.WaitGroup
	errCh := make(chan error, len(targets))
	for _, t := range targets {
		wg.Add(1)
		go func(slug, id string) {
			defer wg.Done()
			fmt.Fprintf(os.Stderr, "  Provisioning %s...\n", slug)
			if err := provisionCrewAndWait(ctx, client, id, timeout); err != nil {
				fmt.Fprintf(os.Stderr, "  X %s: %v\n", slug, err)
				errCh <- fmt.Errorf("%s: %w", slug, err)
				return
			}
			fmt.Fprintf(os.Stderr, "  + %s provisioned\n", slug)
		}(t.slug, t.id)
	}
	wg.Wait()
	close(errCh)

	failed := 0
	for range errCh {
		failed++
	}
	if failed > 0 {
		fmt.Fprintf(os.Stderr,
			"  WARNING: %d/%d crews failed to provision. Agents in those crews will not run.\n",
			failed, len(targets),
		)
		fmt.Fprintf(os.Stderr,
			"           Retry with: crewship crew provision <slug>\n")
	}
}

// provisionCrewAndWait triggers provisioning for a crew and polls until
// completion, failure, or timeout. The trigger endpoint returns 202 Accepted
// (or 409 if a job is already running, which we treat as "fine, keep polling").
func provisionCrewAndWait(ctx context.Context, client *cli.Client, crewID string, timeout time.Duration) error {
	// Bound the whole operation with a sub-context so the poll loop terminates
	// cleanly on timeout / Ctrl-C.
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	pollClient := client.WithContext(ctx)

	// Kick off provisioning. 202 = started; 409 = already in progress (still OK
	// to poll); anything else is an error.
	resp, err := pollClient.Post("/api/v1/crews/"+crewID+"/provision", nil)
	if err != nil {
		return fmt.Errorf("trigger provision: %w", err)
	}
	if resp.StatusCode != http.StatusAccepted &&
		resp.StatusCode != http.StatusOK &&
		resp.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return fmt.Errorf("trigger provision: HTTP %d: %s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}
	resp.Body.Close()

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	var lastStatus string
	for {
		// Check status immediately (so a fast provision returns quickly) then
		// again on every tick.
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
	// Only fall through to auth for 409 (already initialized).
	// Other errors are real failures.
	if resp.StatusCode != http.StatusConflict {
		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		bodyStr := strings.TrimSpace(string(bodyBytes))
		if !strings.Contains(bodyStr, "already initialized") {
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
