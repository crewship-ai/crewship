package main

// `crewship seed verify` — the acceptance test for the demo packs, run
// against a live workspace after `crewship seed`.
//
// A pack is only worth seeding if it can be run again tomorrow and CHECKED,
// not just admired. So for every pack this command:
//
//   1. confirms the crew exists and the delivered scripts are byte-identical
//      to the ones in the binary (apply-style delivery reports success even
//      when a file could not be overwritten — see crewship-manifests README);
//   2. runs the token-zero probe and compares its verdict with an independent
//      read of the same source (GitHub's Actions API, from here, not from the
//      container);
//   3. runs the report routine and checks the agent's `COUNTS:` line against
//      the probe's own numbers — the one place where "the agent wrote a
//      report" turns into a claim that can be false;
//   4. checks the notification landed in the inbox and the Page panels carry
//      this run's id.
//
// A pack whose requirement is unmet (no SEED_GITHUB_TOKEN) is reported as
// SKIP with the reason and never as green; --strict turns a skip into a
// failure for the nightly run that must not quietly pass.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/cli"
)

var seedVerifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Run every demo pack end to end and check the agents' output against the deterministic probes",
	Long: `Verifies the demo packs a seed installed, against the live workspace.

For every pack (or the ones named with --pack) it checks the crew and the
delivered scripts, runs the token-zero probe and compares it with an
independent read of the same public source, runs the report routine and
checks the agent's COUNTS line against the probe, and confirms the inbox
notification and the Page panels were written by that run.

A pack whose requirement is missing (for example SEED_GITHUB_TOKEN for the
GitHub-backed packs) is reported as SKIP with the reason. Exit status is 1
when any check FAILs; with --strict a SKIP fails too.

Examples:
  crewship seed verify
  crewship seed verify --pack ci-watch --timeout 20m
  crewship seed verify --format json --strict`,
	Args: cobra.NoArgs,
	RunE: runSeedVerify,
}

func init() {
	seedVerifyCmd.Flags().StringSlice("pack", nil, "Only verify these pack slugs (repeatable; default: every pack)")
	seedVerifyCmd.Flags().Duration("timeout", 30*time.Minute, "Maximum wait per routine run")
	seedVerifyCmd.Flags().Bool("strict", false, "Treat a skipped pack as a failure")
	seedVerifyCmd.Flags().Bool("skip-report", false, "Check files and probes only; do not run the agent report routines")
	seedVerifyCmd.Flags().String("repo-dir", "", "Local checkout of the audited repository, used to fact-check the docs-drift findings (default: the current directory when it is a git repository)")
	seedCmd.AddCommand(seedVerifyCmd)
}

// verifyCheck is one row of the verdict table.
type verifyCheck struct {
	Pack   string `json:"pack"`
	Step   string `json:"step"`
	Result string `json:"result"` // PASS | FAIL | SKIP
	Detail string `json:"detail,omitempty"`
}

const (
	verifyPass = "PASS"
	verifyFail = "FAIL"
	verifySkip = "SKIP"
)

// verifyGitHubAPI is the base URL the independent ground-truth read uses.
// Overridable through CREWSHIP_SEED_VERIFY_GITHUB_API so the acceptance test
// can point the built binary at a stub.
func verifyGitHubAPI() string {
	if v := strings.TrimSpace(os.Getenv("CREWSHIP_SEED_VERIFY_GITHUB_API")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://api.github.com"
}

type verifyOptions struct {
	packs      []string
	timeout    time.Duration
	strict     bool
	skipReport bool
	repoDir    string
	now        func() time.Time
}

func runSeedVerify(cmd *cobra.Command, _ []string) error {
	loadDotEnvLocal()
	client, err := requireAuthAndWorkspace()
	if err != nil {
		return err
	}
	opts := verifyOptions{now: time.Now}
	opts.packs, _ = cmd.Flags().GetStringSlice("pack")
	opts.timeout, _ = cmd.Flags().GetDuration("timeout")
	opts.strict, _ = cmd.Flags().GetBool("strict")
	opts.skipReport, _ = cmd.Flags().GetBool("skip-report")
	opts.repoDir, _ = cmd.Flags().GetString("repo-dir")

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	checks, err := seedVerify(ctx, client, opts)
	if err != nil {
		return err
	}
	return printVerify(checks, opts.strict)
}

// seedVerify runs every selected pack and returns the verdict rows. It
// returns an error only when it could not even start — a failed check is a
// row, not an error, so one pack's failure never hides another's result.
func seedVerify(ctx context.Context, client *cli.Client, opts verifyOptions) ([]verifyCheck, error) {
	wsID := client.GetWorkspaceID()
	if wsID == "" {
		return nil, fmt.Errorf("seed verify: workspace_id not set on client")
	}
	if opts.now == nil {
		opts.now = time.Now
	}
	selected, err := selectPacks(opts.packs)
	if err != nil {
		return nil, err
	}
	crewIDs, err := verifyListCrews(client)
	if err != nil {
		return nil, fmt.Errorf("list crews: %w", err)
	}

	var checks []verifyCheck
	for _, p := range selected {
		v := &packVerifier{client: client, wsID: wsID, pack: p, opts: opts, crewIDs: crewIDs}
		checks = append(checks, v.run(ctx)...)
	}
	return checks, nil
}

func selectPacks(want []string) ([]seeddata.PackDef, error) {
	if len(want) == 0 {
		return seeddata.Packs, nil
	}
	var out []seeddata.PackDef
	for _, slug := range want {
		p, ok := seeddata.PackBySlug(strings.TrimSpace(slug))
		if !ok {
			return nil, fmt.Errorf("unknown pack %q (known: %s)", slug, strings.Join(packSlugs(), ", "))
		}
		out = append(out, p)
	}
	return out, nil
}

func packSlugs() []string {
	out := make([]string, 0, len(seeddata.Packs))
	for _, p := range seeddata.Packs {
		out = append(out, p.Slug)
	}
	return out
}

// packVerifier holds the state of one pack's verification.
type packVerifier struct {
	client  *cli.Client
	wsID    string
	pack    seeddata.PackDef
	opts    verifyOptions
	crewIDs map[string]string
	checks  []verifyCheck
	started time.Time
	// probe holds the last probe JSON seen (from the probe run, or the
	// report run's own probe step), for the COUNTS reconciliation.
	probe map[string]any
}

func (v *packVerifier) add(step, result, detail string) {
	v.checks = append(v.checks, verifyCheck{Pack: v.pack.Slug, Step: step, Result: result, Detail: detail})
	marker := map[string]string{verifyPass: "✓", verifyFail: "✗", verifySkip: "–"}[result]
	fmt.Fprintf(os.Stderr, "  %s %-12s %-10s %s\n", marker, v.pack.Slug, step, detail)
}

func (v *packVerifier) run(ctx context.Context) []verifyCheck {
	v.started = v.opts.now()
	fmt.Fprintf(os.Stderr, "Verifying pack %s (%s)...\n", v.pack.Slug, v.pack.Name)

	crewID, ok := v.crewIDs[v.pack.CrewSlug]
	if !ok {
		v.add("crew", verifyFail, fmt.Sprintf("crew %q not found — run `crewship seed` first", v.pack.CrewSlug))
		return v.checks
	}
	v.add("crew", verifyPass, v.pack.CrewSlug+" = "+crewID)

	v.verifyFiles(ctx, crewID)

	if runnable, reason := packRunnable(v.pack); !runnable {
		v.add("env", verifySkip, reason+" — the pack is seeded but cannot run")
		return v.checks
	}
	v.add("env", verifyPass, "requirements present")

	if v.pack.ProbeSlug != "" {
		if !v.verifyProbe(ctx) {
			// A wrong probe makes every later number meaningless; stop here
			// and say so rather than fail four more rows for one cause.
			return v.checks
		}
	}
	if v.opts.skipReport {
		v.add("report", verifySkip, "--skip-report")
		return v.checks
	}
	run, ok := v.verifyReport(ctx)
	if !ok || run == nil {
		return v.checks
	}
	v.verifyInbox(run)
	v.verifyPage(run)
	return v.checks
}

// verifyFiles compares every delivered pack file with the embedded original.
// "Saved" from a seed is not proof: a tree owned by the crew runtime can
// refuse the overwrite and the apply path reports success on the last line.
func (v *packVerifier) verifyFiles(ctx context.Context, crewID string) {
	drift, missing := 0, 0
	for _, f := range v.pack.Files {
		want, err := seeddata.PackFileContent(f.Src)
		if err != nil {
			v.add("files", verifyFail, err.Error())
			return
		}
		got, err := verifyDownloadCrewFile(ctx, v.client, crewID, f.Dest)
		if err != nil {
			missing++
			fmt.Fprintf(os.Stderr, "    %s: %v\n", f.Dest, err)
			continue
		}
		if !bytes.Equal(want, got) {
			drift++
			fmt.Fprintf(os.Stderr, "    %s: %d bytes on the crew, %d in the seed\n", f.Dest, len(got), len(want))
		}
	}
	switch {
	case missing > 0 && drift > 0:
		v.add("files", verifyFail, fmt.Sprintf("%d missing, %d drifted of %d", missing, drift, len(v.pack.Files)))
	case missing > 0:
		v.add("files", verifyFail, fmt.Sprintf("%d of %d missing — re-run `crewship seed` (delivery needs a stopped or fresh crew)", missing, len(v.pack.Files)))
	case drift > 0:
		v.add("files", verifyFail, fmt.Sprintf("%d of %d drifted from the seed — the crew runs an older script", drift, len(v.pack.Files)))
	default:
		v.add("files", verifyPass, fmt.Sprintf("%d file(s) byte-identical to the seed", len(v.pack.Files)))
	}
}

func verifyDownloadCrewFile(ctx context.Context, client *cli.Client, crewID, dest string) ([]byte, error) {
	q := url.Values{}
	q.Set("path", dest)
	req, err := client.NewRequest(ctx, http.MethodGet,
		"/api/v1/crews/"+url.PathEscape(crewID)+"/files/download?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// verifyProbe runs the pack's probe and compares it with an independent read.
func (v *packVerifier) verifyProbe(ctx context.Context) bool {
	run, err := verifyRunRoutine(ctx, v.client, v.wsID, v.pack.ProbeSlug, v.opts.timeout)
	if err != nil {
		v.add("probe", verifyFail, err.Error())
		return false
	}
	probeJSON, ok := stepOutput(run, "probe")
	if !ok {
		v.add("probe", verifyFail, "run "+run.ID+" has no output for step 'probe'")
		return false
	}
	var probe map[string]any
	if err := json.Unmarshal([]byte(probeJSON), &probe); err != nil {
		v.add("probe", verifyFail, "step 'probe' output is not JSON: "+truncateForSmoke(probeJSON, 120))
		return false
	}
	v.probe = probe
	if msg, _ := probe["error"].(string); msg != "" {
		v.add("probe", verifyFail, "probe reported an error: "+msg)
		return false
	}

	switch v.pack.Slug {
	case "ci-watch":
		return v.verifyCIProbe(ctx, run, probe)
	default:
		v.add("probe", verifyPass, "run "+run.ID+" completed")
		return true
	}
}

func (v *packVerifier) verifyCIProbe(ctx context.Context, run *cli.PipelineRunDetail, probe map[string]any) bool {
	red, stale := intField(probe, "red"), intField(probe, "stale")
	repo := "crewship-ai/crewship"
	if inputs := run.Inputs; inputs != nil {
		if r, _ := inputs["repo"].(string); r != "" {
			repo = r
		}
	}
	token := firstNonEmpty(os.Getenv(packGitHubEnv), os.Getenv("GH_TOKEN"), os.Getenv("GITHUB_TOKEN"))
	truth, err := githubScheduledTruth(ctx, repo, token, 48*time.Hour, v.opts.now())
	if err != nil {
		v.add("probe", verifyFail, fmt.Sprintf("probe red=%d stale=%d, but the independent GitHub read failed: %v", red, stale, err))
		return false
	}
	if truth.red != red || truth.stale != stale {
		// A run can finish between the two reads. One more read before
		// calling it a disagreement.
		truth2, err2 := githubScheduledTruth(ctx, repo, token, 48*time.Hour, v.opts.now())
		if err2 == nil && truth2.red == red && truth2.stale == stale {
			truth = truth2
		}
	}
	detail := fmt.Sprintf("probe red=%d stale=%d of %d · GitHub red=%d stale=%d of %d · wake=%v",
		red, stale, intField(probe, "checked"), truth.red, truth.stale, truth.checked, probe["wake"])
	if truth.red != red || truth.stale != stale {
		v.add("probe", verifyFail, detail+" — the probe and GitHub disagree")
		return false
	}
	v.add("probe", verifyPass, detail)
	return true
}

// verifyReport runs the report routine and checks its agent output.
func (v *packVerifier) verifyReport(ctx context.Context) (*cli.PipelineRunDetail, bool) {
	run, err := verifyRunRoutine(ctx, v.client, v.wsID, v.pack.ReportSlug, v.opts.timeout)
	if err != nil {
		v.add("report", verifyFail, err.Error())
		return nil, false
	}
	switch v.pack.Slug {
	case "ci-watch":
		return run, v.verifyCIReport(run)
	case "docs-drift":
		return run, v.verifyDocsReport(run)
	case "site-replica":
		return run, v.verifyReplicaReport(run)
	default:
		v.add("report", verifyPass, "run "+run.ID+" completed")
		return run, true
	}
}

func (v *packVerifier) verifyCIReport(run *cli.PipelineRunDetail) bool {
	text, ok := stepOutput(run, "triage")
	if !ok {
		v.add("report", verifyFail, "run "+run.ID+" has no output for step 'triage'")
		return false
	}
	if leak := leakedSecret(text); leak != "" {
		v.add("report", verifyFail, "the report contains what looks like a secret: "+leak)
		return false
	}
	counts, ok := parseCountsLine(text)
	if !ok {
		v.add("report", verifyFail, "no COUNTS: line at the end of the triage")
		return false
	}
	probeJSON, ok := stepOutput(run, "probe")
	if !ok {
		v.add("report", verifyFail, "run has no output for step 'probe'")
		return false
	}
	var probe map[string]any
	if err := json.Unmarshal([]byte(probeJSON), &probe); err != nil {
		v.add("report", verifyFail, "probe output in the report run is not JSON")
		return false
	}
	red, stale := intField(probe, "red"), intField(probe, "stale")
	handled := counts["regressions"] + counts["flaky"] + counts["infra"] + counts["unclear"]
	detail := fmt.Sprintf("COUNTS regressions=%d flaky=%d infra=%d stale=%d unclear=%d · probe red=%d stale=%d",
		counts["regressions"], counts["flaky"], counts["infra"], counts["stale"], counts["unclear"], red, stale)
	if handled != red || counts["stale"] != stale {
		v.add("report", verifyFail, detail+" — the agent's counts do not reconcile with the probe")
		return false
	}
	v.add("report", verifyPass, detail)
	return true
}

func (v *packVerifier) verifyDocsReport(run *cli.PipelineRunDetail) bool {
	text, ok := stepOutput(run, "review")
	if !ok {
		v.add("report", verifyFail, "run "+run.ID+" has no output for step 'review'")
		return false
	}
	if leak := leakedSecret(text); leak != "" {
		v.add("report", verifyFail, "the report contains what looks like a secret: "+leak)
		return false
	}
	counts, ok := parseCountsLine(text)
	if !ok {
		v.add("report", verifyFail, "no COUNTS: line at the end of the review")
		return false
	}
	scanJSON, ok := stepOutput(run, "scan")
	if !ok {
		v.add("report", verifyFail, "run has no output for step 'scan'")
		return false
	}
	var scan map[string]any
	if err := json.Unmarshal([]byte(scanJSON), &scan); err != nil {
		v.add("report", verifyFail, "scan output is not JSON")
		return false
	}
	if msg, _ := scan["error"].(string); msg != "" {
		v.add("report", verifyFail, "scan reported an error: "+msg)
		return false
	}
	total := intField(scan, "total_candidates")
	detail := fmt.Sprintf("COUNTS candidates=%d confirmed=%d rejected=%d truncated=%d · scan candidates=%d across %d pairs",
		counts["candidates"], counts["confirmed"], counts["rejected"], counts["truncated"], total, intField(scan, "pairs"))
	if counts["candidates"] != total {
		v.add("report", verifyFail, detail+" — the agent's candidate count does not match the scan")
		return false
	}
	if counts["confirmed"]+counts["rejected"] != counts["candidates"] {
		v.add("report", verifyFail, detail+" — confirmed + rejected does not add up to candidates")
		return false
	}
	v.add("report", verifyPass, detail)

	// Fact-check: every path the agent cites must exist at the scanned SHA.
	sha, _ := scan["sha"].(string)
	v.verifyCitedPaths(text, sha)
	return true
}

// verifyCitedPaths checks that the `file:line` citations in a confirmed
// finding name files that exist at the scanned commit, using a local
// checkout. Without one the step is reported SKIP, not silently passed.
func (v *packVerifier) verifyCitedPaths(text, sha string) {
	repoDir := v.opts.repoDir
	if repoDir == "" {
		if wd, err := os.Getwd(); err == nil && isGitRepo(wd) {
			repoDir = wd
		}
	}
	cites := citedPaths(text)
	if len(cites) == 0 {
		v.add("fact-check", verifyPass, "no confirmed finding cites a path — nothing to check")
		return
	}
	if repoDir == "" || sha == "" || sha == "unknown" {
		v.add("fact-check", verifySkip, fmt.Sprintf("%d citation(s), but no local checkout (--repo-dir) or no scan SHA to check them against", len(cites)))
		return
	}
	var bad []string
	for _, c := range cites {
		if !gitPathExists(repoDir, sha, c.path) {
			bad = append(bad, c.path)
		}
	}
	if len(bad) > 0 {
		v.add("fact-check", verifyFail, fmt.Sprintf("%d of %d cited paths do not exist at %s: %s", len(bad), len(cites), short(sha), strings.Join(bad, ", ")))
		return
	}
	v.add("fact-check", verifyPass, fmt.Sprintf("%d cited path(s) exist at %s", len(cites), short(sha)))
}

func (v *packVerifier) verifyReplicaReport(run *cli.PipelineRunDetail) bool {
	checkJSON, ok := stepOutput(run, "check")
	if !ok {
		v.add("report", verifyFail, "run "+run.ID+" has no output for step 'check'")
		return false
	}
	var check map[string]any
	if err := json.Unmarshal([]byte(checkJSON), &check); err != nil {
		v.add("report", verifyFail, "check output is not JSON")
		return false
	}
	built, _ := check["built"].(bool)
	if !built {
		v.add("report", verifySkip, "no replica built yet — start the lead issue (copy seznam.cz) and run again")
		return true
	}
	okc, _ := check["ok"].(bool)
	detail := fmt.Sprintf("%d passed, %d failed", intField(check, "passed"), intField(check, "failed"))
	if !okc {
		var failed []string
		if items, _ := check["checks"].([]any); items != nil {
			for _, it := range items {
				m, _ := it.(map[string]any)
				if pass, _ := m["ok"].(bool); !pass {
					failed = append(failed, fmt.Sprint(m["name"]))
				}
			}
		}
		v.add("report", verifyFail, detail+": "+strings.Join(failed, ", "))
		return false
	}
	v.add("report", verifyPass, detail)
	return true
}

// verifyInbox confirms the report routine's notification landed after the
// verification started.
func (v *packVerifier) verifyInbox(run *cli.PipelineRunDetail) {
	resp, err := v.client.Get("/api/v1/inbox?state=all&limit=100")
	if err != nil {
		v.add("inbox", verifyFail, err.Error())
		return
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		v.add("inbox", verifyFail, err.Error())
		return
	}
	var list struct {
		Rows []struct {
			Title     string `json:"title"`
			CreatedAt string `json:"created_at"`
		} `json:"rows"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		v.add("inbox", verifyFail, "inbox response: "+err.Error())
		return
	}
	prefix := verifyNotifyTitlePrefix(v.pack.Slug)
	for _, row := range list.Rows {
		if !strings.HasPrefix(row.Title, prefix) {
			continue
		}
		if t, err := time.Parse(time.RFC3339, row.CreatedAt); err == nil && t.Before(v.started.Add(-time.Minute)) {
			continue
		}
		v.add("inbox", verifyPass, "notification: "+row.Title)
		return
	}
	v.add("inbox", verifyFail, fmt.Sprintf("no inbox item titled %q… since %s", prefix, v.started.Format(time.RFC3339)))
}

// verifyNotifyTitlePrefix is the title the pack's notify step uses. Kept next
// to the routines it mirrors (routines_packs.go); a rename there is a red
// verify here, which is the point.
func verifyNotifyTitlePrefix(pack string) string {
	switch pack {
	case "ci-watch":
		return "Nightly CI"
	case "docs-drift":
		return "Docs drift"
	case "site-replica":
		return "Site replica"
	}
	return ""
}

// verifyPage confirms every panel of the pack's page was written by THIS run.
func (v *packVerifier) verifyPage(run *cli.PipelineRunDetail) {
	resp, err := v.client.Get("/api/v1/pages/" + url.PathEscape(v.pack.PageSlug))
	if err != nil {
		v.add("page", verifyFail, err.Error())
		return
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		v.add("page", verifyFail, err.Error())
		return
	}
	var page struct {
		Panels []struct {
			ID         string `json:"id"`
			RunID      string `json:"run_id"`
			ProducedAt string `json:"produced_at"`
		} `json:"panels"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		v.add("page", verifyFail, "page response: "+err.Error())
		return
	}
	if len(page.Panels) == 0 {
		v.add("page", verifyFail, "page "+v.pack.PageSlug+" has no panels")
		return
	}
	var stale []string
	for _, p := range page.Panels {
		if p.RunID != run.ID {
			stale = append(stale, fmt.Sprintf("%s (run %s)", p.ID, firstNonEmpty(p.RunID, "never")))
		}
	}
	if len(stale) > 0 {
		v.add("page", verifyFail, fmt.Sprintf("%d of %d panels not written by run %s: %s", len(stale), len(page.Panels), run.ID, strings.Join(stale, ", ")))
		return
	}
	v.add("page", verifyPass, fmt.Sprintf("%d panel(s) written by run %s", len(page.Panels), run.ID))
}

// ── plumbing ──────────────────────────────────────────────────────────────

// verifyRunRoutine starts a run parked for one second and polls it to a
// terminal state. Parked, because the synchronous run path holds the run
// inside the HTTP request — and a client timeout there cancels the server's
// work mid-step. The dispatcher picks a parked run up outside any request.
func verifyRunRoutine(ctx context.Context, client *cli.Client, wsID, slug string, timeout time.Duration) (*cli.PipelineRunDetail, error) {
	resp, err := client.Post(fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/run", wsID, slug),
		map[string]any{"inputs": map[string]any{}, "delay_seconds": 1})
	if err != nil {
		return nil, fmt.Errorf("run %s: %w", slug, err)
	}
	if err := cli.CheckError(resp); err != nil {
		resp.Body.Close()
		return nil, fmt.Errorf("run %s: %w", slug, err)
	}
	var started struct {
		RunID string `json:"run_id"`
		ID    string `json:"id"`
	}
	if err := cli.ReadJSON(resp, &started); err != nil {
		return nil, fmt.Errorf("run %s: %w", slug, err)
	}
	runID := firstNonEmpty(started.RunID, started.ID)
	if runID == "" {
		return nil, fmt.Errorf("run %s: no run id in the response", slug)
	}
	pollCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		pollCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	detail, err := client.PollPipelineRun(pollCtx, runID, 2*time.Second, nil)
	if err != nil {
		if errors.Is(pollCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("run %s (%s) did not finish within %s", slug, runID, timeout)
		}
		return nil, err
	}
	switch strings.ToLower(detail.Status) {
	case "completed", "succeeded", "success", "done":
		return detail, nil
	}
	return nil, fmt.Errorf("run %s (%s) ended %s at step %s: %s", slug, runID, strings.ToUpper(detail.Status), detail.FailedAtStep, detail.ErrorMessage)
}

// stepOutput returns a step's output as text.
func stepOutput(run *cli.PipelineRunDetail, step string) (string, bool) {
	if run == nil || run.StepOutputs == nil {
		return "", false
	}
	v, ok := run.StepOutputs[step]
	if !ok || v == nil {
		return "", false
	}
	switch t := v.(type) {
	case string:
		return t, true
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return "", false
		}
		return string(b), true
	}
}

func verifyListCrews(client *cli.Client) (map[string]string, error) {
	resp, err := client.Get("/api/v1/crews")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return nil, err
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	type crew struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	}
	var rows []crew
	if err := json.Unmarshal(body, &rows); err != nil {
		var wrapped struct {
			Crews []crew `json:"crews"`
			Items []crew `json:"items"`
		}
		if err2 := json.Unmarshal(body, &wrapped); err2 != nil {
			return nil, err
		}
		rows = append(wrapped.Crews, wrapped.Items...)
	}
	out := map[string]string{}
	for _, c := range rows {
		out[c.Slug] = c.ID
	}
	return out, nil
}

var countsLineRe = regexp.MustCompile(`(?m)^\s*COUNTS:\s*(.+?)\s*$`)
var countPairRe = regexp.MustCompile(`([A-Za-z_]+)\s*=\s*(\d+)`)

// parseCountsLine reads the last `COUNTS: a=1 b=2` line of a report.
func parseCountsLine(text string) (map[string]int, bool) {
	matches := countsLineRe.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil, false
	}
	last := matches[len(matches)-1][1]
	out := map[string]int{}
	for _, m := range countPairRe.FindAllStringSubmatch(last, -1) {
		n, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		out[strings.ToLower(m[1])] = n
	}
	return out, len(out) > 0
}

var secretLeakRe = regexp.MustCompile(`(ghp_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|sk-ant-[A-Za-z0-9_-]{20,}|x-access-token:[^@\s]+@|Bearer [A-Za-z0-9._-]{20,}|API_KEY=\S+)`)

// leakedSecret returns the first substring that looks like a credential.
func leakedSecret(text string) string {
	m := secretLeakRe.FindString(text)
	if m == "" {
		return ""
	}
	if len(m) > 24 {
		m = m[:24] + "…"
	}
	return m
}

type citation struct{ path string }

var citeRe = regexp.MustCompile("`((?:docs|internal|cmd|app|components|lib|hooks|stores|scripts)/[A-Za-z0-9_./-]+?):(\\d+)`")

// citedPaths pulls `path:line` citations out of a report, deduplicated.
func citedPaths(text string) []citation {
	seen := map[string]bool{}
	var out []citation
	for _, m := range citeRe.FindAllStringSubmatch(text, -1) {
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		out = append(out, citation{path: m[1]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

func isGitRepo(dir string) bool {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--git-dir")
	return cmd.Run() == nil
}

// gitPathExists reports whether path exists in the local checkout at sha.
// A SHA the checkout does not have is a fetch away; reported as missing so
// the operator sees it rather than a silent pass.
func gitPathExists(dir, sha, path string) bool {
	cmd := exec.Command("git", "-C", dir, "cat-file", "-e", sha+":"+path)
	return cmd.Run() == nil
}

func intField(m map[string]any, key string) int {
	switch t := m[key].(type) {
	case float64:
		return int(t)
	case int:
		return t
	case json.Number:
		n, _ := t.Int64()
		return int(n)
	case string:
		n, _ := strconv.Atoi(t)
		return n
	}
	return 0
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// ── GitHub ground truth ───────────────────────────────────────────────────

type scheduledTruth struct {
	checked, red, stale int
}

var githubFailureConclusions = map[string]bool{
	"failure": true, "timed_out": true, "cancelled": true,
	"startup_failure": true, "action_required": true, "stale": true,
}

// githubScheduledTruth reads the scheduled-run state of every active
// workflow directly from GitHub — the same rule the probe applies, written
// a second time in a second language on a second machine, which is what
// makes the comparison worth something.
func githubScheduledTruth(ctx context.Context, repo, token string, maxStale time.Duration, now time.Time) (scheduledTruth, error) {
	var truth scheduledTruth
	if token == "" {
		return truth, fmt.Errorf("no GitHub token in %s / GH_TOKEN", packGitHubEnv)
	}
	var wfs struct {
		Workflows []struct {
			ID    int64  `json:"id"`
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"workflows"`
	}
	if err := githubGet(ctx, token, fmt.Sprintf("/repos/%s/actions/workflows?per_page=100", repo), &wfs); err != nil {
		return truth, err
	}
	for _, w := range wfs.Workflows {
		if w.State != "active" {
			continue
		}
		var runs struct {
			WorkflowRuns []struct {
				Conclusion *string `json:"conclusion"`
				CreatedAt  string  `json:"created_at"`
			} `json:"workflow_runs"`
		}
		if err := githubGet(ctx, token, fmt.Sprintf("/repos/%s/actions/workflows/%d/runs?event=schedule&branch=main&per_page=5", repo, w.ID), &runs); err != nil {
			return truth, err
		}
		if len(runs.WorkflowRuns) == 0 {
			continue
		}
		truth.checked++
		var latestAt time.Time
		var latestConclusion *string
		for _, r := range runs.WorkflowRuns {
			t, err := time.Parse(time.RFC3339, r.CreatedAt)
			if err != nil {
				continue
			}
			if t.After(latestAt) {
				latestAt = t
				latestConclusion = r.Conclusion
			}
		}
		switch {
		case latestConclusion != nil && githubFailureConclusions[*latestConclusion]:
			truth.red++
		case latestConclusion == nil:
			// still running — neither red nor stale
		case now.Sub(latestAt) > maxStale:
			truth.stale++
		}
	}
	return truth, nil
}

func githubGet(ctx context.Context, token, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, verifyGitHubAPI()+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "crewship-seed-verify")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("GitHub API %s: HTTP %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// ── output ────────────────────────────────────────────────────────────────

func printVerify(checks []verifyCheck, strict bool) error {
	failed, skipped := 0, 0
	for _, c := range checks {
		switch c.Result {
		case verifyFail:
			failed++
		case verifySkip:
			skipped++
		}
	}
	if flagFormat == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(map[string]any{
			"checks":  checks,
			"failed":  failed,
			"skipped": skipped,
			"passed":  len(checks) - failed - skipped,
			"strict":  strict,
		}); err != nil {
			return err
		}
	} else {
		fmt.Println()
		fmt.Printf("%-14s %-12s %-6s %s\n", "PACK", "STEP", "RESULT", "DETAIL")
		for _, c := range checks {
			fmt.Printf("%-14s %-12s %-6s %s\n", c.Pack, c.Step, c.Result, c.Detail)
		}
		fmt.Println()
		fmt.Printf("%d passed, %d failed, %d skipped\n", len(checks)-failed-skipped, failed, skipped)
	}
	if failed > 0 {
		return fmt.Errorf("seed verify: %d check(s) failed", failed)
	}
	if strict && skipped > 0 {
		return fmt.Errorf("seed verify: %d check(s) skipped and --strict is set", skipped)
	}
	return nil
}
