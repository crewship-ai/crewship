package main

// B9 (#2362) acceptance — the reliability editor's two new server doors,
// driven through the BUILT BINARY against the REAL api router:
//
//   - `crewship routine schedules preview` (§13.2 "When") answers "what
//     would this cron fire" without a saved schedule.
//   - `crewship routine webhooks update` (F21) edits name / rate limit /
//     inputs template in place; the public URL a sender already has
//     configured keeps resolving to the SAME webhook, signed with the
//     SAME secret, after the edit.
//
// Mirrors the shape of acceptance_routine_trigger_test.go: a real migrated
// DB, a real api.NewRouter, and the CLI binary run as a subprocess against
// it.

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/testutil"
)

const reliabilityAcceptanceWorkspaceID = "creliabilityws00000001"

func startReliabilityAcceptanceServer(t *testing.T) (cfgPath, serverURL string) {
	t.Helper()

	// CreateWebhook/UpdateWebhook encrypt the HMAC signing secret at rest
	// and fail CLOSED with no usable key (#1254 item C) — the server runs
	// in-process (httptest.NewServer) inside this same test binary, so
	// t.Setenv here reaches it.
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("ab", 32))

	dbh := testutil.MigratedDB(t)
	db := dbh.DB
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed exec %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Reliability', 'reliability-ws')`, reliabilityAcceptanceWorkspaceID)
	mustExec(`INSERT INTO users (id, email, full_name) VALUES ('rel-owner', 'owner@rel-ex.com', 'Owner')`)
	mustExec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('relm-owner', ?, 'rel-owner', 'OWNER')`,
		reliabilityAcceptanceWorkspaceID)
	now := time.Now().UTC().Format(time.RFC3339)
	mustExec(`INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash, created_at, updated_at, last_test_run_at)
		VALUES ('rel-pipe', ?, 'rel-routine', 'rel-routine', '{"name":"rel-routine","steps":[]}', 'hash', ?, ?, ?)`,
		reliabilityAcceptanceWorkspaceID, now, now, now)
	// A second routine so webhook --slug retargeting has somewhere to go.
	mustExec(`INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash, created_at, updated_at, last_test_run_at)
		VALUES ('rel-pipe-2', ?, 'rel-routine-2', 'rel-routine-2', '{"name":"rel-routine-2","steps":[]}', 'hash', ?, ?, ?)`,
		reliabilityAcceptanceWorkspaceID, now, now, now)

	// Two finished runs of the first routine, for `routine health`.
	for i, status := range []string{"completed", "failed"} {
		ts := time.Now().UTC().Add(-time.Duration(2-i) * time.Hour).Format(time.RFC3339Nano)
		mustExec(`INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, status, mode, started_at, cost_usd, created_at, updated_at)
			VALUES (?, ?, 'rel-pipe', 'rel-routine', ?, 'run', ?, 0.25, ?, ?)`,
			"rel-run-"+status, reliabilityAcceptanceWorkspaceID, status, ts, ts, ts)
	}

	const ownerToken = "crewship_cli_relowner00000000000000000"
	mustExec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES ('clt-rel-owner', 'rel-owner', 't', ?, datetime('now'))`,
		sha256HexToken(ownerToken))

	router, err := api.NewRouter(db, "this-is-a-32-char-test-secret-pad", logger)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	router.PipelinesHandler.SetScheduleStore(pipeline.NewScheduleStore(db))
	router.PipelinesHandler.SetWebhookStore(pipeline.NewWebhookStore(db))
	router.PipelinesHandler.SetRunStore(pipeline.NewRunStore(db))
	// FireWebhook 503s without a runner even though dispatch never reaches
	// it for the synchronous 202 this test checks (the run starts in a
	// background goroutine) — see FireWebhook's doc comment.
	router.PipelinesHandler.SetRunner(unusedAgentRunner{})

	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	cfgPath = filepath.Join(t.TempDir(), "cli-config.yaml")
	cfg := "server: " + srv.URL + "\nworkspace: " + reliabilityAcceptanceWorkspaceID +
		"\ntoken: " + ownerToken + "\nformat: table\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath, srv.URL
}

func runReliabilityCLI(t *testing.T, cfgPath string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(buildCrewshipBinary(t), args...)
	cmd.Env = append(os.Environ(),
		"CREWSHIP_CONFIG="+cfgPath,
		"NO_COLOR=1",
		"CREWSHIP_SERVER=", "CREWSHIP_PROFILE=", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestAcceptance_RoutineSchedulesPreview_FiveFireTimes drives `routine
// schedules preview` through the CLI binary and checks it prints five
// future fire times for a cron expression that was never saved as a
// schedule — the stateless "what would this fire" the reliability editor
// needs while a cron/timezone pair is still being drafted.
func TestAcceptance_RoutineSchedulesPreview_FiveFireTimes(t *testing.T) {
	cfgPath, _ := startReliabilityAcceptanceServer(t)

	out, err := runReliabilityCLI(t, cfgPath, "-f", "json", "routine", "schedules", "preview",
		"--cron", "0 9 * * *", "--timezone", "Europe/Prague")
	if err != nil {
		t.Fatalf("routine schedules preview failed: %v\n%s", err, out)
	}
	var result struct {
		CronExpr    string   `json:"cron_expr"`
		Timezone    string   `json:"timezone"`
		Occurrences []string `json:"occurrences"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode preview output: %v\nraw: %s", err, out)
	}
	if len(result.Occurrences) != 5 {
		t.Fatalf("got %d occurrences, want 5:\n%s", len(result.Occurrences), out)
	}
	if result.Timezone != "Europe/Prague" {
		t.Fatalf("timezone = %q, want Europe/Prague", result.Timezone)
	}
	for i, occ := range result.Occurrences {
		if _, perr := time.Parse(time.RFC3339, occ); perr != nil {
			t.Errorf("occurrence[%d] = %q is not RFC3339: %v", i, occ, perr)
		}
	}
}

// TestAcceptance_RoutineSchedulesPreview_BadCron_Fails proves the CLI
// surfaces the server's cron-parse error rather than silently succeeding.
func TestAcceptance_RoutineSchedulesPreview_BadCron_Fails(t *testing.T) {
	cfgPath, _ := startReliabilityAcceptanceServer(t)

	out, err := runReliabilityCLI(t, cfgPath, "routine", "schedules", "preview", "--cron", "not a cron")
	if err == nil {
		t.Fatalf("expected preview to fail on a bad cron expression, got success:\n%s", out)
	}
}

func hmacSHA256Hex(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// TestAcceptance_RoutineWebhooksUpdate_EditsInPlaceWithoutRotatingURL is
// the F21 accept line proven end to end: create a webhook through the
// CLI, capture the URL and secret it reveals ONCE, edit name/rate-limit/
// inputs-template through `routine webhooks update`, and confirm the
// EXACT SAME URL signed with the EXACT SAME secret still dispatches
// (202) both before and after the edit — the whole point of F21 over the
// old delete+recreate dance.
func TestAcceptance_RoutineWebhooksUpdate_EditsInPlaceWithoutRotatingURL(t *testing.T) {
	cfgPath, serverURL := startReliabilityAcceptanceServer(t)

	createOut, err := runReliabilityCLI(t, cfgPath, "-f", "json", "routine", "webhooks", "create",
		"--slug", "rel-routine", "--name", "original name", "--rate-limit", "10", "--base-url", serverURL)
	if err != nil {
		t.Fatalf("routine webhooks create failed: %v\n%s", err, createOut)
	}
	var created struct {
		ID            string `json:"id"`
		PublicURL     string `json:"public_url"`
		SigningSecret string `json:"signing_secret"`
	}
	if err := json.Unmarshal([]byte(createOut), &created); err != nil {
		t.Fatalf("decode create output: %v\nraw: %s", err, createOut)
	}
	if created.ID == "" || created.PublicURL == "" || created.SigningSecret == "" {
		t.Fatalf("create response missing id/public_url/signing_secret:\n%s", createOut)
	}

	fire := func(label string) {
		t.Helper()
		body := []byte(`{"hello":"world"}`)
		req, rerr := http.NewRequest(http.MethodPost, created.PublicURL, bytes.NewReader(body))
		if rerr != nil {
			t.Fatalf("%s: build request: %v", label, rerr)
		}
		req.Header.Set("X-Crewship-Signature", "sha256="+hmacSHA256Hex(created.SigningSecret, body))
		resp, derr := http.DefaultClient.Do(req)
		if derr != nil {
			t.Fatalf("%s: fire webhook: %v", label, derr)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("%s: status = %d, want 202 (same URL + same secret must still dispatch)", label, resp.StatusCode)
		}
	}
	fire("before update")

	updateOut, err := runReliabilityCLI(t, cfgPath, "-f", "json", "routine", "webhooks", "update", created.ID,
		"--name", "renamed via CLI", "--rate-limit", "77", "--inputs-template", `{"source":"cli-update"}`)
	if err != nil {
		t.Fatalf("routine webhooks update failed: %v\n%s", err, updateOut)
	}
	var updated struct {
		Name            string         `json:"name"`
		RateLimitPerMin int            `json:"rate_limit_per_min"`
		InputsTemplate  map[string]any `json:"inputs_template"`
		SigningSecret   string         `json:"signing_secret"`
	}
	if err := json.Unmarshal([]byte(updateOut), &updated); err != nil {
		t.Fatalf("decode update output: %v\nraw: %s", err, updateOut)
	}
	if updated.Name != "renamed via CLI" {
		t.Errorf("name = %q, want %q", updated.Name, "renamed via CLI")
	}
	if updated.RateLimitPerMin != 77 {
		t.Errorf("rate_limit_per_min = %d, want 77", updated.RateLimitPerMin)
	}
	if updated.InputsTemplate["source"] != "cli-update" {
		t.Errorf("inputs_template[source] = %v, want %q", updated.InputsTemplate["source"], "cli-update")
	}
	if updated.SigningSecret != "" {
		t.Errorf("an ordinary update (no --rotate-secret) must not reveal a secret, got %q", updated.SigningSecret)
	}

	// The proof: the SAME URL, signed with the SAME secret the create
	// response revealed, still resolves and dispatches after the edit.
	fire("after update")

	listOut, err := runReliabilityCLI(t, cfgPath, "-f", "json", "routine", "webhooks", "list", "--slug", "rel-routine")
	if err != nil {
		t.Fatalf("routine webhooks list failed: %v\n%s", err, listOut)
	}
	if !strings.Contains(listOut, "renamed via CLI") {
		t.Errorf("list output does not reflect the rename:\n%s", listOut)
	}
}

// TestAcceptance_RoutineWebhooksUpdate_RotateSecret_ChangesSecretNotURL
// proves the other half of F21's "explicit, opt-in token rotation": with
// --rotate-secret the OLD signature stops working but the URL (token)
// stays exactly the same — rotating the secret is not the same operation
// as rotating the URL, and only the URL rotation still requires delete +
// recreate.
func TestAcceptance_RoutineWebhooksUpdate_RotateSecret_ChangesSecretNotURL(t *testing.T) {
	cfgPath, serverURL := startReliabilityAcceptanceServer(t)

	createOut, err := runReliabilityCLI(t, cfgPath, "-f", "json", "routine", "webhooks", "create",
		"--slug", "rel-routine", "--name", "rotate-me", "--base-url", serverURL)
	if err != nil {
		t.Fatalf("routine webhooks create failed: %v\n%s", err, createOut)
	}
	var created struct {
		ID            string `json:"id"`
		PublicURL     string `json:"public_url"`
		SigningSecret string `json:"signing_secret"`
	}
	if err := json.Unmarshal([]byte(createOut), &created); err != nil {
		t.Fatalf("decode create output: %v\nraw: %s", err, createOut)
	}

	rotateOut, err := runReliabilityCLI(t, cfgPath, "-f", "json", "routine", "webhooks", "update", created.ID,
		"--rotate-secret")
	if err != nil {
		t.Fatalf("routine webhooks update --rotate-secret failed: %v\n%s", err, rotateOut)
	}
	var rotated struct {
		SigningSecret string `json:"signing_secret"`
	}
	if err := json.Unmarshal([]byte(rotateOut), &rotated); err != nil {
		t.Fatalf("decode rotate output: %v\nraw: %s", err, rotateOut)
	}
	if rotated.SigningSecret == "" || rotated.SigningSecret == created.SigningSecret {
		t.Fatalf("expected a freshly minted secret in the rotate response, got %q (original %q)",
			rotated.SigningSecret, created.SigningSecret)
	}

	// The OLD secret must no longer validate against the SAME URL.
	body := []byte(`{"hello":"world"}`)
	req, _ := http.NewRequest(http.MethodPost, created.PublicURL, bytes.NewReader(body))
	req.Header.Set("X-Crewship-Signature", "sha256="+hmacSHA256Hex(created.SigningSecret, body))
	resp, derr := http.DefaultClient.Do(req)
	if derr != nil {
		t.Fatalf("fire with old secret: %v", derr)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusAccepted {
		t.Fatalf("the OLD secret still validated after --rotate-secret — rotation did not take effect")
	}

	// The NEW secret against the SAME URL (token unchanged) must work.
	req2, _ := http.NewRequest(http.MethodPost, created.PublicURL, bytes.NewReader(body))
	req2.Header.Set("X-Crewship-Signature", "sha256="+hmacSHA256Hex(rotated.SigningSecret, body))
	resp2, derr2 := http.DefaultClient.Do(req2)
	if derr2 != nil {
		t.Fatalf("fire with new secret: %v", derr2)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusAccepted {
		t.Fatalf("status with new secret on the SAME URL = %d, want 202 (the token/URL must survive a secret rotation)", resp2.StatusCode)
	}
}

// TestAcceptance_RoutineWebhooksUpdate_SlugAndVersionPin drives the CLI
// parity fields `update` shares with `schedules update` — code review on
// this PR found them missing from the CLI even though the server, the
// OpenAPI schema and the docs all advertised them. Proves --slug actually
// retargets, --pin-version actually pins, and --unpin actually clears it —
// again, none of it touching the URL.
func TestAcceptance_RoutineWebhooksUpdate_SlugAndVersionPin(t *testing.T) {
	cfgPath, serverURL := startReliabilityAcceptanceServer(t)

	createOut, err := runReliabilityCLI(t, cfgPath, "-f", "json", "routine", "webhooks", "create",
		"--slug", "rel-routine", "--name", "retarget-me", "--base-url", serverURL)
	if err != nil {
		t.Fatalf("routine webhooks create failed: %v\n%s", err, createOut)
	}
	var created struct {
		ID        string `json:"id"`
		PublicURL string `json:"public_url"`
	}
	if err := json.Unmarshal([]byte(createOut), &created); err != nil {
		t.Fatalf("decode create output: %v\nraw: %s", err, createOut)
	}

	updateOut, err := runReliabilityCLI(t, cfgPath, "-f", "json", "routine", "webhooks", "update", created.ID,
		"--slug", "rel-routine-2", "--pin-version", "3")
	if err != nil {
		t.Fatalf("routine webhooks update --slug --pin-version failed: %v\n%s", err, updateOut)
	}
	var updated struct {
		TargetPipelineSlug    string `json:"target_pipeline_slug"`
		TargetPipelineVersion *int   `json:"target_pipeline_version"`
		PublicURL             string `json:"public_url"`
	}
	if err := json.Unmarshal([]byte(updateOut), &updated); err != nil {
		t.Fatalf("decode update output: %v\nraw: %s", err, updateOut)
	}
	if updated.TargetPipelineSlug != "rel-routine-2" {
		t.Errorf("target_pipeline_slug = %q, want rel-routine-2", updated.TargetPipelineSlug)
	}
	if updated.TargetPipelineVersion == nil || *updated.TargetPipelineVersion != 3 {
		t.Errorf("target_pipeline_version = %v, want 3", updated.TargetPipelineVersion)
	}

	unpinOut, err := runReliabilityCLI(t, cfgPath, "-f", "json", "routine", "webhooks", "update", created.ID, "--unpin")
	if err != nil {
		t.Fatalf("routine webhooks update --unpin failed: %v\n%s", err, unpinOut)
	}
	var unpinned struct {
		TargetPipelineVersion *int `json:"target_pipeline_version"`
	}
	if err := json.Unmarshal([]byte(unpinOut), &unpinned); err != nil {
		t.Fatalf("decode unpin output: %v\nraw: %s", err, unpinOut)
	}
	if unpinned.TargetPipelineVersion != nil {
		t.Errorf("target_pipeline_version = %v after --unpin, want nil (cleared)", unpinned.TargetPipelineVersion)
	}

	// Retargeting + pinning + unpinning never touched the URL: the original
	// public URL still resolves (401, not 404 — unsigned, but the token is
	// still known).
	req, _ := http.NewRequest(http.MethodPost, created.PublicURL, bytes.NewReader([]byte(`{}`)))
	resp, derr := http.DefaultClient.Do(req)
	if derr != nil {
		t.Fatalf("fire after retarget/pin/unpin: %v", derr)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		t.Fatalf("the URL stopped resolving after --slug/--pin-version/--unpin — the token rotated")
	}
}

// ODCH-RT-15: `routine schedules preview --when "every day at 9am"` asks the
// server to turn the phrase into a cron (GET .../preview?phrase=) and prints
// the cron it derived with the next starts.
func TestAcceptance_RoutineSchedulesPreview_Phrase(t *testing.T) {
	cfgPath, _ := startReliabilityAcceptanceServer(t)

	out, err := runReliabilityCLI(t, cfgPath, "-f", "json", "routine", "schedules", "preview",
		"--when", "every weekday at 8:30", "--timezone", "Europe/Prague", "--count", "3")
	if err != nil {
		t.Fatalf("preview --when failed: %v\n%s", err, out)
	}
	var result struct {
		CronExpr    string   `json:"cron_expr"`
		Occurrences []string `json:"occurrences"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("decode: %v\nraw: %s", err, out)
	}
	if result.CronExpr != "30 8 * * 1-5" || len(result.Occurrences) != 3 {
		t.Fatalf("got cron %q with %d occurrences, want \"30 8 * * 1-5\" with 3:\n%s", result.CronExpr, len(result.Occurrences), out)
	}

	if out, err := runReliabilityCLI(t, cfgPath, "routine", "schedules", "preview", "--when", "every day at 9am", "--cron", "0 9 * * *"); err == nil {
		t.Fatalf("--when with --cron must fail, got:\n%s", out)
	}
}

// ODCH-RT-03: `routine list --search` is GET /pipelines?q= — words that must
// all appear in a routine's name, slug, description or step text.
func TestAcceptance_RoutineList_Search(t *testing.T) {
	cfgPath, _ := startReliabilityAcceptanceServer(t)
	slugs := func(args ...string) []string {
		t.Helper()
		out, err := runReliabilityCLI(t, cfgPath, append([]string{"-f", "json", "routine", "list"}, args...)...)
		if err != nil {
			t.Fatalf("routine list %v: %v\n%s", args, err, out)
		}
		var rows []struct {
			Slug string `json:"slug"`
		}
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatalf("decode: %v\n%s", err, out)
		}
		got := []string{}
		for _, r := range rows {
			got = append(got, r.Slug)
		}
		return got
	}
	if got := slugs("--search", "ROUTINE-2"); len(got) != 1 || got[0] != "rel-routine-2" {
		t.Fatalf("--search ROUTINE-2 = %v, want [rel-routine-2]", got)
	}
	if got := slugs("--search", "no such text"); len(got) != 0 {
		t.Fatalf("--search with no match = %v, want none", got)
	}
	if got := slugs(); len(got) != 2 {
		t.Fatalf("without --search = %v, want both routines", got)
	}
}

// ODCH-RT-01: `routine folders` drives pipeline-folders end to end: create,
// list with counts, move routines in and out, rename, delete (routines stay).
func TestAcceptance_RoutineFolders(t *testing.T) {
	cfgPath, _ := startReliabilityAcceptanceServer(t)
	run := func(args ...string) string {
		t.Helper()
		out, err := runReliabilityCLI(t, cfgPath, append([]string{"-f", "json", "routine"}, args...)...)
		if err != nil {
			t.Fatalf("routine %v: %v\n%s", args, err, out)
		}
		return out
	}
	var created struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(run("folders", "create", "Market intel", "--color", "teal")), &created); err != nil || created.ID == "" || created.Name != "Market intel" {
		t.Fatalf("create: %v %+v", err, created)
	}
	if out, err := runReliabilityCLI(t, cfgPath, "routine", "folders", "create", "market INTEL"); err == nil {
		t.Fatalf("a duplicate name must be refused, got:\n%s", out)
	}

	run("folders", "move", "rel-routine", "rel-routine-2", "--folder", created.ID)
	folders := func() []struct {
		Name         string `json:"name"`
		RoutineCount int    `json:"routine_count"`
	} {
		var rows []struct {
			Name         string `json:"name"`
			RoutineCount int    `json:"routine_count"`
		}
		if err := json.Unmarshal([]byte(run("folders", "list")), &rows); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	if got := folders(); len(got) != 1 || got[0].RoutineCount != 2 {
		t.Fatalf("after move: %+v", got)
	}

	// The routine list reports the folder.
	var listed []struct {
		Slug     string `json:"slug"`
		FolderID string `json:"folder_id"`
	}
	if err := json.Unmarshal([]byte(run("list")), &listed); err != nil || len(listed) != 2 || listed[0].FolderID != created.ID {
		t.Fatalf("list: %v %+v", err, listed)
	}

	run("folders", "move", "rel-routine-2", "--none")
	run("folders", "update", created.ID, "--name", "Intel")
	if got := folders(); got[0].Name != "Intel" || got[0].RoutineCount != 1 {
		t.Fatalf("after unfile+rename: %+v", got)
	}
	if out, err := runReliabilityCLI(t, cfgPath, "routine", "folders", "move", "rel-routine"); err == nil {
		t.Fatalf("move without --folder/--none must fail, got:\n%s", out)
	}

	run("folders", "delete", created.ID)
	if got := folders(); len(got) != 0 {
		t.Fatalf("after delete: %+v", got)
	}
	if err := json.Unmarshal([]byte(run("list")), &listed); err != nil || len(listed) != 2 || listed[0].FolderID != "" {
		t.Fatalf("routines must survive the folder: %v %+v", err, listed)
	}
}

// ODCH-RT-07: `routine health` prints what the Health table shows — the last
// outcomes, the success rate over the finished runs, and runs and spend in a
// rolling window.
func TestAcceptance_RoutineHealth(t *testing.T) {
	cfgPath, _ := startReliabilityAcceptanceServer(t)
	out, err := runReliabilityCLI(t, cfgPath, "-f", "json", "routine", "health", "--days", "7")
	if err != nil {
		t.Fatalf("routine health: %v\n%s", err, out)
	}
	var got struct {
		WindowDays int `json:"window_days"`
		Routines   []struct {
			Slug         string   `json:"slug"`
			LastOutcomes []string `json:"last_outcomes"`
			SuccessPct   *int     `json:"success_pct"`
			Runs         int      `json:"runs"`
			SpendUSD     float64  `json:"spend_usd"`
		} `json:"routines"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if got.WindowDays != 7 || len(got.Routines) != 1 || got.Routines[0].Slug != "rel-routine" {
		t.Fatalf("health = %+v", got)
	}
	r := got.Routines[0]
	if len(r.LastOutcomes) != 2 || r.LastOutcomes[0] != "completed" || r.LastOutcomes[1] != "failed" || r.SuccessPct == nil || *r.SuccessPct != 50 || r.Runs != 2 || r.SpendUSD != 0.5 {
		t.Fatalf("rel-routine = %+v", r)
	}
	if out, err := runReliabilityCLI(t, cfgPath, "routine", "health", "--days", "0"); err == nil {
		t.Fatalf("--days 0 must be refused, got:\n%s", out)
	}
	if human, err := runReliabilityCLI(t, cfgPath, "routine", "health"); err != nil || !strings.Contains(human, "rel-routine") || !strings.Contains(human, "50%") {
		t.Fatalf("human output: %v\n%s", err, human)
	}
}

// ODCH-RT-06: `routine checkup` prints the server's checklist and exits
// non-zero while something needs fixing.
func TestAcceptance_RoutineCheckup(t *testing.T) {
	cfgPath, _ := startReliabilityAcceptanceServer(t)
	out, err := runReliabilityCLI(t, cfgPath, "-f", "json", "routine", "checkup", "rel-routine")
	// The seeded routine has no cost cap (a warning) and a failed last run;
	// nothing needs fixing, so the command succeeds.
	if err != nil {
		t.Fatalf("routine checkup: %v\n%s", err, out)
	}
	var rep struct {
		Slug   string `json:"slug"`
		Total  int    `json:"total"`
		Failed int    `json:"failed"`
		Checks []struct {
			Key   string `json:"key"`
			Level string `json:"level"`
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	levels := map[string]string{}
	for _, c := range rep.Checks {
		levels[c.Key] = c.Level
	}
	if rep.Slug != "rel-routine" || rep.Total != len(rep.Checks) || levels["runnable"] != "ok" || levels["cost"] != "warn" {
		t.Fatalf("checkup = %+v", rep)
	}
	if out, err := runReliabilityCLI(t, cfgPath, "routine", "checkup", "no-such-routine"); err == nil {
		t.Fatalf("an unknown routine must fail, got:\n%s", out)
	}
	if human, err := runReliabilityCLI(t, cfgPath, "routine", "checkup", "rel-routine"); err != nil || !strings.Contains(human, "Can run") {
		t.Fatalf("human output: %v\n%s", err, human)
	}
}

// ODCH-RT-22: `routine templates` keeps a definition, starts a DRAFT from it
// (never a live routine), and deletes it.
func TestAcceptance_RoutineTemplates(t *testing.T) {
	cfgPath, _ := startReliabilityAcceptanceServer(t)
	run := func(args ...string) string {
		t.Helper()
		out, err := runReliabilityCLI(t, cfgPath, append([]string{"-f", "json", "routine"}, args...)...)
		if err != nil {
			t.Fatalf("routine %v: %v\n%s", args, err, out)
		}
		return out
	}
	file := filepath.Join(t.TempDir(), "tpl.json")
	if err := os.WriteFile(file, []byte(`{"name":"digest","steps":[{"id":"a","type":"transform","transform":{"input":"{}","expression":"."}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var created struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		StepCount int    `json:"step_count"`
	}
	if err := json.Unmarshal([]byte(run("templates", "create", "Daily digest", "--file", file, "--category", "ops")), &created); err != nil || created.ID == "" || created.StepCount != 1 {
		t.Fatalf("create: %v %+v", err, created)
	}
	if out, err := runReliabilityCLI(t, cfgPath, "routine", "templates", "create", "Daily digest", "--file", file); err == nil {
		t.Fatalf("a duplicate name must be refused, got:\n%s", out)
	}
	if out, err := runReliabilityCLI(t, cfgPath, "routine", "templates", "create", "X"); err == nil {
		t.Fatalf("create without a source must fail, got:\n%s", out)
	}

	var listed []struct {
		ID       string `json:"id"`
		Category string `json:"category"`
	}
	if err := json.Unmarshal([]byte(run("templates", "list")), &listed); err != nil || len(listed) != 1 || listed[0].Category != "ops" {
		t.Fatalf("list: %v %+v", err, listed)
	}
	if out := run("templates", "get", created.ID); !strings.Contains(out, `"definition"`) {
		t.Fatalf("get has no definition:\n%s", out)
	}

	var draft struct {
		Slug     string `json:"slug"`
		Revision int    `json:"revision"`
	}
	if err := json.Unmarshal([]byte(run("templates", "use", created.ID, "--slug", "morning-digest")), &draft); err != nil || draft.Slug != "morning-digest" || draft.Revision != 1 {
		t.Fatalf("use: %v %+v", err, draft)
	}
	// It is a draft: the routine list does not have it.
	if out := run("list"); strings.Contains(out, "morning-digest") {
		t.Fatalf("a template must start a draft, not a live routine:\n%s", out)
	}
	if out, err := runReliabilityCLI(t, cfgPath, "routine", "templates", "use", created.ID, "--slug", "morning-digest"); err == nil {
		t.Fatalf("a taken slug must be refused, got:\n%s", out)
	}

	run("templates", "delete", created.ID)
	if err := json.Unmarshal([]byte(run("templates", "list")), &listed); err != nil || len(listed) != 0 {
		t.Fatalf("after delete: %v %+v", err, listed)
	}
}
