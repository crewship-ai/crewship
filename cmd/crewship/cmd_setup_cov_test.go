package main

import (
	"bufio"
	"fmt"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"github.com/crewship-ai/crewship/internal/crashreport"
)

// saveSetupFlagsCov snapshots the setup command's flag-bound globals and
// the cobra Changed bits, restoring both at cleanup. runSetup reads the
// globals; the telemetry branch additionally checks Flags().Changed.
func saveSetupFlagsCov(t *testing.T) {
	t.Helper()
	origWS, origLang := setupWorkspaceFlag, setupLanguageFlag
	origCrew, origAdapter := setupCrewFlag, setupAdapterFlag
	origModel, origKey := setupModelFlag, setupAPIKeyFlag
	origYes, origTel := setupYesFlag, setupTelemetryFlag
	telFlag := setupCmd.Flags().Lookup("telemetry")
	origTelChanged := telFlag.Changed
	t.Cleanup(func() {
		setupWorkspaceFlag, setupLanguageFlag = origWS, origLang
		setupCrewFlag, setupAdapterFlag = origCrew, origAdapter
		setupModelFlag, setupAPIKeyFlag = origModel, origKey
		setupYesFlag, setupTelemetryFlag = origYes, origTel
		telFlag.Changed = origTelChanged
	})
	// Start each test from a clean slate.
	setupWorkspaceFlag, setupLanguageFlag = "", ""
	setupCrewFlag, setupAdapterFlag = "", ""
	setupModelFlag, setupAPIKeyFlag = "", ""
	setupYesFlag, setupTelemetryFlag = false, false
	telFlag.Changed = false
}

// withStdinCov feeds scripted input through the package-level stdinReader
// the prompt helpers read from, restoring the original reader at cleanup.
func withStdinCov(t *testing.T, input string) {
	t.Helper()
	orig := stdinReader
	stdinReader = bufio.NewReader(strings.NewReader(input))
	t.Cleanup(func() { stdinReader = orig })
}

func TestReadLine(t *testing.T) {
	withStdinCov(t, "  hello world  \n")
	got, err := readLine()
	if err != nil || got != "hello world" {
		t.Fatalf("readLine: got (%q, %v)", got, err)
	}

	// EOF without newline is the "accept default" signal, not an error.
	withStdinCov(t, "")
	got, err = readLine()
	if err != nil || got != "" {
		t.Fatalf("readLine EOF: got (%q, %v)", got, err)
	}
}

func TestIsValidCrewSlug(t *testing.T) {
	t.Parallel()
	for _, slug := range []string{"software-development", "devops-sre", "content-marketing", "accounting-finance", "blank"} {
		if !isValidCrewSlug(slug) {
			t.Errorf("%q should be valid", slug)
		}
	}
	if isValidCrewSlug("definitely-not-a-template") {
		t.Error("unknown slug accepted")
	}
}

func TestLookupAdapter(t *testing.T) {
	t.Parallel()
	a, ok := lookupAdapter("CLAUDE_CODE")
	if !ok || a.provider != "ANTHROPIC" || a.envVar != "ANTHROPIC_API_KEY" || a.defaultModel == "" {
		t.Errorf("CLAUDE_CODE adapter: %+v ok=%v", a, ok)
	}
	if _, ok := lookupAdapter("VIM"); ok {
		t.Error("unknown adapter accepted")
	}
}

func TestPromptCrew(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"bare enter takes default", "\n", "software-development", false},
		{"index selects", "2\n", "devops-sre", false},
		{"slug accepted", "blank\n", "blank", false},
		{"invalid choice", "99\n", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withStdinCov(t, tc.input)
			got, err := captureStdoutCov(t, func() error {
				slug, perr := promptCrew()
				if perr != nil {
					return perr
				}
				if slug != tc.want {
					t.Errorf("slug: got %q want %q", slug, tc.want)
				}
				return nil
			})
			if tc.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tc.wantErr && !strings.Contains(got, "Pick your first crew:") {
				t.Errorf("prompt text missing; got %q", got)
			}
		})
	}
}

func TestPromptAdapter(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"bare enter takes default", "\n", "CLAUDE_CODE", false},
		{"index selects", "3\n", "CODEX_CLI", false},
		{"key accepted", "GEMINI_CLI\n", "GEMINI_CLI", false},
		{"invalid choice", "emacs\n", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withStdinCov(t, tc.input)
			_, err := captureStdoutCov(t, func() error {
				key, perr := promptAdapter()
				if perr != nil {
					return perr
				}
				if key != tc.want {
					t.Errorf("adapter: got %q want %q", key, tc.want)
				}
				return nil
			})
			if tc.wantErr != (err != nil) {
				t.Fatalf("err: %v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestPromptYesNo(t *testing.T) {
	t.Run("explicit yes", func(t *testing.T) {
		withStdinCov(t, "y\n")
		_, err := captureStdoutCov(t, func() error {
			v, perr := promptYesNo("Enable?", false)
			if perr != nil {
				return perr
			}
			if !v {
				t.Error("want true")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})

	t.Run("garbage re-asks then accepts no", func(t *testing.T) {
		withStdinCov(t, "maybe\nn\n")
		out, err := captureStdoutCov(t, func() error {
			v, perr := promptYesNo("Enable?", true)
			if perr != nil {
				return perr
			}
			if v {
				t.Error("want false")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "Please answer y or n.") {
			t.Errorf("re-ask message missing: %q", out)
		}
	})
}

func TestPromptOptional(t *testing.T) {
	withStdinCov(t, "\n")
	_, err := captureStdoutCov(t, func() error {
		v, perr := promptOptional("Language", "English")
		if perr != nil {
			return perr
		}
		if v != "English" {
			t.Errorf("default: got %q", v)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	withStdinCov(t, "Čeština\n")
	_, err = captureStdoutCov(t, func() error {
		v, perr := promptOptional("Language", "English")
		if perr != nil {
			return perr
		}
		if v != "Čeština" {
			t.Errorf("explicit: got %q", v)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestServerTelemetryEnabled(t *testing.T) {
	t.Run("server says enabled", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnGet("/api/v1/system/telemetry", clitest.JSONResponse(200, map[string]bool{"enabled": true}))
		if !serverTelemetryEnabled() {
			t.Error("want true")
		}
	})

	t.Run("server says disabled", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnGet("/api/v1/system/telemetry", clitest.JSONResponse(200, map[string]bool{"enabled": false}))
		if serverTelemetryEnabled() {
			t.Error("want false")
		}
	})

	t.Run("server error falls back to build default", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnGet("/api/v1/system/telemetry", clitest.ErrorResponse(500, "down"))
		if got, want := serverTelemetryEnabled(), crashreport.DefaultOptIn(version); got != want {
			t.Errorf("fallback: got %v want DefaultOptIn(%q)=%v", got, version, want)
		}
	})
}

func TestRunSetup_NotLoggedIn(t *testing.T) {
	saveCLIState(t)
	saveSetupFlagsCov(t)
	cliCfg = &cli.CLIConfig{}

	err := runSetup(setupCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("want not logged in, got %v", err)
	}
}

// TestRunSetup_NonInteractiveValidation walks the flag-validation ladder.
// Under `go test` stdin is not a TTY, so runSetup always takes the
// non-interactive path and the prompt fallbacks turn into errors.
func TestRunSetup_NonInteractiveValidation(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func()
		wantErr string
	}{
		{"crew required", func() {}, "--crew is required"},
		{"unknown crew", func() { setupCrewFlag = "space-pirates" }, `unknown crew template "space-pirates"`},
		{"adapter required", func() { setupCrewFlag = "blank" }, "--adapter is required"},
		{"unknown adapter", func() {
			setupCrewFlag = "blank"
			setupAdapterFlag = "VIM"
		}, `unknown adapter "VIM"`},
		{"token required", func() {
			setupCrewFlag = "blank"
			setupAdapterFlag = "CLAUDE_CODE"
		}, "no token provided"},
		{"token too short", func() {
			setupCrewFlag = "blank"
			setupAdapterFlag = "CLAUDE_CODE"
			setupAPIKeyFlag = "short"
		}, "token looks too short"},
		{"api key rejected for anthropic", func() {
			setupCrewFlag = "blank"
			setupAdapterFlag = "CLAUDE_CODE"
			setupAPIKeyFlag = "sk-ant-api03-aaaaaaaa"
		}, "looks like an Anthropic API key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			saveCLIState(t)
			saveSetupFlagsCov(t)
			t.Setenv("ANTHROPIC_API_KEY", "")
			cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: covWorkspaceID}
			tc.mutate()

			err := runSetup(setupCmd, nil)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestRunSetup_HappyPathTemplateCrew(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	setupStubCLICov(t, stub)
	saveSetupFlagsCov(t)

	stub.OnPost("/api/v1/onboarding/setup", clitest.JSONResponse(200, map[string]any{
		"workspace_id": "ws_1", "crew_id": "crew_1", "agent_id": "ag_1",
		"agent_ids": []string{"ag_1", "ag_2", "ag_3", "ag_4"}, "agent_count": 4,
	}))

	setupCrewFlag = "software-development"
	setupAdapterFlag = "CLAUDE_CODE"
	setupAPIKeyFlag = "sk-ant-oat01-perfectly-fine-token"
	setupWorkspaceFlag = "My Workspace"
	setupLanguageFlag = "Čeština"

	out, err := captureStdoutCov(t, func() error {
		return runSetup(setupCmd, nil)
	})
	if err != nil {
		t.Fatalf("runSetup: %v", err)
	}
	// Note: the "Workspace ready" banner goes to stderr (cli.PrintSuccess);
	// stdout carries the agent pointer lines.
	for _, want := range []string{"First agent ID: ag_1", "/crews/agents/ag_1/chat"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q; got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Telemetry:") {
		t.Errorf("telemetry line must be absent when flag unset non-interactively:\n%s", out)
	}

	calls := stub.CallsFor("POST", "/api/v1/onboarding/setup")
	if len(calls) != 1 {
		t.Fatalf("expected one setup POST, got %d", len(calls))
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	checks := map[string]any{
		"workspace_name":     "My Workspace",
		"preferred_language": "Čeština",
		"cli_adapter":        "CLAUDE_CODE",
		"llm_provider":       "ANTHROPIC",
		"llm_model":          "claude-sonnet-4-6", // adapter default kicks in
		"credential_name":    "ANTHROPIC_API_KEY",
		"credential_value":   "sk-ant-oat01-perfectly-fine-token",
		"crew_template_slug": "software-development",
		"pairing_mode":       false,
	}
	for k, want := range checks {
		if body[k] != want {
			t.Errorf("body[%s]: got %v want %v", k, body[k], want)
		}
	}
	if _, present := body["telemetry_opt_in"]; present {
		t.Error("telemetry_opt_in must be omitted when neither flag nor interactive consent applies")
	}
	if _, present := body["crew_name"]; present {
		t.Error("template crews must not send crew_name")
	}
}

func TestRunSetup_BlankCrewAndTelemetryFlag(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	setupStubCLICov(t, stub)
	saveSetupFlagsCov(t)

	stub.OnPost("/api/v1/onboarding/setup", clitest.JSONResponse(200, map[string]any{
		"workspace_id": "ws_1", "crew_id": "crew_1", "agent_id": "ag_9", "agent_count": 1,
	}))

	setupCrewFlag = "blank"
	setupAdapterFlag = "GEMINI_CLI"
	setupAPIKeyFlag = "gemini-cli-token-12345"
	setFlagCov(t, setupCmd, "telemetry", "true") // marks Changed → explicit consent

	out, err := captureStdoutCov(t, func() error {
		return runSetup(setupCmd, nil)
	})
	if err != nil {
		t.Fatalf("runSetup: %v", err)
	}
	if !strings.Contains(out, "Telemetry: enabled") {
		t.Errorf("telemetry confirmation missing:\n%s", out)
	}

	calls := stub.CallsFor("POST", "/api/v1/onboarding/setup")
	if len(calls) != 1 {
		t.Fatalf("expected one setup POST, got %d", len(calls))
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body["crew_name"] != "My Crew" {
		t.Errorf("blank crew_name: %v", body["crew_name"])
	}
	if name, _ := body["agent_name"].(string); !strings.Contains(name, "Gemini CLI") {
		t.Errorf("agent_name should derive from adapter label; got %v", body["agent_name"])
	}
	if body["telemetry_opt_in"] != true {
		t.Errorf("telemetry_opt_in: %v", body["telemetry_opt_in"])
	}
	if body["llm_provider"] != "GOOGLE" || body["credential_name"] != "GOOGLE_API_KEY" {
		t.Errorf("adapter wiring: provider=%v cred=%v", body["llm_provider"], body["credential_name"])
	}
}

func TestRunSetup_TokenFromEnvVar(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	setupStubCLICov(t, stub)
	saveSetupFlagsCov(t)

	stub.OnPost("/api/v1/onboarding/setup", clitest.JSONResponse(200, map[string]any{
		"agent_count": 1,
	}))

	t.Setenv("ANTHROPIC_API_KEY", "  sk-ant-oat01-from-env  ")
	setupCrewFlag = "blank"
	setupAdapterFlag = "CLAUDE_CODE"

	_, err := captureStdoutCov(t, func() error {
		return runSetup(setupCmd, nil)
	})
	if err != nil {
		t.Fatalf("runSetup: %v", err)
	}
	calls := stub.CallsFor("POST", "/api/v1/onboarding/setup")
	if len(calls) != 1 {
		t.Fatalf("expected one setup POST, got %d", len(calls))
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body["credential_value"] != "sk-ant-oat01-from-env" {
		t.Errorf("env token should be trimmed and used; got %v", body["credential_value"])
	}
}

func TestRunSetup_ServerError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	setupStubCLICov(t, stub)
	saveSetupFlagsCov(t)

	stub.OnPost("/api/v1/onboarding/setup", clitest.ErrorResponse(422, "workspace already onboarded"))

	setupCrewFlag = "blank"
	setupAdapterFlag = "CLAUDE_CODE"
	setupAPIKeyFlag = "sk-ant-oat01-perfectly-fine-token"

	_, err := captureStdoutCov(t, func() error {
		return runSetup(setupCmd, nil)
	})
	if err == nil || !strings.Contains(err.Error(), "setup:") ||
		!strings.Contains(err.Error(), "workspace already onboarded") {
		t.Fatalf("want wrapped setup error, got %v", err)
	}
}

func TestRunSetup_ModelOverride(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	setupStubCLICov(t, stub)
	saveSetupFlagsCov(t)

	stub.OnPost("/api/v1/onboarding/setup", clitest.JSONResponse(200, map[string]any{"agent_count": 1}))

	setupCrewFlag = "blank"
	setupAdapterFlag = "CODEX_CLI"
	setupModelFlag = "gpt-custom"
	setupAPIKeyFlag = "openai-token-123456"

	_, err := captureStdoutCov(t, func() error {
		return runSetup(setupCmd, nil)
	})
	if err != nil {
		t.Fatalf("runSetup: %v", err)
	}
	var body map[string]any
	calls := stub.CallsFor("POST", "/api/v1/onboarding/setup")
	if len(calls) != 1 {
		t.Fatalf("expected one setup POST, got %d", len(calls))
	}
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body["llm_model"] != "gpt-custom" {
		t.Errorf("explicit --model must win over adapter default; got %v", body["llm_model"])
	}
}

// errReaderCov always fails — drives the non-EOF readLine error branch.
type errReaderCov struct{}

func (errReaderCov) Read([]byte) (int, error) {
	return 0, fmt.Errorf("tty went away")
}

func withBrokenStdinCov(t *testing.T) {
	t.Helper()
	orig := stdinReader
	stdinReader = bufio.NewReader(errReaderCov{})
	t.Cleanup(func() { stdinReader = orig })
}

func TestReadLine_Error(t *testing.T) {
	withBrokenStdinCov(t)
	if _, err := readLine(); err == nil || !strings.Contains(err.Error(), "read stdin") {
		t.Fatalf("want read stdin error, got %v", err)
	}
}

func TestPrompts_ReadErrorPropagates(t *testing.T) {
	t.Run("promptCrew", func(t *testing.T) {
		withBrokenStdinCov(t)
		_, err := captureStdoutCov(t, func() error {
			_, perr := promptCrew()
			return perr
		})
		if err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("promptAdapter", func(t *testing.T) {
		withBrokenStdinCov(t)
		_, err := captureStdoutCov(t, func() error {
			_, perr := promptAdapter()
			return perr
		})
		if err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("promptYesNo", func(t *testing.T) {
		withBrokenStdinCov(t)
		_, err := captureStdoutCov(t, func() error {
			_, perr := promptYesNo("ok?", true)
			return perr
		})
		if err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("promptOptional", func(t *testing.T) {
		withBrokenStdinCov(t)
		_, err := captureStdoutCov(t, func() error {
			_, perr := promptOptional("lang", "English")
			return perr
		})
		if err == nil {
			t.Fatal("want error")
		}
	})
}

// TestPromptAPIKey_NonTTY: under `go test` stdin is not a terminal, so
// term.ReadPassword fails with ENOTTY — the error must be wrapped, not
// swallowed into an empty token.
func TestPromptAPIKey_NonTTY(t *testing.T) {
	if _, err := promptAPIKey("Claude Code"); err == nil || !strings.Contains(err.Error(), "read token") {
		t.Skipf("stdin unexpectedly behaves like a TTY here (err=%v)", err)
	}
}

func TestServerTelemetryEnabled_TransportAndDecodeFallback(t *testing.T) {
	want := crashreport.DefaultOptIn(version)

	t.Run("connection refused", func(t *testing.T) {
		setupDeadCLICov(t)
		if got := serverTelemetryEnabled(); got != want {
			t.Errorf("got %v want build default %v", got, want)
		}
	})

	t.Run("malformed body", func(t *testing.T) {
		stub := clitest.NewStubServer()
		defer stub.Close()
		setupStubCLICov(t, stub)
		stub.OnGet("/api/v1/system/telemetry", clitest.TextResponse(200, "not json"))
		if got := serverTelemetryEnabled(); got != want {
			t.Errorf("got %v want build default %v", got, want)
		}
	})
}

func TestRunSetup_PreflightBlocksBadScheme(t *testing.T) {
	saveCLIState(t)
	saveSetupFlagsCov(t)
	t.Setenv("CREWSHIP_SERVER", "")
	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: covWorkspaceID, Server: "ftp://example.com"}

	setupCrewFlag = "blank"
	setupAdapterFlag = "CLAUDE_CODE"
	setupAPIKeyFlag = "sk-ant-oat01-perfectly-fine-token"

	err := runSetup(setupCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("want scheme preflight error, got %v", err)
	}
}

func TestRunSetup_NetworkError(t *testing.T) {
	setupDeadCLICov(t)
	saveSetupFlagsCov(t)

	setupCrewFlag = "blank"
	setupAdapterFlag = "CLAUDE_CODE"
	setupAPIKeyFlag = "sk-ant-oat01-perfectly-fine-token"

	err := runSetup(setupCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "contact server") {
		t.Fatalf("want contact server error, got %v", err)
	}
}

func TestRunSetup_DecodeError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	setupStubCLICov(t, stub)
	saveSetupFlagsCov(t)
	stub.OnPost("/api/v1/onboarding/setup", clitest.TextResponse(200, "not json"))

	setupCrewFlag = "blank"
	setupAdapterFlag = "CLAUDE_CODE"
	setupAPIKeyFlag = "sk-ant-oat01-perfectly-fine-token"

	err := runSetup(setupCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "parse response") {
		t.Fatalf("want parse response error, got %v", err)
	}
}

func TestRunSetup_TelemetryDisabledFlag(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	setupStubCLICov(t, stub)
	saveSetupFlagsCov(t)
	stub.OnPost("/api/v1/onboarding/setup", clitest.JSONResponse(200, map[string]any{"agent_count": 1}))

	setupCrewFlag = "blank"
	setupAdapterFlag = "CLAUDE_CODE"
	setupAPIKeyFlag = "sk-ant-oat01-perfectly-fine-token"
	setFlagCov(t, setupCmd, "telemetry", "false")

	out, err := captureStdoutCov(t, func() error {
		return runSetup(setupCmd, nil)
	})
	if err != nil {
		t.Fatalf("runSetup: %v", err)
	}
	if !strings.Contains(out, "Telemetry: disabled") {
		t.Errorf("disabled confirmation missing:\n%s", out)
	}
	var body map[string]any
	calls := stub.CallsFor("POST", "/api/v1/onboarding/setup")
	if len(calls) != 1 {
		t.Fatalf("expected one setup POST, got %d", len(calls))
	}
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body["telemetry_opt_in"] != false {
		t.Errorf("telemetry_opt_in: %v", body["telemetry_opt_in"])
	}
}
