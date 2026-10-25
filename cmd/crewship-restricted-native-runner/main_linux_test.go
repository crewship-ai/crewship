//go:build linux

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeCommandPinsSandboxAndClosedProvider(t *testing.T) {
	args := strings.Join(nativeArgs("gpt-5-mini"), "\n")
	for _, required := range []string{`default_permissions="crewship_native"`, `permissions.crewship_native.network.enabled=false`, `permissions.crewship_native.extends=":workspace"`, `permissions.crewship_native.filesystem={":root"="deny"`, "approval_policy=\"never\"", "web_search=\"disabled\"", "supports_websockets=false", "request_max_retries=0", "stream_max_retries=0", "http://127.0.0.1:9121/v1"} {
		if !strings.Contains(args, required) {
			t.Fatalf("missing %s", required)
		}
	}
	for _, forbidden := range []string{"--sandbox", "danger-full-access", "--yolo", "auth.json", "OPENAI_API_KEY", "require_escalated"} {
		if strings.Contains(args, forbidden) {
			t.Fatalf("unsafe command %s", forbidden)
		}
	}
}

// This diagnostic uses only a synthetic token and an unreachable container-local
// provider. It exposes startup errors without changing production stderr policy.
// It is not a substitute for the non-skippable native accounting canary.
func TestNativeCandidateConfigProbe(t *testing.T) {
	image := os.Getenv("CREWSHIP_NATIVE_CONFIG_PROBE_IMAGE")
	if image == "" {
		t.Skip("explicit disposable image probe only")
	}
	policy, err := filepath.Abs("../../internal/restrictedruntime/nativepolicy/seccomp.json")
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("crewship-native-config-probe-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if output, err := exec.CommandContext(ctx, "docker", "rm", "-f", name).CombinedOutput(); err != nil {
			t.Errorf("owned probe cleanup failed: %v %s", err, output)
		}
	})
	args := []string{"run", "--name", name, "--read-only", "--network", "none", "--user", "1001:1001",
		"--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--security-opt", "seccomp=" + policy,
		"--security-opt", "apparmor=crewship-native-codex-v159", "--memory", "536870912", "--pids-limit", "256",
		"--tmpfs", "/home/agent:rw,nosuid,nodev,noexec,size=8388608,uid=1001,gid=1001,mode=0700",
		"--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=4194304,mode=1777",
		"--env", "HOME=/home/agent", "--env", "CODEX_HOME=/home/agent/.codex",
		"--env", "CREWSHIP_BROKER_TOKEN=" + strings.Repeat("a", 64),
		"--entrypoint", "/bin/sh", image, "-c",
		`mkdir -p /home/agent/work /home/agent/.codex; exec "$@"`, "probe", "/usr/bin/codex"}
	args = append(args, nativeArgs("gpt-5-mini")...)
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	t.Logf("synthetic startup diagnostic: %s", output)
	if ctx.Err() != nil || err == nil || !strings.Contains(string(output), "http://127.0.0.1:9121/v1/responses") {
		t.Fatalf("configured CLI did not reach the closed local provider: %v", err)
	}
}
