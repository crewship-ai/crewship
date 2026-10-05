package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/crewship-ai/crewship/internal/cli"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAIExecutablePreservesStableSymlink(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "version-1")
	next := filepath.Join(dir, "version-2")
	link := filepath.Join(dir, "crewship")
	for _, path := range []string{old, next} {
		if err := os.WriteFile(path, []byte("fixture"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(old, link); err != nil {
		t.Fatal(err)
	}
	lookup := func(string) (string, error) { return link, nil }
	got, err := chooseAIExecutable("crewship", old, lookup)
	if err != nil || got != link {
		t.Fatalf("resolved versioned target: %s %v", got, err)
	}
	if err = os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(next, link); err != nil {
		t.Fatal(err)
	}
	got, err = chooseAIExecutable("crewship", next, lookup)
	if err != nil || got != link {
		t.Fatalf("upgrade broke stable path: %s %v", got, err)
	}
	// Another crewship on PATH is not proof that it is this installation.
	got, err = chooseAIExecutable("missing", old, lookup)
	if err != nil || got != old {
		t.Fatalf("accepted another executable: %s %v", got, err)
	}
	if err = os.Remove(link); err != nil {
		t.Fatal(err)
	}
	got, err = chooseAIExecutable("crewship", old, func(string) (string, error) { return "", errors.New("missing") })
	if err != nil || got != old {
		t.Fatal(got, err)
	}
}

func TestAIConnectionOwnershipProtectsCustomEntries(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	oldDir := aiConnectionDir
	aiConnectionDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { aiConnectionDir = oldDir })
	config := filepath.Join(dir, ".claude.json")
	entry := aiEntry{Type: "stdio", Command: "/installed/crewship", Args: []string{"mcp", "serve"}}
	save := func(e aiEntry) {
		t.Helper()
		raw, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"crewship": e, "other": map[string]any{"command": "keep-me"}}, "private_other_setting": "do-not-print"})
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(config, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	save(entry)
	read, found, err := inspectAIEntry(context.Background(), "claude")
	if err != nil || !found || ownsAIEntry("claude", read) {
		t.Fatal("unmanaged entry treated as owned")
	}
	if err = saveAIReceipt("claude", read); err != nil {
		t.Fatal(err)
	}
	if !ownsAIEntry("claude", read) {
		t.Fatal("recorded entry not owned")
	}
	if _, err = executeAITest(t, "disconnect", "claude", "--dry-run"); err != nil {
		t.Fatal(err)
	}
	entry.Args = append(entry.Args, "--allow-write")
	entry.Env = map[string]string{"SECRET": "never-print-this"}
	save(entry)
	if _, err = executeAITest(t, "disconnect", "claude"); err == nil {
		t.Fatal("removed modified entry")
	}
	raw, _ := os.ReadFile(config)
	if !strings.Contains(string(raw), "keep-me") {
		t.Fatal("unrelated entry lost")
	}
	output, err := executeAITest(t, "doctor", "claude")
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"never-print-this", "do-not-print"} {
		if strings.Contains(output, secret) {
			t.Fatal("diagnostics leaked config")
		}
	}
	if !strings.Contains(output, `"owned": false`) {
		t.Fatal(output)
	}
	other := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", other)
	if ownsAIEntry("claude", read) {
		t.Fatal("receipt reused in different client context")
	}
}

func TestAIConnectionMissingAndMalformedConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	_, found, err := inspectAIEntry(context.Background(), "claude")
	if err != nil || found {
		t.Fatal(found, err)
	}
	if err = os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(`{"mcpServers":`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = inspectAIEntry(context.Background(), "claude"); err == nil {
		t.Fatal("malformed config treated as absent")
	}
}

func TestAILaunchCarriesCustomCredentialPathWithoutSecrets(t *testing.T) {
	saveCLIState(t)
	flagProfile, flagServer, flagWorkspace = "", "https://fixture.invalid", "workspace"
	cliCfg = &cli.CLIConfig{Server: "https://fixture.invalid", Token: "never-export-this"}
	t.Setenv("CREWSHIP_CONFIG", "relative-config.yaml")
	output, err := executeAITest(t, "connect", "codex", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	absolute, _ := filepath.Abs("relative-config.yaml")
	if !strings.Contains(output, absolute) || !strings.Contains(output, "--credential-config") || strings.Contains(output, "never-export-this") {
		t.Fatal(output)
	}
}

func TestAIReceiptIncludesClientSpecificOptions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	t.Setenv("CREWSHIP_CONFIG", filepath.Join(dir, "cli.yaml"))
	path := filepath.Join(dir, ".claude.json")
	raw := `{"mcpServers":{"crewship":{"type":"stdio","command":"crewship","args":["mcp","serve"],"clientSpecificOption":{"enabled":true}}}}`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	entry, _, err := inspectAIEntry(context.Background(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	if err = saveAIReceipt("claude", entry); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte(strings.Replace(raw, `"enabled":true`, `"enabled":false`, 1)), 0600); err != nil {
		t.Fatal(err)
	}
	changed, _, err := inspectAIEntry(context.Background(), "claude")
	if err != nil || ownsAIEntry("claude", changed) {
		t.Fatal("ignored modified client-specific option", err)
	}
}

func TestAIConnectionRejectsInvalidPolicyBeforeRegistration(t *testing.T) {
	saveCLIState(t)
	flagProfile, flagServer, flagWorkspace = "", "https://fixture.invalid", "workspace"
	cliCfg = &cli.CLIConfig{Server: "https://fixture.invalid"}
	for _, args := range [][]string{
		{"--catalog", "invalid"},
		{"--allow-write", "--write-tags", "does-not-exist"},
		{"--allow-write", "--write-operations", "get_api_v1_agents"},
		{"--write-operations", "post_api_v1_crews"},
	} {
		if _, err := executeAITest(t, append([]string{"connect", "codex", "--dry-run"}, args...)...); err == nil {
			t.Fatalf("accepted invalid policy %v", args)
		}
	}
}
