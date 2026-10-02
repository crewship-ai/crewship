package toolchain

import (
	"context"
	"errors"
	"testing"

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
	return provider.SandboxCapabilities{Offline: true}
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
