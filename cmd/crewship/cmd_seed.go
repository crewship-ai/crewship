package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

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
	seedCmd.Flags().Bool("test-backup", false, "After seeding, run a backup/restore round-trip self-test on one crew (implies --wait-provision)")
	seedCmd.Flags().Bool("with-memory", false, "Pre-seed agent memory tiers (AGENT.md / CREW.md / PERSONA.md / pins.md / daily/{date}.md / learned.md) for the demo workspace; useful for memory-recall demos and live GDPR/RBAC tests")
	seedCmd.Flags().Bool("with-users", false, "Add four extra users (ADMIN, MANAGER, MEMBER, VIEWER) to the workspace for RBAC matrix testing; requires CREWSHIP_ALLOW_SIGNUP=true on the server")
}

// loadDotEnvLocal seeds os.Getenv with values from .env.local in the
// current working directory, but ONLY for keys that aren't already in
// the process environment. This makes `crewship seed` work the same
// way whether invoked via `dev.sh seed` (which exports the file) or
// directly as `crewship seed` from a fresh shell, where SEED_*
// (provider keys), CREWSHIP_PORT (instance offset), and
// CREWSHIP_STORAGE_BASE_PATH (where the daemon keeps crew
// filesystems) would otherwise be empty — leaving credentials marked
// EXPIRED at sidecar validation time, the CLI hitting the wrong port,
// or memory files landing in ~/.crewship/ where the daemon can't see
// them.
//
// We deliberately do NOT overwrite existing env values — operators
// who run `SEED_ANTHROPIC_API_KEY=… crewship seed` get to override
// whatever's in .env.local without editing the file.
//
// After file parsing, derive CREWSHIP_SERVER from CREWSHIP_PORT
// (multi-instance dev.sh puts each instance on 8080+N). Without this,
// the CLI default `--server http://localhost:8080` lands on whichever
// instance happens to be on the base port — almost never the one the
// operator just configured — and bootstrap loops on a 403 that looks
// like "your token is wrong" but is really "wrong server."
//
// Missing file is non-fatal: returns silently, seed continues with
// whatever env is set. Malformed lines (no `=`) are skipped.
func loadDotEnvLocal() {
	f, err := os.Open(".env.local")
	if err != nil {
		// no file — still try CREWSHIP_PORT → CREWSHIP_SERVER bridge
		bridgeServerFromPort()
		return
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		// strip optional surrounding quotes (single or double)
		if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
			val = val[1 : len(val)-1]
		}
		if _, alreadySet := os.LookupEnv(key); alreadySet {
			continue
		}
		_ = os.Setenv(key, val)
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed reading .env.local: %v\n", err)
	}
	bridgeServerFromPort()
}

// bridgeServerFromPort fills in CREWSHIP_SERVER from CREWSHIP_PORT
// when the operator only set the port (the common multi-instance dev
// pattern). This keeps the CLI honest about which daemon to talk to
// instead of silently defaulting to :8080.
func bridgeServerFromPort() {
	if os.Getenv("CREWSHIP_SERVER") != "" {
		return
	}
	port := os.Getenv("CREWSHIP_PORT")
	if port == "" {
		return
	}
	_ = os.Setenv("CREWSHIP_SERVER", "http://127.0.0.1:"+port)
}

func runSeed(cmd *cobra.Command, args []string) error {
	loadDotEnvLocal()
	ctx := cmd.Context()
	nuke, _ := cmd.Flags().GetBool("nuke")
	skipIssues, _ := cmd.Flags().GetBool("skip-issues")
	password, _ := cmd.Flags().GetString("password")
	smokeTest, _ := cmd.Flags().GetBool("smoke-test")
	smokeTimeout, _ := cmd.Flags().GetInt("smoke-timeout")
	provisionTimeoutSec, _ := cmd.Flags().GetInt("provision-timeout")
	waitProvision, _ := cmd.Flags().GetBool("wait-provision")
	testBackup, _ := cmd.Flags().GetBool("test-backup")
	withMemory, _ := cmd.Flags().GetBool("with-memory")
	withUsers, _ := cmd.Flags().GetBool("with-users")
	// Smoke test + test-backup both need a provisioned, running container
	// to do anything useful, so they implicitly force --wait-provision.
	if smokeTest || testBackup {
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

	// ── Phase 1b: RBAC fixture users (optional) ──
	// Runs BEFORE crews so the seeded admin/manager/member/viewer
	// exist as workspace members before crew creation lands; that way
	// crew-detail panels and member queries already show the full
	// roster when the operator first opens the UI.
	if withUsers {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := seedRBACUsers(ctx, client); err != nil {
			fmt.Fprintf(os.Stderr, "RBAC user seeding hit an error (continuing): %v\n", err)
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

	// ── Phase 2a: Connect crews (all-pairs, bidirectional) ──
	// Mission planner can dispatch tasks across crew boundaries and
	// the orchestrator blocks the hand-off unless a crew_connections
	// row authorises it. Wired here so the LEAD can delegate to any
	// other crew on the demo workspace without the operator having
	// to click through the connections UI first. Non-fatal — a
	// missing connection produces a clear error at dispatch, so a
	// partial failure here is recoverable manually.
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := seedCrewConnections(ctx, client, crewIDs); err != nil {
		fmt.Fprintf(os.Stderr, "Crew connection seeding hit an error (continuing): %v\n", err)
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
	// Deliberately do NOT early-return on triggerErr, even in sync mode:
	// the async design of Phase 2b is "trigger, continue seeding while
	// images build". Returning here would also skip agents/skills/issues.
	// We remember the error and surface it at the end joined with
	// whatever waitForProvisions produces.

	// ── Phase 3: Agents ──
	if err := ctx.Err(); err != nil {
		return err
	}
	agentIDs, err := seedAgents(ctx, client, crewIDs)
	if err != nil {
		return err
	}

	// ── Phase 3b: Agent memory tiers (optional) ──
	// Runs AFTER agents are created so we can resolve each agent's
	// crew_id from the API. Failure here is non-fatal — the rest of
	// the seed still produces a usable workspace; agents just won't
	// have boot-time memory context until they write some.
	if withMemory {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := seedAgentMemory(ctx, client, crewIDs); err != nil {
			fmt.Fprintf(os.Stderr, "Memory seeding hit an error (continuing): %v\n", err)
		}
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

	// ── Phase 9b: Routines (5 starter recipes) ──
	// Runs BEFORE issues because routines depend only on crews and
	// the issues phase can hit pre-existing 5xx on label/project
	// re-creation in non-nuke seeds, aborting the entire seed before
	// routines would land. Routines failure is non-fatal — a missing
	// crew or DSL parse error logs but doesn't torpedo subsequent
	// seed phases.
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := seedRoutines(ctx, client, crewIDs); err != nil {
		fmt.Fprintf(os.Stderr, "Routine seeding hit an error (continuing): %v\n", err)
	}

	// ── Phase 9b: Demo schedules ──
	// Routines exist now; wire one demo cron so a fresh /activity has
	// something flowing into it without the user having to compose
	// a schedule from the UI. Failure is non-fatal.
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := seedSchedules(ctx, client); err != nil {
		fmt.Fprintf(os.Stderr, "Schedule seeding hit an error (continuing): %v\n", err)
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
	var waitErr error
	if waitProvision {
		// Poll only the targets whose triggers actually succeeded — polling
		// a crew whose trigger 429'd or errored would just time out idle.
		// Save the error and fall through; we'll combine it with any
		// triggerErr from Phase 2b after the optional self-test runs.
		waitErr = waitForProvisions(ctx, client, startedTargets, provisionTimeout)
	} else if len(startedTargets) > 0 {
		// Report only the crews whose triggers actually landed. Failed triggers
		// were already logged with an "X <slug>" line by triggerProvisions, so
		// counting them here would lie to the user.
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintf(os.Stderr, "Provisioning %d crew(s) in the background.\n", len(startedTargets))
		fmt.Fprintln(os.Stderr, "  Agents in these crews become runnable once provisioning finishes (~few minutes).")
		fmt.Fprintln(os.Stderr, "  Status: crewship crew provision status <slug>   (or re-run `crewship seed --wait-provision`)")
	}

	// ── Phase 10c: Backup self-test (optional) ──
	// Requires a provisioned crew (--wait-provision forced above). We pick
	// the first deterministic target — testing all four would be 4× slower
	// for no extra coverage; one roundtrip exercises the full pipeline.
	if testBackup {
		if err := ctx.Err(); err != nil {
			return err
		}
		// A started target only means the trigger was accepted. If the
		// wait phase reported a failure, don't proceed — the self-test
		// would then fail on warmup or ContainerExists and mask the
		// provisioning error behind a less useful message. Surface the
		// provisioning failure directly.
		if waitErr != nil {
			return errors.Join(triggerErr, waitErr)
		}
		if len(startedTargets) == 0 {
			// The operator asked for a self-test; silently skipping and
			// exiting 0 would let CI and scripts treat a broken
			// provisioning step as green.
			return fmt.Errorf("--test-backup requested, but no crew successfully started provisioning")
		}
		// Prefer "research" — it's the Python crew with a minimal feature
		// set, so the bundle stays small and the collector/restorer
		// don't hit quirky installs (e.g. terraform on devops creates
		// version-alias symlinks the manifest validator rejects, a
		// pre-existing issue unrelated to this self-test).
		target := startedTargets[0]
		for _, t := range startedTargets {
			if t.slug == "research" {
				target = t
				break
			}
		}
		if err := runBackupSelfTest(ctx, client, target, crewIDs, agentIDs); err != nil {
			return err
		}
	}

	// ── Phase 11: Summary + deferred provisioning errors ──
	// Surface whatever triggerProvisions or waitForProvisions reported
	// now that the rest of the seed has completed. In async mode triggerErr
	// is reported but non-fatal (matches the existing async semantics);
	// in sync mode both are returned so a caller exits non-zero.
	if waitProvision {
		if err := errors.Join(triggerErr, waitErr); err != nil {
			return err
		}
	} else if triggerErr != nil {
		fmt.Fprintf(os.Stderr, "\nNote: provisioning trigger reported errors (continuing): %v\n", triggerErr)
	}

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

// runBackupSelfTest invokes the admin self-test endpoint for the chosen
// crew and prints a one-line verdict. Returns a non-nil error when the
// pipeline itself failed or when the canary round-trip didn't verify —
// mirrors runSmokeTest's "fail the command on a red test" behaviour so
// `crewship seed --test-backup` is CI-usable.
//
// "Provisioned" means the image is cached; the crew *container* is only
// instantiated on the first agent run. We trigger one short agent run as
// a warm-up so the server endpoint finds a running container to pause.

func seedBootstrap(ctx context.Context, password string) (*cli.Client, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	fmt.Fprintln(os.Stderr, "Bootstrapping...")
	server := cli.ResolveServer(flagServer, cliCfg)

	// Try bootstrap (works only on empty DB). Bind ctx so Ctrl-C can
	// interrupt the in-flight HTTP call instead of blocking on the
	// 30 s client timeout.
	//
	// Fresh deployments arm a one-shot setup token at boot (GitLab
	// `initial_root_password` convention). The token lives at
	// <data_dir>/initial_setup_token with a comment header — read
	// it and forward in X-Setup-Token so the bootstrap call doesn't
	// bounce off the 403 gate.
	setupToken := readSetupTokenFile()
	if setupToken != "" {
		fmt.Fprintln(os.Stderr, "  Found initial_setup_token — sending as X-Setup-Token header")
	}
	resp, err := postBootstrap(ctx, server, setupToken, map[string]string{
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
		// Fall through to auth for two server-side messages:
		//   "already initialized" — admin row exists, normal post-bootstrap state
		//   "bootstrap closed"    — setup token was armed but already consumed
		// Both indicate the operator should already hold a CLI token (saved by
		// the earlier bootstrap that consumed the token); requireAuth picks
		// it up. Without the second match, a re-run of `crewship seed` after
		// a successful initial bootstrap dies on this exact message.
		lower := strings.ToLower(bodyStr)
		if !strings.Contains(lower, "already initialized") && !strings.Contains(lower, "bootstrap closed") {
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

// readSetupTokenFile returns the bootstrap token written by the daemon
// at first start (when the users table is empty). Returns "" when the
// file doesn't exist (already-bootstrapped state) or has no value
// line. The file format is:
//
//	# crewship: one-shot bootstrap token — DO NOT COMMIT, DO NOT SHARE
//	# Use ONCE as X-Setup-Token header on POST /api/v1/bootstrap.
//	# Auto-deleted on first successful bootstrap.
//	#
//	<64-hex-char token>
//
// We honour CREWSHIP_STORAGE_BASE_PATH (set by dev.sh per-instance) so
// the seed CLI finds the token even when invoked alongside an unusual
// data dir; falls back to ~/.crewship for one-host deployments.
func readSetupTokenFile() string {
	candidates := []string{}
	if base := os.Getenv("CREWSHIP_STORAGE_BASE_PATH"); base != "" {
		candidates = append(candidates, base+"/initial_setup_token")
	}
	if h, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, h+"/.crewship/initial_setup_token")
	}
	for _, p := range candidates {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		defer f.Close()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			return line
		}
		if err := scanner.Err(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed reading %s: %v\n", p, err)
		}
	}
	return ""
}

// postBootstrap issues the bootstrap POST with the optional X-Setup-
// Token header. Returns the raw *http.Response so the caller can
// inspect StatusCode + body the same way it did before this helper.
//
// We don't go through cli.Client because that struct doesn't expose
// a "headers" hook and bootstrap is the one path where we need to
// attach a one-shot operational header before any auth flow.
func postBootstrap(ctx context.Context, server, setupToken string, body map[string]string) (*http.Response, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", server+"/api/v1/bootstrap", bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if setupToken != "" {
		req.Header.Set("X-Setup-Token", setupToken)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	return client.Do(req)
}
