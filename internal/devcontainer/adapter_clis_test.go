package devcontainer

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRequiredAdapterCLIs(t *testing.T) {
	tests := []struct {
		name     string
		adapters []string
		want     []string // binaries, catalogue order
	}{
		{"none", nil, nil},
		{"unknown skipped", []string{"", "NOPE"}, nil},
		{"one", []string{"CLAUDE_CODE"}, []string{"claude"}},
		{"deduped and ordered", []string{"GEMINI_CLI", "claude_code", "GEMINI_CLI", " codex_cli "}, []string{"claude", "codex", "gemini"}},
		{"every adapter", []string{"CLAUDE_CODE", "CODEX_CLI", "GEMINI_CLI", "OPENCODE", "CURSOR_CLI", "FACTORY_DROID"},
			[]string{"claude", "codex", "gemini", "opencode", "cursor-agent", "droid"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := RequiredAdapterCLIs(tc.adapters)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d clis, want %d: %+v", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i].Binary != tc.want[i] {
					t.Errorf("cli[%d] = %q, want %q", i, got[i].Binary, tc.want[i])
				}
			}
		})
	}
}

// Every adapter the agent handlers accept must have an install recipe; a new
// adapter added without one would be exactly the silent gap this catalogue
// closes.
func TestAdapterCLIs_EveryEntryInstallable(t *testing.T) {
	for _, a := range adapterCLIs {
		if a.Binary == "" {
			t.Errorf("%s: no binary", a.Adapter)
		}
		if a.MiseTool == "" && a.InstallCommand == "" {
			t.Errorf("%s: neither a mise tool nor an install command", a.Adapter)
		}
	}
	if _, ok := AdapterCLIFor("claude_code"); !ok {
		t.Error("AdapterCLIFor is not case-insensitive")
	}
	if _, ok := AdapterCLIFor("NOPE"); ok {
		t.Error("AdapterCLIFor accepted an unknown adapter")
	}
}

func miseTools(t *testing.T, cfg string) map[string]string {
	t.Helper()
	var m MiseConfig
	if err := json.Unmarshal([]byte(cfg), &m); err != nil {
		t.Fatalf("mise config is not JSON: %v\n%s", err, cfg)
	}
	return m.Tools
}

func TestEnsureAdapterCLIs(t *testing.T) {
	t.Run("nothing required leaves inputs untouched", func(t *testing.T) {
		cfg := &Config{Image: "debian"}
		plan, err := EnsureAdapterCLIs(cfg, `{"tools":{"node":"22"}}`, nil)
		if err != nil {
			t.Fatal(err)
		}
		if plan.MiseConfig != `{"tools":{"node":"22"}}` || plan.AddedTools != nil || plan.Binaries != nil {
			t.Errorf("unexpected plan: %+v", plan)
		}
	})

	t.Run("adds the mise tool when nothing provides the binary", func(t *testing.T) {
		cfg := &Config{Image: "debian", Features: map[string]map[string]any{"ghcr.io/devcontainers/features/terraform:1": {}}}
		plan, err := EnsureAdapterCLIs(cfg, "", []string{"CLAUDE_CODE"})
		if err != nil {
			t.Fatal(err)
		}
		tools := miseTools(t, plan.MiseConfig)
		if tools["claude"] != "latest" {
			t.Errorf("claude tool not added: %v", tools)
		}
		if len(plan.AddedTools) != 1 || plan.AddedTools[0] != "claude" {
			t.Errorf("AddedTools = %v", plan.AddedTools)
		}
		if len(plan.Binaries) != 1 || plan.Binaries[0] != "claude" {
			t.Errorf("Binaries = %v", plan.Binaries)
		}
	})

	t.Run("keeps the operator's tools and adds beside them", func(t *testing.T) {
		cfg := &Config{Image: "debian"}
		plan, err := EnsureAdapterCLIs(cfg, `{"tools":{"node":"22","claude":"2.1.0"},"env":{"FOO":"bar"}}`, []string{"CLAUDE_CODE", "CODEX_CLI"})
		if err != nil {
			t.Fatal(err)
		}
		tools := miseTools(t, plan.MiseConfig)
		if tools["node"] != "22" || tools["claude"] != "2.1.0" || tools["codex"] != "latest" {
			t.Errorf("tools = %v", tools)
		}
		if !strings.Contains(plan.MiseConfig, `"FOO":"bar"`) {
			t.Errorf("env dropped: %s", plan.MiseConfig)
		}
		if len(plan.AddedTools) != 1 || plan.AddedTools[0] != "codex" {
			t.Errorf("AddedTools = %v (a pinned claude must not be replaced)", plan.AddedTools)
		}
	})

	t.Run("a declared feature satisfies the binary without a mise tool", func(t *testing.T) {
		cfg := &Config{Image: "debian", Features: map[string]map[string]any{"ghcr.io/devcontainers-extra/features/claude-code:2": {}}}
		plan, err := EnsureAdapterCLIs(cfg, "", []string{"CLAUDE_CODE"})
		if err != nil {
			t.Fatal(err)
		}
		if plan.MiseConfig != "" || len(plan.AddedTools) != 0 {
			t.Errorf("feature-provided binary still added a tool: %+v", plan)
		}
		if len(plan.Binaries) != 1 || plan.Binaries[0] != "claude" {
			t.Errorf("the binary must still be verified: %v", plan.Binaries)
		}
	})

	t.Run("TOML mise config is re-encoded as JSON", func(t *testing.T) {
		cfg := &Config{Image: "debian"}
		plan, err := EnsureAdapterCLIs(cfg, "[tools]\nnode = \"22\"\n", []string{"GEMINI_CLI"})
		if err != nil {
			t.Fatal(err)
		}
		tools := miseTools(t, plan.MiseConfig)
		if tools["node"] != "22" || tools["gemini-cli"] != "latest" {
			t.Errorf("tools = %v", tools)
		}
	})

	t.Run("installer-only CLI becomes a postCreateCommand, once", func(t *testing.T) {
		cfg := &Config{Image: "debian", PostCreateCommand: "echo hi"}
		plan, err := EnsureAdapterCLIs(cfg, "", []string{"FACTORY_DROID"})
		if err != nil {
			t.Fatal(err)
		}
		cmds := cfg.NormalizedPostCreateCommands()
		if len(cmds) != 2 || cmds[0] != "echo hi" || !strings.Contains(cmds[1], "app.factory.ai") {
			t.Errorf("postCreateCommand = %v", cmds)
		}
		if len(plan.AddedCommands) != 1 {
			t.Errorf("AddedCommands = %v", plan.AddedCommands)
		}
		// Second pass: idempotent.
		again, err := EnsureAdapterCLIs(cfg, plan.MiseConfig, []string{"FACTORY_DROID"})
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.NormalizedPostCreateCommands()) != 2 || len(again.AddedCommands) != 0 {
			t.Errorf("second pass added again: %v / %v", cfg.NormalizedPostCreateCommands(), again.AddedCommands)
		}
	})

	t.Run("invalid mise config is an error, not a silent skip", func(t *testing.T) {
		if _, err := EnsureAdapterCLIs(&Config{Image: "debian"}, `{"tools":`, []string{"CLAUDE_CODE"}); err == nil {
			t.Error("expected an error")
		}
	})
}

func TestFeatureLeafID_StripsTagAndDigest(t *testing.T) {
	for ref, want := range map[string]string{
		"ghcr.io/devcontainers-extra/features/claude-code:2":         "claude-code",
		"ghcr.io/devcontainers-extra/features/claude-code@sha256:ab": "claude-code",
		"ghcr.io/devcontainers/features/common-utils":                "common-utils",
		"localhost:5000/features/python:1":                           "python",
		"claude-code":                                                "claude-code",
	} {
		if got := featureLeafID(ref); got != want {
			t.Errorf("%s: got %q want %q", ref, got, want)
		}
	}
}

func TestImageCoversAdapter(t *testing.T) {
	req := &AggregatedRequirements{AdapterBinaries: []string{"claude"}}
	if !ImageCoversAdapter(req, "CLAUDE_CODE") || ImageCoversAdapter(req, "CODEX_CLI") {
		t.Error("coverage by binary name is wrong")
	}
	if !ImageCoversAdapter(req, "NOPE") || !ImageCoversAdapter(nil, "") {
		t.Error("an unknown or empty adapter needs nothing and must count as covered")
	}
	if ImageCoversAdapter(nil, "CLAUDE_CODE") {
		t.Error("no requirements (no build, or a pre-verification build) must not count as covered")
	}
	if !ImageCoversAdapters(req, []string{"CLAUDE_CODE", "claude_code"}) || ImageCoversAdapters(req, []string{"CLAUDE_CODE", "GEMINI_CLI"}) {
		t.Error("ImageCoversAdapters is not the conjunction")
	}
}
