package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/devcontainer"
)

func TestCrewLockApplyLocalOutputAndFailureAtomicity(t *testing.T) {
	digest := func(v any) string {
		raw, _ := json.Marshal(v)
		sum := sha256.Sum256(raw)
		return "sha256:" + hex.EncodeToString(sum[:])
	}
	tools := map[string]string{"node": "22.0.0"}
	lock := &devcontainer.MiseLockBundle{SchemaVersion: 1, Files: map[string]string{"mise.lock": "native lock"}}
	plan := devcontainer.MiseResolution{SelectorsSHA256: digest(tools), LockSHA256: digest(lock), LockChanged: true, Lock: lock, ImageID: "sha256:" + strings.Repeat("a", 64), Platform: "linux-x64", MiseVersion: "2026.9.18"}
	planJSON, _ := json.Marshal(plan)
	dir := t.TempDir()
	configPath, planPath := filepath.Join(dir, "mise.json"), filepath.Join(dir, "plan.json")
	const original = `{"tools":{"node":"22.0.0"},"env":{"CUSTOM":"kept"},"ai_cli_check":"required","tasks":{"build":"echo ok"}}`
	var out bytes.Buffer
	old := crewLockApplyCmd.OutOrStdout()
	crewLockApplyCmd.SetOut(&out)
	defer crewLockApplyCmd.SetOut(old)
	for _, mode := range []string{"valid", "stale", "trailing", "unknown", "oversized", "null"} {
		t.Run(mode, func(t *testing.T) {
			out.Reset()
			config, proposal := original, string(planJSON)
			switch mode {
			case "stale":
				config = strings.ReplaceAll(original, "22.0.0", "latest")
			case "trailing":
				proposal += "{}"
			case "unknown":
				proposal = strings.Replace(proposal, "{", `{"unexpected":true,`, 1)
			case "oversized":
				proposal = strings.Repeat(" ", (4<<20)+1)
			case "null":
				proposal = "null"
			}
			if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(planPath, []byte(proposal), 0600); err != nil {
				t.Fatal(err)
			}
			err := crewLockApplyCmd.RunE(crewLockApplyCmd, []string{configPath, planPath})
			if mode == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				var got map[string]json.RawMessage
				if err := json.Unmarshal(out.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if !bytes.Contains(got["lock"], []byte("native lock")) || string(got["ai_cli_check"]) != `"required"` || !bytes.Contains(got["tasks"], []byte("echo ok")) {
					t.Fatalf("wrong output %s", out.String())
				}
			} else if err == nil || out.Len() != 0 {
				t.Fatalf("invalid plan emitted output: %q %v", out.String(), err)
			}
			current, err := os.ReadFile(configPath)
			if err != nil || string(current) != config {
				t.Fatal("command changed input file", err)
			}
		})
	}
}
