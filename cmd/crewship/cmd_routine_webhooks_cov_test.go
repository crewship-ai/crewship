package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const covWebhooksPath = "/api/v1/workspaces/" + covWorkspaceIDCli4 + "/pipeline-webhooks"

func covWebhookRow(id, name, slug, token string, secretSet, enabled bool) map[string]any {
	return map[string]any{
		"id": id, "workspace_id": covWorkspaceIDCli4, "name": name,
		"target_pipeline_id": "pl-1", "target_pipeline_slug": slug,
		"token": token, "signing_secret_set": secretSet,
		"enabled": enabled, "rate_limit_per_min": 60,
		"fire_count": 3, "last_status": "ok",
		"created_at": "2026-06-01T00:00:00Z", "updated_at": "2026-06-01T00:00:00Z",
	}
}

// ─── helpers under test ──────────────────────────────────────────────

func TestRedactedShort(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "****"},
		{"abcd", "****"},
		{"abcde", "***bcde"},
		{"whk_token_1234567890", "***7890"},
	}
	for _, tc := range cases {
		if got := redactedShort(tc.in); got != tc.want {
			t.Errorf("redactedShort(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestClientBaseURL_EnvFallbacks(t *testing.T) {
	// *cli.Client exposes BaseURL as a FIELD, not a method, so the
	// baseURLer interface probe never matches — the helper falls back
	// to env then the localhost default. This test pins that contract.
	c := cli.NewClient("http://stub:9999", "tok", "")

	t.Setenv("CREWSHIP_SERVER", "https://env.example.com")
	if got := clientBaseURL(c); got != "https://env.example.com" {
		t.Errorf("env fallback = %q", got)
	}

	t.Setenv("CREWSHIP_SERVER", "")
	if got := clientBaseURL(c); got != "http://localhost:8080" {
		t.Errorf("default fallback = %q", got)
	}
}

// ─── list ────────────────────────────────────────────────────────────

func declareWebhookListFlags(c *cobra.Command) {
	c.Flags().String("slug", "", "")
	c.Flags().Bool("json", false, "")
}

func TestRoutineWebhooksListRunE_EmptyState(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet(covWebhooksPath, clitest.JSONResponse(200, []map[string]any{}))

	c := covFreshCmd(routineWebhooksListCmd, declareWebhookListFlags)
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "No webhooks in this workspace.") {
		t.Errorf("empty-state line missing: %q", out)
	}
}

func TestRoutineWebhooksListRunE_TableAndSlugFilter(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet(covWebhooksPath, clitest.JSONResponse(200, []map[string]any{
		covWebhookRow("wh-1", "gh-prs", "pr-review", "tok_aaaa1111", true, true),
		covWebhookRow("wh-2", "summaries", "summarize-text", "tok_bbbb2222", false, false),
	}))

	c := covFreshCmd(routineWebhooksListCmd, declareWebhookListFlags)
	covSetFlagsCli4(t, c, map[string]string{"slug": "pr-review"})
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "gh-prs") || !strings.Contains(out, "pr-review") {
		t.Errorf("filtered row missing: %q", out)
	}
	if strings.Contains(out, "summaries") {
		t.Errorf("slug filter leaked other routine's webhook: %q", out)
	}
	// HMAC column rendering.
	if !strings.Contains(out, "yes") {
		t.Errorf("hmac yes missing: %q", out)
	}
}

func TestRoutineWebhooksListRunE_JSONRedactsToken(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet(covWebhooksPath, clitest.JSONResponse(200, []map[string]any{
		covWebhookRow("wh-1", "gh-prs", "pr-review", "tok_secret_abcd", true, true),
	}))

	c := covFreshCmd(routineWebhooksListCmd, declareWebhookListFlags)
	covSetFlagsCli4(t, c, map[string]string{"json": "true"})
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if strings.Contains(out, "tok_secret_abcd") {
		t.Errorf("full token leaked in --json output: %q", out)
	}
	if !strings.Contains(out, "***abcd") {
		t.Errorf("redacted token missing: %q", out)
	}
	var rows []webhookRow
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("--json output not parseable: %v", err)
	}
	if len(rows) != 1 || rows[0].SigningSecret != "" {
		t.Errorf("signing secret should be scrubbed: %+v", rows)
	}
}

// ─── create ──────────────────────────────────────────────────────────

func declareWebhookCreateFlags(c *cobra.Command) {
	c.Flags().String("slug", "", "")
	c.Flags().String("name", "", "")
	c.Flags().String("hmac-secret", "", "")
	c.Flags().Int("rate-limit", 60, "")
	c.Flags().String("inputs-template", "", "")
	c.Flags().String("base-url", "", "")
}

func TestRoutineWebhooksCreateRunE_SlugRequired(t *testing.T) {
	covSetupCli4(t)
	c := covFreshCmd(routineWebhooksCreateCmd, declareWebhookCreateFlags)
	if err := c.RunE(c, nil); err == nil || !strings.Contains(err.Error(), "--slug is required") {
		t.Fatalf("want slug-required, got %v", err)
	}
}

func TestRoutineWebhooksCreateRunE_BadInputsTemplate(t *testing.T) {
	covSetupCli4(t)
	c := covFreshCmd(routineWebhooksCreateCmd, declareWebhookCreateFlags)
	covSetFlagsCli4(t, c, map[string]string{"slug": "pr-review", "inputs-template": "{not json"})
	if err := c.RunE(c, nil); err == nil || !strings.Contains(err.Error(), "--inputs-template must be valid JSON") {
		t.Fatalf("want JSON validation error, got %v", err)
	}
}

func TestRoutineWebhooksCreateRunE_HappyPath_SecretRevealOnce(t *testing.T) {
	stub := covSetupCli4(t)
	created := covWebhookRow("wh-9", "github-prs", "pr-review", "tok_new_9999", true, true)
	created["signing_secret"] = "shhh-hmac-secret"
	stub.OnPost(covWebhooksPath, clitest.JSONResponse(201, created))

	c := covFreshCmd(routineWebhooksCreateCmd, declareWebhookCreateFlags)
	covSetFlagsCli4(t, c, map[string]string{
		"slug":            "pr-review",
		"hmac-secret":     "shhh-hmac-secret",
		"rate-limit":      "0", // <=0 must default to 60 in the request
		"inputs-template": `{"repo":"crewship"}`,
		"base-url":        "https://hooks.example.com/",
	})

	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Webhook created.") {
		t.Errorf("missing created line: %q", out)
	}
	if !strings.Contains(out, "https://hooks.example.com/api/v1/webhooks/tok_new_9999") {
		t.Errorf("public URL not printed with base-url override: %q", out)
	}
	if !strings.Contains(out, "shhh-hmac-secret") || !strings.Contains(out, "shown once") {
		t.Errorf("HMAC reveal block missing: %q", out)
	}

	calls := stub.CallsFor("POST", covWebhooksPath)
	if len(calls) != 1 {
		t.Fatalf("POST calls = %d, want 1", len(calls))
	}
	var body map[string]any
	if err := json.Unmarshal(calls[0].Body, &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["name"] != "pr-review webhook" {
		t.Errorf("default name = %v, want 'pr-review webhook'", body["name"])
	}
	if body["rate_limit_per_min"] != float64(60) {
		t.Errorf("rate limit should clamp to 60, got %v", body["rate_limit_per_min"])
	}
	if body["signing_secret"] != "shhh-hmac-secret" {
		t.Errorf("signing secret not forwarded: %v", body)
	}
	tpl, ok := body["inputs_template"].(map[string]any)
	if !ok || tpl["repo"] != "crewship" {
		t.Errorf("inputs_template = %v", body["inputs_template"])
	}
	if body["enabled"] != true {
		t.Errorf("enabled = %v, want true", body["enabled"])
	}
}

func TestRoutineWebhooksCreateRunE_MissingTokenInResponse(t *testing.T) {
	stub := covSetupCli4(t)
	created := covWebhookRow("wh-9", "github-prs", "pr-review", "", false, true)
	stub.OnPost(covWebhooksPath, clitest.JSONResponse(201, created))

	c := covFreshCmd(routineWebhooksCreateCmd, declareWebhookCreateFlags)
	covSetFlagsCli4(t, c, map[string]string{"slug": "pr-review"})
	_, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, nil) })
	if err == nil || !strings.Contains(err.Error(), "missing token") {
		t.Fatalf("want missing-token error, got %v", err)
	}
}

// ─── url ─────────────────────────────────────────────────────────────

func TestRoutineWebhooksUrlRunE_FoundAndNotFound(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet(covWebhooksPath, clitest.JSONResponse(200, []map[string]any{
		covWebhookRow("wh-1", "gh-prs", "pr-review", "tok url+escape", true, true),
	}))

	c := covFreshCmd(routineWebhooksUrlCmd, func(c *cobra.Command) {
		c.Flags().String("base-url", "", "")
	})
	covSetFlagsCli4(t, c, map[string]string{"base-url": "https://pub.example.com"})

	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{"wh-1"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	// Token must be path-escaped in the printed URL.
	if !strings.Contains(out, "https://pub.example.com/api/v1/webhooks/tok%20url+escape") {
		t.Errorf("escaped public URL missing: %q", out)
	}

	err = c.RunE(c, []string{"wh-missing"})
	if err == nil || !strings.Contains(err.Error(), "webhook wh-missing not found") {
		t.Fatalf("want not-found error, got %v", err)
	}
}

// ─── delete ──────────────────────────────────────────────────────────

func TestRoutineWebhooksDeleteRunE_YesDeletes(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnDelete(covWebhooksPath+"/wh-1", clitest.EmptyResponse(204))

	c := covFreshCmd(routineWebhooksDeleteCmd, func(c *cobra.Command) {
		c.Flags().Bool("yes", false, "")
	})
	covSetFlagsCli4(t, c, map[string]string{"yes": "true"})

	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{"wh-1"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Webhook wh-1 deleted.") {
		t.Errorf("missing deleted line: %q", out)
	}
	if got := len(stub.CallsFor("DELETE", covWebhooksPath+"/wh-1")); got != 1 {
		t.Errorf("DELETE calls = %d, want 1", got)
	}
}

func TestRoutineWebhooksDeleteRunE_RequiresTypedYes(t *testing.T) {
	stub := covSetupCli4(t)
	c := covFreshCmd(routineWebhooksDeleteCmd, func(c *cobra.Command) {
		c.Flags().Bool("yes", false, "")
	})
	// "y" alone is NOT enough — the prompt demands the full word "yes".
	err := covWithStdinCli4(t, "y\n", func() error {
		_, runErr := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{"wh-1"}) })
		return runErr
	})
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("want aborted on partial confirmation, got %v", err)
	}
	if got := len(stub.Calls()); got != 0 {
		t.Errorf("no API calls expected after abort, got %d", got)
	}
}

func TestRoutineWebhooksDeleteRunE_TypedYesConfirms(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnDelete(covWebhooksPath+"/wh-2", clitest.EmptyResponse(204))
	c := covFreshCmd(routineWebhooksDeleteCmd, func(c *cobra.Command) {
		c.Flags().Bool("yes", false, "")
	})
	err := covWithStdinCli4(t, "YES\n", func() error {
		_, runErr := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{"wh-2"}) })
		return runErr
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if got := len(stub.CallsFor("DELETE", covWebhooksPath+"/wh-2")); got != 1 {
		t.Errorf("DELETE calls = %d, want 1", got)
	}
}

// ─── remaining error branches ────────────────────────────────────────

func covWebhookCmds() map[string]*cobra.Command {
	return map[string]*cobra.Command{
		"list":   covFreshCmd(routineWebhooksListCmd, declareWebhookListFlags),
		"create": covFreshCmd(routineWebhooksCreateCmd, declareWebhookCreateFlags),
		"url":    covFreshCmd(routineWebhooksUrlCmd, func(c *cobra.Command) { c.Flags().String("base-url", "", "") }),
		"delete": covFreshCmd(routineWebhooksDeleteCmd, func(c *cobra.Command) { c.Flags().Bool("yes", false, "") }),
	}
}

func TestRoutineWebhooksGates_AuthAndWorkspace(t *testing.T) {
	cmds := covWebhookCmds()
	// create validates --slug before auth, so it needs the flag set.
	covSetFlagsCli4(t, cmds["create"], map[string]string{"slug": "x"})
	covSetFlagsCli4(t, cmds["delete"], map[string]string{"yes": "true"})
	for name, c := range cmds {
		t.Run(name+" no auth", func(t *testing.T) {
			saveCLIState(t)
			cliCfg = &cli.CLIConfig{}
			if err := c.RunE(c, []string{"wh-1"}); err == nil || !strings.Contains(err.Error(), "not logged in") {
				t.Errorf("want not-logged-in, got %v", err)
			}
		})
		t.Run(name+" no workspace", func(t *testing.T) {
			saveCLIState(t)
			t.Setenv("CREWSHIP_WORKSPACE", "")
			flagWorkspace = ""
			cliCfg = &cli.CLIConfig{Token: "tok"}
			if err := c.RunE(c, []string{"wh-1"}); err == nil || !strings.Contains(err.Error(), "no workspace set") {
				t.Errorf("want workspace error, got %v", err)
			}
		})
	}
}

func TestRoutineWebhooks_ServerAndTransportErrors(t *testing.T) {
	cmds := covWebhookCmds()
	covSetFlagsCli4(t, cmds["create"], map[string]string{"slug": "pr-review"})
	covSetFlagsCli4(t, cmds["delete"], map[string]string{"yes": "true"})

	methods := map[string]string{"list": "GET", "create": "POST", "url": "GET", "delete": "DELETE"}
	paths := map[string]string{
		"list": covWebhooksPath, "create": covWebhooksPath,
		"url": covWebhooksPath, "delete": covWebhooksPath + "/wh-1",
	}
	for name := range methods {
		t.Run(name+" server error", func(t *testing.T) {
			stub := covSetupCli4(t)
			stub.On(methods[name], paths[name], clitest.ErrorResponse(500, "webhooks exploded"))
			c := cmds[name]
			if err := c.RunE(c, []string{"wh-1"}); err == nil || !strings.Contains(err.Error(), "webhooks exploded") {
				t.Errorf("want server error surfaced, got %v", err)
			}
		})
		t.Run(name+" transport error", func(t *testing.T) {
			covDeadServerCli4(t)
			c := cmds[name]
			if err := c.RunE(c, []string{"wh-1"}); err == nil {
				t.Error("want transport error against dead server")
			}
		})
	}
}

func TestRoutineWebhooks_DecodeErrors(t *testing.T) {
	cmds := covWebhookCmds()
	covSetFlagsCli4(t, cmds["create"], map[string]string{"slug": "pr-review"})

	t.Run("list", func(t *testing.T) {
		stub := covSetupCli4(t)
		stub.OnGet(covWebhooksPath, clitest.TextResponse(200, "not-json"))
		c := cmds["list"]
		if err := c.RunE(c, nil); err == nil || !strings.Contains(err.Error(), "decode response") {
			t.Errorf("want decode error, got %v", err)
		}
	})
	t.Run("url", func(t *testing.T) {
		stub := covSetupCli4(t)
		stub.OnGet(covWebhooksPath, clitest.TextResponse(200, "not-json"))
		c := cmds["url"]
		if err := c.RunE(c, []string{"wh-1"}); err == nil || !strings.Contains(err.Error(), "decode response") {
			t.Errorf("want decode error, got %v", err)
		}
	})
	t.Run("create decode failure tells user to verify", func(t *testing.T) {
		stub := covSetupCli4(t)
		stub.OnPost(covWebhooksPath, clitest.TextResponse(201, "not-json"))
		c := cmds["create"]
		err := c.RunE(c, nil)
		if err == nil || !strings.Contains(err.Error(), "may have been created") ||
			!strings.Contains(err.Error(), "webhooks list --slug pr-review") {
			t.Errorf("want verify-hint error, got %v", err)
		}
	})
}

func TestRoutineWebhooksUrlRunE_DefaultBaseURL(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet(covWebhooksPath, clitest.JSONResponse(200, []map[string]any{
		covWebhookRow("wh-1", "gh", "pr", "tok123", false, true),
	}))
	// No --base-url flag, CREWSHIP_SERVER cleared by covSetupCli4, and
	// cli.Client has no BaseURL() method — the printed URL must fall
	// back to the localhost default.
	c := covFreshCmd(routineWebhooksUrlCmd, func(c *cobra.Command) { c.Flags().String("base-url", "", "") })
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{"wh-1"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "http://localhost:8080/api/v1/webhooks/tok123") {
		t.Errorf("default base URL missing: %q", out)
	}
}

func TestRoutineWebhooksCreateRunE_DefaultBaseURLAndNoSecret(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnPost(covWebhooksPath, clitest.JSONResponse(201, covWebhookRow("wh-3", "n", "summarize", "tok456", false, true)))
	c := covFreshCmd(routineWebhooksCreateCmd, declareWebhookCreateFlags)
	covSetFlagsCli4(t, c, map[string]string{"slug": "summarize", "name": "custom-name"})
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "http://localhost:8080/api/v1/webhooks/tok456") {
		t.Errorf("default base URL missing: %q", out)
	}
	if strings.Contains(out, "HMAC signing secret") {
		t.Errorf("no secret reveal expected without signing_secret: %q", out)
	}
	calls := stub.CallsFor("POST", covWebhooksPath)
	if len(calls) != 1 || !strings.Contains(string(calls[0].Body), `"name":"custom-name"`) {
		t.Errorf("custom name not forwarded: %v", calls)
	}
}
