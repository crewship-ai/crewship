package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// declareTokenEmitFlags adds the secret-output flags emitToken reads.
func declareTokenEmitFlags(c *cobra.Command) {
	c.Flags().String("output-file", "", "")
	c.Flags().Bool("quiet", false, "")
}

// covCmdWithBuffers returns a fresh command wired to in-memory out/err.
func covCmdWithBuffers(src *cobra.Command, declare func(*cobra.Command)) (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	c := covFreshCmd(src, declare)
	var out, errOut bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&errOut)
	return c, &out, &errOut
}

func TestEmitToken_OutputFileOpenError(t *testing.T) {
	c, _, _ := covCmdWithBuffers(tokenCreateCmd, declareTokenEmitFlags)
	// Directory does not exist → OpenFile must fail and wrap the path.
	bad := filepath.Join(t.TempDir(), "missing-dir", "token.txt")
	if err := c.Flags().Set("output-file", bad); err != nil {
		t.Fatal(err)
	}
	err := emitToken(c, "n", "id", "tok_x")
	if err == nil || !strings.Contains(err.Error(), "write token to "+bad) {
		t.Fatalf("want write error mentioning path, got %v", err)
	}
}

// ─── token list ──────────────────────────────────────────────────────

func covTokenListPayload(now time.Time) map[string]any {
	old := now.Add(-120 * 24 * time.Hour).Format(time.RFC3339)
	fresh := now.Add(-time.Hour).Format(time.RFC3339)
	revoked := now.Add(-time.Hour).Format(time.RFC3339)
	// IDs/names deliberately avoid the literal status words so the
	// assertions below match only the STATUS column.
	return map[string]any{
		"data": []map[string]any{
			{"id": "tok_aaa_0123456789abc", "name": "fresh-one", "created_at": fresh, "last_used_at": fresh},
			{"id": "tok_bbb_0123456789abc", "name": "old-one", "created_at": old, "last_used_at": old},
			{"id": "tok_ccc_0123456789abc", "name": "untouched-one", "created_at": old, "last_used_at": nil},
			{"id": "tok_ddd_0123456789abc", "name": "dead-one", "created_at": old, "last_used_at": old, "revoked_at": revoked},
		},
	}
}

func TestTokenListRunE_StatusClassificationAndFooter(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/auth/cli-tokens", clitest.JSONResponse(200, covTokenListPayload(time.Now().UTC())))

	c, _, errBuf := covCmdWithBuffers(tokenListCmd, func(c *cobra.Command) {
		c.Flags().Int("warn-stale-days", 90, "")
	})

	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{"active", "stale", "unused", "revoked"} {
		if !strings.Contains(out, want) {
			t.Errorf("status %q missing from table: %q", want, out)
		}
	}
	// IDs truncated to 12 chars in the table.
	if !strings.Contains(out, "tok_aaa_0123") || strings.Contains(out, "tok_aaa_0123456789abc") {
		t.Errorf("ID truncation wrong: %q", out)
	}
	// Footer counts the 2 stale/unused tokens (revoked doesn't count).
	if !strings.Contains(errBuf.String(), "2 stale/unused token(s) found.") {
		t.Errorf("stale footer missing: %q", errBuf.String())
	}

	// Workspace must NOT be injected for auth-scoped endpoints.
	calls := stub.CallsFor("GET", "/api/v1/auth/cli-tokens")
	if len(calls) != 1 {
		t.Fatalf("list calls = %d", len(calls))
	}
	if strings.Contains(calls[0].Query, "workspace_id") {
		t.Errorf("workspace_id leaked into token list query: %q", calls[0].Query)
	}
}

func TestTokenListRunE_DisabledStaleCheckNoFooter(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/auth/cli-tokens", clitest.JSONResponse(200, covTokenListPayload(time.Now().UTC())))

	c, _, errBuf := covCmdWithBuffers(tokenListCmd, func(c *cobra.Command) {
		c.Flags().Int("warn-stale-days", 90, "")
	})
	if err := c.Flags().Set("warn-stale-days", "0"); err != nil {
		t.Fatal(err)
	}
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if strings.Contains(out, "stale") || strings.Contains(out, "unused") {
		t.Errorf("stale classification should be disabled: %q", out)
	}
	if errBuf.Len() != 0 {
		t.Errorf("no footer expected when disabled: %q", errBuf.String())
	}
}

// ─── token create ────────────────────────────────────────────────────

func TestTokenCreateRunE_QuietEmitsBareToken(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnPost("/api/v1/auth/cli-token", clitest.JSONResponse(201, map[string]string{
		"token": "tok_brand_new_secret", "id": "tid-1", "name": "ci-runner",
	}))

	c, out, _ := covCmdWithBuffers(tokenCreateCmd, declareTokenEmitFlags)
	if err := c.Flags().Set("quiet", "true"); err != nil {
		t.Fatal(err)
	}
	if err := c.RunE(c, []string{"ci-runner"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "tok_brand_new_secret" {
		t.Errorf("quiet stdout = %q, want bare token", got)
	}

	calls := stub.CallsFor("POST", "/api/v1/auth/cli-token")
	if len(calls) != 1 {
		t.Fatalf("create calls = %d", len(calls))
	}
	if !strings.Contains(string(calls[0].Body), `"name":"ci-runner"`) {
		t.Errorf("name not forwarded: %s", calls[0].Body)
	}
}

func TestTokenCreateRunE_DefaultName(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnPost("/api/v1/auth/cli-token", clitest.JSONResponse(201, map[string]string{
		"token": "tok_x", "id": "tid-2", "name": "CLI token",
	}))
	c, _, _ := covCmdWithBuffers(tokenCreateCmd, declareTokenEmitFlags)
	if err := c.RunE(c, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := stub.CallsFor("POST", "/api/v1/auth/cli-token")
	if len(calls) != 1 || !strings.Contains(string(calls[0].Body), `"name":"CLI token"`) {
		t.Errorf("default name not used: %v", calls)
	}
}

// ─── token revoke ────────────────────────────────────────────────────

func TestTokenRevokeRunE_YesRevokes(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnDelete("/api/v1/auth/cli-tokens/tid-9", clitest.EmptyResponse(204))

	c := covFreshCmd(tokenRevokeCmd, func(c *cobra.Command) {
		c.Flags().BoolP("yes", "y", false, "")
	})
	covSetFlagsCli4(t, c, map[string]string{"yes": "true"})

	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{"tid-9"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Token revoked.") {
		t.Errorf("success line missing: %q", out)
	}
	if got := len(stub.CallsFor("DELETE", "/api/v1/auth/cli-tokens/tid-9")); got != 1 {
		t.Errorf("DELETE calls = %d, want 1", got)
	}
}

// ─── token rotate ────────────────────────────────────────────────────

func covRotateListResponse() map[string]any {
	return map[string]any{
		"data": []map[string]any{
			{"id": "tid-old", "name": "ci-runner", "revoked_at": nil},
			{"id": "tid-dead", "name": "gone", "revoked_at": "2026-06-01T00:00:00Z"},
		},
	}
}

func TestTokenRotateRunE_HappyPath(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/auth/cli-tokens", clitest.JSONResponse(200, covRotateListResponse()))
	stub.OnPost("/api/v1/auth/cli-token", clitest.JSONResponse(201, map[string]string{
		"token": "tok_rotated_secret", "id": "tid-new", "name": "ci-runner (rotated)",
	}))
	stub.OnDelete("/api/v1/auth/cli-tokens/tid-old", clitest.EmptyResponse(204))

	c, out, errOut := covCmdWithBuffers(tokenRotateCmd, func(c *cobra.Command) {
		declareTokenEmitFlags(c)
		c.Flags().String("name", "", "")
	})

	transcript, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{"tid-old"}) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(transcript, "Old token tid-old revoked.") {
		t.Errorf("revoke confirmation missing: %q", transcript)
	}
	// New bearer goes to the command's stderr (default emit mode).
	if !strings.Contains(errOut.String(), "tok_rotated_secret") {
		t.Errorf("rotated token missing from stderr: %q", errOut.String())
	}
	if strings.Contains(out.String(), "tok_rotated_secret") {
		t.Errorf("rotated token leaked to stdout: %q", out.String())
	}

	// Create body carries the old name + rotation timestamp suffix.
	creates := stub.CallsFor("POST", "/api/v1/auth/cli-token")
	if len(creates) != 1 {
		t.Fatalf("create calls = %d", len(creates))
	}
	var body map[string]string
	_ = json.Unmarshal(creates[0].Body, &body)
	wantPrefix := "ci-runner (rotated " + time.Now().UTC().Format("2006-01-02")
	if !strings.HasPrefix(body["name"], wantPrefix) {
		t.Errorf("rotated name = %q, want prefix %q", body["name"], wantPrefix)
	}
	if got := len(stub.CallsFor("DELETE", "/api/v1/auth/cli-tokens/tid-old")); got != 1 {
		t.Errorf("old token revoke calls = %d, want 1", got)
	}
}

func TestTokenRotateRunE_NotFoundAndAlreadyRevoked(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/auth/cli-tokens", clitest.JSONResponse(200, covRotateListResponse()))

	c, _, _ := covCmdWithBuffers(tokenRotateCmd, func(c *cobra.Command) {
		declareTokenEmitFlags(c)
		c.Flags().String("name", "", "")
	})

	err := c.RunE(c, []string{"tid-ghost"})
	if err == nil || !strings.Contains(err.Error(), "token tid-ghost not found") {
		t.Fatalf("want not-found, got %v", err)
	}

	err = c.RunE(c, []string{"tid-dead"})
	if err == nil || !strings.Contains(err.Error(), "already revoked") {
		t.Fatalf("want already-revoked, got %v", err)
	}
}

func TestTokenRotateRunE_RevokeFailureKeepsNewToken(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/auth/cli-tokens", clitest.JSONResponse(200, covRotateListResponse()))
	stub.OnPost("/api/v1/auth/cli-token", clitest.JSONResponse(201, map[string]string{
		"token": "tok_half_rotated", "id": "tid-new", "name": "x",
	}))
	stub.OnDelete("/api/v1/auth/cli-tokens/tid-old", clitest.ErrorResponse(500, "revoke exploded"))

	c, _, errOut := covCmdWithBuffers(tokenRotateCmd, func(c *cobra.Command) {
		declareTokenEmitFlags(c)
		c.Flags().String("name", "", "")
	})
	covSetFlagsCli4(t, c, map[string]string{"name": "custom-name"})

	_, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, []string{"tid-old"}) })
	if err == nil || !strings.Contains(err.Error(), "new token IS active") {
		t.Fatalf("want loud revoke-failure error, got %v", err)
	}
	// The new bearer must still have been emitted before the failure.
	if !strings.Contains(errOut.String(), "tok_half_rotated") {
		t.Errorf("new token must be emitted before revoke: %q", errOut.String())
	}
	// --name override skips the timestamp suffix.
	creates := stub.CallsFor("POST", "/api/v1/auth/cli-token")
	if len(creates) != 1 || !strings.Contains(string(creates[0].Body), `"name":"custom-name"`) {
		t.Errorf("--name override not honoured: %v", creates)
	}
}

// ─── token validate ──────────────────────────────────────────────────

func TestTokenValidateRunE_ValidHuman(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/auth/cli-token/validate", clitest.JSONResponse(200, map[string]any{
		"valid": true, "user_id": "u1", "email": "demo@crewship.ai", "expires_at": "2027-01-01T00:00:00Z",
	}))

	c, _, _ := covCmdWithBuffers(tokenValidateCmd, func(c *cobra.Command) {
		c.Flags().Bool("json", false, "")
	})
	out, err := covCaptureStdoutCli4(t, func() error { return c.RunE(c, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Token is valid.") || !strings.Contains(out, "demo@crewship.ai") || !strings.Contains(out, "2027-01-01") {
		t.Errorf("human output missing: %q", out)
	}
}

func TestTokenValidateRunE_ValidJSON(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/auth/cli-token/validate", clitest.JSONResponse(200, map[string]any{
		"valid": true, "user_id": "u1", "email": "demo@crewship.ai", "expires_at": "",
	}))

	c, out, _ := covCmdWithBuffers(tokenValidateCmd, func(c *cobra.Command) {
		c.Flags().Bool("json", false, "")
	})
	if err := c.Flags().Set("json", "true"); err != nil {
		t.Fatal(err)
	}
	if err := c.RunE(c, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	var decoded struct {
		Valid bool   `json:"valid"`
		Email string `json:"email"`
	}
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("json output unparseable: %v (%q)", err, out.String())
	}
	if !decoded.Valid || decoded.Email != "demo@crewship.ai" {
		t.Errorf("decoded = %+v", decoded)
	}
}

func TestTokenValidateRunE_401(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/auth/cli-token/validate", clitest.ErrorResponse(401, "nope"))

	c, out, _ := covCmdWithBuffers(tokenValidateCmd, func(c *cobra.Command) {
		c.Flags().Bool("json", false, "")
	})
	if err := c.Flags().Set("json", "true"); err != nil {
		t.Fatal(err)
	}
	err := c.RunE(c, nil)
	if err == nil || !strings.Contains(err.Error(), "token is invalid or expired") {
		t.Fatalf("want invalid-token error, got %v", err)
	}
	if !strings.Contains(out.String(), `"valid":false`) {
		t.Errorf("json mode should emit valid:false on 401: %q", out.String())
	}
}

func TestTokenValidateRunE_ServerSaysInvalid(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/auth/cli-token/validate", clitest.JSONResponse(200, map[string]any{
		"valid": false,
	}))
	c, _, _ := covCmdWithBuffers(tokenValidateCmd, func(c *cobra.Command) {
		c.Flags().Bool("json", false, "")
	})
	err := c.RunE(c, nil)
	if err == nil || err.Error() != "token is invalid" {
		t.Fatalf("want 'token is invalid', got %v", err)
	}
}

// ─── remaining error branches ────────────────────────────────────────

func TestTokenGates_NoAuth(t *testing.T) {
	builders := map[string]func() *cobra.Command{
		"list": func() *cobra.Command {
			return covFreshCmd(tokenListCmd, func(c *cobra.Command) { c.Flags().Int("warn-stale-days", 90, "") })
		},
		"create": func() *cobra.Command { return covFreshCmd(tokenCreateCmd, declareTokenEmitFlags) },
		"revoke": func() *cobra.Command {
			return covFreshCmd(tokenRevokeCmd, func(c *cobra.Command) { c.Flags().BoolP("yes", "y", false, "") })
		},
		"rotate": func() *cobra.Command {
			return covFreshCmd(tokenRotateCmd, func(c *cobra.Command) {
				declareTokenEmitFlags(c)
				c.Flags().String("name", "", "")
			})
		},
		"validate": func() *cobra.Command {
			return covFreshCmd(tokenValidateCmd, func(c *cobra.Command) { c.Flags().Bool("json", false, "") })
		},
	}
	for name, build := range builders {
		t.Run(name, func(t *testing.T) {
			saveCLIState(t)
			cliCfg = &cli.CLIConfig{}
			c := build()
			if err := c.RunE(c, []string{"tid-1"}); err == nil || !strings.Contains(err.Error(), "not logged in") {
				t.Errorf("want not-logged-in, got %v", err)
			}
		})
	}
}

func TestTokenList_TransportServerDecodeErrors(t *testing.T) {
	c := covFreshCmd(tokenListCmd, func(c *cobra.Command) { c.Flags().Int("warn-stale-days", 90, "") })

	covDeadServerCli4(t)
	if err := c.RunE(c, nil); err == nil {
		t.Error("want transport error")
	}

	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/auth/cli-tokens", clitest.ErrorResponse(500, "tokens exploded"))
	if err := c.RunE(c, nil); err == nil || !strings.Contains(err.Error(), "tokens exploded") {
		t.Errorf("want server error, got %v", err)
	}

	stub.OnGet("/api/v1/auth/cli-tokens", clitest.TextResponse(200, "not-json"))
	if err := c.RunE(c, nil); err == nil {
		t.Error("want decode error")
	}
}

func TestTokenCreate_TransportServerDecodeErrors(t *testing.T) {
	c := covFreshCmd(tokenCreateCmd, declareTokenEmitFlags)

	covDeadServerCli4(t)
	if err := c.RunE(c, nil); err == nil {
		t.Error("want transport error")
	}

	stub := covSetupCli4(t)
	stub.OnPost("/api/v1/auth/cli-token", clitest.ErrorResponse(403, "create denied"))
	if err := c.RunE(c, nil); err == nil || !strings.Contains(err.Error(), "create denied") {
		t.Errorf("want server error, got %v", err)
	}

	stub.OnPost("/api/v1/auth/cli-token", clitest.TextResponse(201, "not-json"))
	if err := c.RunE(c, nil); err == nil {
		t.Error("want decode error")
	}
}

func TestTokenRevoke_AbortAndErrors(t *testing.T) {
	c := covFreshCmd(tokenRevokeCmd, func(c *cobra.Command) { c.Flags().BoolP("yes", "y", false, "") })

	// Piped "n" aborts before any API call.
	stub := covSetupCli4(t)
	err := covWithStdinCli4(t, "n\n", func() error { return c.RunE(c, []string{"tid-1"}) })
	if err == nil || err.Error() != "aborted" {
		t.Fatalf("want aborted, got %v", err)
	}
	if len(stub.Calls()) != 0 {
		t.Errorf("no API calls expected on abort, got %d", len(stub.Calls()))
	}

	covSetFlagsCli4(t, c, map[string]string{"yes": "true"})
	stub.OnDelete("/api/v1/auth/cli-tokens/tid-1", clitest.ErrorResponse(404, "no such token"))
	if err := c.RunE(c, []string{"tid-1"}); err == nil || !strings.Contains(err.Error(), "no such token") {
		t.Errorf("want server error, got %v", err)
	}

	covDeadServerCli4(t)
	if err := c.RunE(c, []string{"tid-1"}); err == nil {
		t.Error("want transport error")
	}
}

func TestTokenRotate_ListPhaseErrors(t *testing.T) {
	c := covFreshCmd(tokenRotateCmd, func(c *cobra.Command) {
		declareTokenEmitFlags(c)
		c.Flags().String("name", "", "")
	})

	covDeadServerCli4(t)
	if err := c.RunE(c, []string{"tid-old"}); err == nil || !strings.Contains(err.Error(), "list tokens") {
		t.Errorf("want list-phase transport error, got %v", err)
	}

	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/auth/cli-tokens", clitest.ErrorResponse(500, "list exploded"))
	if err := c.RunE(c, []string{"tid-old"}); err == nil || !strings.Contains(err.Error(), "list exploded") {
		t.Errorf("want list server error, got %v", err)
	}

	stub.OnGet("/api/v1/auth/cli-tokens", clitest.TextResponse(200, "not-json"))
	if err := c.RunE(c, []string{"tid-old"}); err == nil || !strings.Contains(err.Error(), "parse token list") {
		t.Errorf("want parse error, got %v", err)
	}
}

func TestTokenRotate_CreatePhaseErrors(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/auth/cli-tokens", clitest.JSONResponse(200, covRotateListResponse()))

	c := covFreshCmd(tokenRotateCmd, func(c *cobra.Command) {
		declareTokenEmitFlags(c)
		c.Flags().String("name", "", "")
	})

	stub.OnPost("/api/v1/auth/cli-token", clitest.ErrorResponse(403, "quota reached"))
	if err := c.RunE(c, []string{"tid-old"}); err == nil || !strings.Contains(err.Error(), "quota reached") {
		t.Errorf("want create server error, got %v", err)
	}

	stub.OnPost("/api/v1/auth/cli-token", clitest.TextResponse(201, "not-json"))
	if err := c.RunE(c, []string{"tid-old"}); err == nil || !strings.Contains(err.Error(), "parse new token") {
		t.Errorf("want parse-new-token error, got %v", err)
	}
}

func TestTokenValidate_TransportServerDecodeErrors(t *testing.T) {
	c := covFreshCmd(tokenValidateCmd, func(c *cobra.Command) { c.Flags().Bool("json", false, "") })

	covDeadServerCli4(t)
	if err := c.RunE(c, nil); err == nil {
		t.Error("want transport error")
	}

	// Non-401 server error goes through CheckError.
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/auth/cli-token/validate", clitest.ErrorResponse(500, "validate exploded"))
	if err := c.RunE(c, nil); err == nil || !strings.Contains(err.Error(), "validate exploded") {
		t.Errorf("want server error, got %v", err)
	}

	stub.OnGet("/api/v1/auth/cli-token/validate", clitest.TextResponse(200, "not-json"))
	if err := c.RunE(c, nil); err == nil {
		t.Error("want decode error")
	}
}

func TestTokenValidate_InvalidWithJSONOutput(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/auth/cli-token/validate", clitest.JSONResponse(200, map[string]any{"valid": false}))
	c, out, _ := covCmdWithBuffers(tokenValidateCmd, func(c *cobra.Command) { c.Flags().Bool("json", false, "") })
	if err := c.Flags().Set("json", "true"); err != nil {
		t.Fatal(err)
	}
	err := c.RunE(c, nil)
	if err == nil || err.Error() != "token is invalid" {
		t.Fatalf("want token-is-invalid, got %v", err)
	}
	if !strings.Contains(out.String(), `"valid":false`) {
		t.Errorf("json mode should emit the invalid payload: %q", out.String())
	}
}

func TestTokenValidate_401Plain(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/auth/cli-token/validate", clitest.ErrorResponse(403, "forbidden"))
	c, out, _ := covCmdWithBuffers(tokenValidateCmd, func(c *cobra.Command) { c.Flags().Bool("json", false, "") })
	err := c.RunE(c, nil)
	if err == nil || !strings.Contains(err.Error(), "token is invalid or expired") {
		t.Fatalf("want invalid-token error on 403, got %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("non-json mode must not write to stdout: %q", out.String())
	}
}
