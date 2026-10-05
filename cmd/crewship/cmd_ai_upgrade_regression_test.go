package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"
)

func TestAISkillUpgradePreservesCustomContent(t *testing.T) {
	saveCLIState(t)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("CREWSHIP_CONFIG", filepath.Join(t.TempDir(), "credentials.yaml"))
	contextPath, err := aiClientContext("claude")
	if err != nil {
		t.Fatal(err)
	}
	registration := `{"mcpServers":{"crewship":{"type":"stdio","command":"/test/crewship","args":["mcp","serve"]}}}`
	if err := os.WriteFile(contextPath, []byte(registration), 0600); err != nil {
		t.Fatal(err)
	}
	entry, _, err := inspectAIEntry(context.Background(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	if err := saveAIReceipt("claude", entry); err != nil {
		t.Fatal(err)
	}
	original := crewshipAISkill
	t.Cleanup(func() { crewshipAISkill = original })
	directory, err := aiDefaultSkillDirectory("claude")
	if err != nil {
		t.Fatal(err)
	}
	crewshipAISkill = "previous bundled skill\n"
	if err := installAISkill(directory); err != nil {
		t.Fatal(err)
	}
	crewshipAISkill = original
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.Flags().Bool("install-skill", true, "")
	if err := connectAIClient(cmd, "claude", "/test/crewship", []string{"mcp", "serve"}, nil); err != nil {
		t.Fatalf("upgrade unchanged owned skill: %v", err)
	}
	target := filepath.Join(directory, "SKILL.md")
	data, err := os.ReadFile(target)
	if err != nil || string(data) != original {
		t.Fatalf("upgraded guide: %v", err)
	}
	data, err = os.ReadFile(contextPath)
	if err != nil || string(data) != registration || !ownsAIEntry("claude", entry) {
		t.Fatal("skill refresh changed registration or connection ownership")
	}
	if err := os.WriteFile(target, []byte("custom guide\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := installAISkill(directory); err == nil {
		t.Fatal("accepted modified owned skill")
	}
	data, err = os.ReadFile(target)
	if err != nil || string(data) != "custom guide\n" {
		t.Fatal("custom guide changed")
	}
}

func TestAISkillAdoptsExactLegacyBundle(t *testing.T) {
	legacy, err := os.ReadFile("testdata/ai-skill-before-workflows.md")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	target := filepath.Join(directory, "SKILL.md")
	if err := os.WriteFile(target, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	if err := installAISkill(directory); err != nil {
		t.Fatalf("upgrade exact legacy bundle without receipt: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != crewshipAISkill {
		t.Fatal("legacy guide not updated")
	}
}

func TestAISkillRefusesSymlinkedOwnershipReceipt(t *testing.T) {
	directory := t.TempDir()
	if err := installAISkill(directory); err != nil {
		t.Fatal(err)
	}
	receipt := filepath.Join(directory, aiSkillReceiptName)
	if err := os.Remove(receipt); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "unrelated")
	if err := os.WriteFile(victim, []byte("preserve me"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, receipt); err != nil {
		t.Fatal(err)
	}
	if err := installAISkill(directory); err == nil {
		t.Fatal("accepted symlinked ownership receipt")
	}
	data, err := os.ReadFile(victim)
	if err != nil || string(data) != "preserve me" {
		t.Fatal("unrelated file changed")
	}
}

func TestMCPMissingExplicitCredentialConfigFailsBeforeRequest(t *testing.T) {
	saveCLIState(t)
	flagServer, flagProfile, flagWorkspace = "", "", ""
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	t.Setenv("CREWSHIP_SERVER", server.URL)
	t.Setenv("CREWSHIP_TOKEN", "must-not-reach-fallback-server")
	previous := cliCfg
	cmd := newMCPServeCommand()
	cmd.SetArgs([]string{"--credential-config", filepath.Join(t.TempDir(), "missing.yaml"), "--catalog", "server"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "cannot load selected credential configuration") {
		t.Fatalf("missing explicit config did not fail closed: %v", err)
	}
	if calls.Load() != 0 || cliCfg != previous {
		t.Fatalf("fallback contacted or login selection changed: requests=%d", calls.Load())
	}
	if strings.Contains(err.Error(), "must-not-reach-fallback-server") {
		t.Fatal("error exposed credential")
	}
}
