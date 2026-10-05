package devcontainer

import (
	"archive/tar"
	"bytes"
	"strings"
	"testing"
)

func toolchainArchive(t testing.TB, files map[string]string) []byte {
	t.Helper()
	var out bytes.Buffer
	w := tar.NewWriter(&out)
	for name, content := range files {
		if err := w.WriteHeader(&tar.Header{Name: "toolchain/" + name, Mode: 0600, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestToolchainInventorySeparatesObservedVersionFromUnknown(t *testing.T) {
	raw := toolchainArchive(t, map[string]string{
		"schema":      "1\n",
		"claude.path": "/opt/mise/data/shims/claude\n", "claude.version": "2.1.263 (Claude Code)\n", "claude.status": "0\n",
		"codex.path": "/opt/mise/data/shims/codex\n", "codex.version": "codex-cli 0.153.2\n", "codex.status": "0\n",
		"gemini.version": "not a version; EXAMPLE-SECRET-DO-NOT-REPORT", "gemini.status": "1\n",
	})
	inventory, err := parseToolchainArchive(bytes.NewReader(raw), []string{"claude", "codex", "gemini", "opencode"})
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Status != "recorded" || len(inventory.Tools) != 4 {
		t.Fatalf("inventory=%+v", inventory)
	}
	if inventory.Tools[0].Version != "2.1.263" || inventory.Tools[1].Version != "0.153.2" {
		t.Fatalf("versions=%+v", inventory.Tools)
	}
	if inventory.Tools[2].Version != "" || inventory.Tools[2].Status != "probe_failed" {
		t.Fatalf("failed probe became a version: %+v", inventory.Tools[2])
	}
	if inventory.Tools[3].Status != "unavailable" {
		t.Fatalf("absent tool=%+v", inventory.Tools[3])
	}
}

func TestToolchainInventoryRejectsMalformedEvidence(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"no schema":       {"claude.version": "2.1.0"},
		"future schema":   {"schema": "2"},
		"oversized entry": {"schema": "1", "claude.version": strings.Repeat("x", 8193)},
		"traversal":       {"schema": "1", "../outside": "secret"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseToolchainArchive(bytes.NewReader(toolchainArchive(t, files)), []string{"claude"}); err == nil {
				t.Fatal("malformed inventory accepted")
			}
		})
	}
}

func TestToolchainVersionDoesNotMistakeErrorTextForVersion(t *testing.T) {
	for _, raw := range []string{"error: HTTP 401; need version 2.1.0", "EXAMPLE_SECRET=2.1.0", "", "2026-10-02 crashed"} {
		if got := ObservedToolVersion("claude", raw); got != "" {
			t.Fatalf("unrecognized output became version %q", got)
		}
	}
}

func TestRequestedToolchainPreservesSelectorAndSource(t *testing.T) {
	cfg := &Config{Features: map[string]map[string]any{"ghcr.io/devcontainers/features/claude-code:1": nil}}
	got, err := RequestedToolchain(cfg, `{"tools":{"codex":"0.153.2"}}`, []string{"CLAUDE_CODE", "CODEX_CLI", "GEMINI_CLI"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("requests=%+v", got)
	}
	if got[0].Source != "devcontainer_feature" || got[0].Selector != "" {
		t.Fatalf("feature falsely assigned a mise version: %+v", got[0])
	}
	if got[1].Selector != "0.153.2" || !got[1].Exact {
		t.Fatalf("exact pin lost: %+v", got[1])
	}
	if got[2].Selector != "latest" || got[2].Exact {
		t.Fatalf("default selector=%+v", got[2])
	}
}

func TestRequestedToolchainExplicitMiseOverridesFeatureSource(t *testing.T) {
	cfg := &Config{Features: map[string]map[string]any{"ghcr.io/devcontainers/features/claude-code:1": nil}}
	got, err := RequestedToolchain(cfg, `{"tools":{"claude":"2.1.263"}}`, []string{"CLAUDE_CODE"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Source != "mise" || got[0].Selector != "2.1.263" || !got[0].Exact {
		t.Fatalf("explicit pin hidden behind feature: %+v", got)
	}
}
