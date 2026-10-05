package toolchain

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/provider"
)

type qualificationRuntime struct {
	spec                 provider.SandboxSpec
	removed              bool
	failExec, failRemove bool
	output               string
}

func (*qualificationRuntime) SandboxCapabilities() provider.SandboxCapabilities {
	return provider.SandboxCapabilities{Offline: true, MaxLifetime: 2 * time.Minute}
}
func (r *qualificationRuntime) CreateSandbox(_ context.Context, s provider.SandboxSpec) (provider.SandboxRef, error) {
	r.spec = s
	return provider.SandboxRef{ImageID: s.ImageID}, nil
}
func (r *qualificationRuntime) InspectSandbox(context.Context, provider.SandboxRef) (provider.SandboxState, error) {
	return provider.SandboxState{Running: true, ImageID: r.spec.ImageID}, nil
}
func (r *qualificationRuntime) ExecSandbox(ctx context.Context, _ provider.SandboxRef, s provider.SandboxExec) (provider.SandboxExecResult, error) {
	if r.failExec {
		return provider.SandboxExecResult{}, errors.New("fixture failure")
	}
	return provider.SandboxExecResult{Output: r.output}, nil
}
func (r *qualificationRuntime) RemoveSandbox(ctx context.Context, _ provider.SandboxRef) error {
	r.removed = true
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if r.failRemove {
		return errors.New("fixture cleanup failure")
	}
	return nil
}
func TestQualificationBindsImageAndCleansEveryOutcome(t *testing.T) {
	for _, tc := range []struct {
		name, status, output string
		failExec, failRemove bool
	}{
		{"pass", "passed", "codex-cli 0.152.0", false, false},
		{"version drift", "failed", "codex-cli 0.151.0", false, false},
		{"exec failure", "failed", "", true, false},
		{"cleanup failure", "cleanup_failed", "codex-cli 0.152.0", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &qualificationRuntime{output: tc.output, failExec: tc.failExec, failRemove: tc.failRemove}
			inventory := &devcontainer.ToolchainInventory{ImageID: "immutable-image", Tools: []devcontainer.ToolchainTool{{Binary: "codex", Path: "/opt/bin/codex", Version: "0.152.0", Status: "observed"}}}
			got := Qualify(context.Background(), r, inventory, "/opt/bin:/usr/bin:/bin")
			if got.Status != tc.status || !r.removed || r.spec.ImageID != inventory.ImageID || len(r.spec.Mounts) != 0 {
				t.Fatalf("%+v %+v", got, r)
			}
		})
	}
}
func TestQualificationUnavailableDoesNotInventPass(t *testing.T) {
	if got := Qualify(context.Background(), nil, nil, ""); got.Status != "unavailable" {
		t.Fatal(got)
	}
}

type shimQualificationRuntime struct{ qualificationRuntime }

func (r *shimQualificationRuntime) ExecSandbox(ctx context.Context, _ provider.SandboxRef, s provider.SandboxExec) (provider.SandboxExecResult, error) {
	command := exec.CommandContext(ctx, s.Command[0], s.Command[1:]...)
	command.Env = append([]string{"PATH=/usr/bin:/bin"}, s.Env...)
	out, err := command.CombinedOutput()
	return provider.SandboxExecResult{Output: string(out)}, err
}
func TestQualificationPreservesShimInvocation(t *testing.T) {
	dir := t.TempDir()
	mise := filepath.Join(dir, "mise")
	shim := filepath.Join(dir, "codex")
	source := `#!/bin/sh
case "$0" in */codex) echo codex-cli 0.160.0;; *) echo mise 2026.10.0;; esac
`
	if err := os.WriteFile(mise, []byte(source), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(mise, shim); err != nil {
		t.Fatal(err)
	}
	inventory := &devcontainer.ToolchainInventory{ImageID: "fixture-image", Tools: []devcontainer.ToolchainTool{{Binary: "codex", Path: shim, Version: "0.160.0", Status: "observed"}}}
	runtime := &shimQualificationRuntime{}
	if got := Qualify(context.Background(), runtime, inventory, ""); got.Status != "passed" {
		t.Fatalf("raw shim path failed: %+v", got)
	}
	inventory.Tools[0].Path = mise
	if got := Qualify(context.Background(), runtime, inventory, ""); got.Status != "failed" || got.Tools[0].Status != "version_mismatch" {
		t.Fatalf("fixture did not detect readlink regression: %+v", got)
	}
}
