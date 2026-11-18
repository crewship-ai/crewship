//go:build linux

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentRuntimeProbeUsesExecutingBytesWithoutHumanConfig(t *testing.T) {
	binary := buildCrewshipBinary(t)
	raw, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	cfg := filepath.Join(t.TempDir(), "invalid-human-config")
	if err := os.WriteFile(cfg, []byte("[not: valid yaml"), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), binary, "agent-runtime-probe")
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "CREWSHIP_") && !strings.HasPrefix(entry, "HOME=") {
			command.Env = append(command.Env, entry)
		}
	}
	command.Env = append(command.Env, "HOME="+t.TempDir(), "CREWSHIP_CONFIG="+cfg, "CREWSHIP_SERVER=http://127.0.0.1:1", "CREWSHIP_PROFILE=unavailable-human-profile")
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("local runtime probe must not require a human profile: %v", err)
	}
	var reply struct {
		Protocol int      `json:"protocol" yaml:"protocol"`
		SHA256   string   `json:"sha256" yaml:"sha256"`
		OS       string   `json:"os" yaml:"os"`
		Arch     string   `json:"arch" yaml:"arch"`
		Commands []string `json:"commands" yaml:"commands"`
	}
	if err := json.Unmarshal(out, &reply); err != nil {
		t.Fatal("runtime probe must emit only its bounded JSON protocol")
	}
	if reply.Protocol != 1 || reply.OS != "linux" || reply.SHA256 != hex.EncodeToString(sum[:]) || reply.Arch == "" {
		t.Fatal("runtime probe does not identify the actual executing artifact")
	}
	for _, want := range []string{"commands", "version"} {
		found := false
		for _, path := range reply.Commands {
			found = found || path == want
		}
		if !found {
			t.Fatalf("runtime probe omitted available native command %s", want)
		}
	}
	if strings.Contains(string(out), cfg) || strings.Contains(string(out), "unavailable-human-profile") {
		t.Fatal("runtime probe consulted or disclosed human configuration")
	}
}
