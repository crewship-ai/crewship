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
