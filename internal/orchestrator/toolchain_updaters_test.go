package orchestrator

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

func TestManagedGeminiAndOpenCodeEnvironment(t *testing.T) {
	for _, adapter := range []string{"GEMINI_CLI", "OPENCODE"} {
		req := AgentRunRequest{CLIAdapter: adapter, AgentSlug: "managed", RunID: "run"}
		want := "OPENCODE_DISABLE_AUTOUPDATE=1"
		if adapter == "GEMINI_CLI" {
			want = "GEMINI_CLI_SYSTEM_SETTINGS_PATH=" + agentHomeDir(req.AgentSlug, req.RunID) + "/.gemini/crewship-managed-settings.json"
		}
		for name, env := range map[string][]string{"direct": BuildEnvVars(req, nil), "sidecar": BuildEnvVarsSidecar(req, false)} {
			t.Run(adapter+"/"+name, func(t *testing.T) {
				if !slices.Contains(env, want) {
					t.Fatalf("missing managed updater setting: %s", want)
				}
			})
		}
	}
}

func TestGeminiManagedSettingsFailureStopsPreflight(t *testing.T) {
	o, c, req := preflightFixture(t)
	req.CLIAdapter = "GEMINI_CLI"
	req.Credentials = nil
	c.stdout = func(cfg provider.ExecConfig, stdin string) string {
		if len(cfg.Cmd) == 1 && cfg.Cmd[0] == "sh" && stdin != "" {
			return preflightFailMarker + "file:.gemini/crewship-managed-settings.json\n"
		}
		return ""
	}
	c.exitFor = func(cfg provider.ExecConfig) int {
		if len(cfg.Cmd) == 1 && cfg.Cmd[0] == "sh" {
			return 1
		}
		return 0
	}
	_, _, err := o.preparePreflightDirs(context.Background(), req, nil, false, false, "run-1")
	if err == nil || !strings.Contains(err.Error(), "managed toolchain") {
		t.Fatalf("missing required settings must stop preflight, got %v", err)
	}
}

func TestGeminiManagedSettingsMatchRunEnvironment(t *testing.T) {
	fake := &adapterTestContainer{}
	req := AgentRunRequest{CLIAdapter: "GEMINI_CLI", AgentSlug: "managed", RunID: "run", ContainerID: "owned"}
	if err := setupManagedToolchainSettings(context.Background(), fake, req, quietAdapterLogger()); err != nil {
		t.Fatal(err)
	}
	script := findScriptForPath(t, fake.execScripts, managedGeminiSettingsFile)
	body := decodeBase64FromShellScript(t, script)
	var settings struct {
		General map[string]bool `json:"general"`
	}
	if err := json.Unmarshal([]byte(body), &settings); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"enableAutoUpdate", "enableAutoUpdateNotification"} {
		if value, ok := settings.General[key]; !ok || value {
			t.Fatalf("%s must be explicitly false", key)
		}
	}
	for _, key := range []string{"disableAutoUpdate", "disableUpdateNag"} {
		if !settings.General[key] {
			t.Fatalf("legacy %s must be true", key)
		}
	}
}
