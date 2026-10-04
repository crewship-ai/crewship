// Package toolchain qualifies immutable CLI artifacts independently of the
// runtime backend. Offline qualification is not an authenticated model test.
package toolchain

import (
	"context"
	"crypto/rand"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/provider"
)

type Qualification = devcontainer.ToolchainQualification
type ToolResult = devcontainer.ToolchainProbe

// Qualify creates one disposable offline instance. It never receives credentials
// and never modifies an existing crew instance. A failing probe is evidence,
// not permission to relax the requested isolation profile.
func Qualify(ctx context.Context, runtime provider.SandboxRuntime, inventory *devcontainer.ToolchainInventory, loginPath string) (result Qualification) {
	result.Status = "unavailable"
	result.Tools = []ToolResult{}
	if inventory == nil {
		return
	}
	result.ImageID = inventory.ImageID
	if runtime == nil || (!runtime.SandboxCapabilities().Offline || runtime.SandboxCapabilities().MaxLifetime < 90*time.Second) || inventory.ImageID == "" || len(inventory.Tools) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	ref, err := runtime.CreateSandbox(ctx, provider.SandboxSpec{Lifetime: 90 * time.Second, ID: "toolchain-" + strings.ToLower(rand.Text()), ImageID: inventory.ImageID, MemoryBytes: 512 << 20, NanoCPUs: 1e9, PIDs: 64})
	if err != nil {
		result.Status = "instance_failed"
		return
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if runtime.RemoveSandbox(cleanup, ref) != nil {
			result.Status = "cleanup_failed"
		}
	}()
	state, err := runtime.InspectSandbox(ctx, ref)
	if err != nil || !state.Running || state.ImageID != inventory.ImageID {
		result.Status = "instance_failed"
		return
	}
	result.Status = "passed"
	env := []string{"HOME=/home/agent", "DISABLE_AUTOUPDATER=1"}
	for _, kv := range devcontainer.MiseRuntimeEnv {
		env = append(env, kv[0]+"="+kv[1])
	}
	if loginPath != "" {
		env = append(env, "PATH="+loginPath)
	}
	for _, tool := range inventory.Tools {
		observed := ToolResult{Binary: tool.Binary, Status: "unavailable"}
		if tool.Status == "observed" && tool.Version != "" && strings.HasPrefix(tool.Path, "/") {
			out, err := runtime.ExecSandbox(ctx, ref, provider.SandboxExec{Command: []string{tool.Path, "--version"}, Env: env, Timeout: 10 * time.Second, OutputLimit: 1024})
			switch {
			case err != nil:
				observed.Status = "probe_failed"
			case out.ExitCode != 0:
				observed.Status = "probe_failed"
			case devcontainer.ObservedToolVersion(tool.Binary, out.Output) != tool.Version:
				observed.Status = "version_mismatch"
			default:
				observed.Status = "passed"
			}
		}
		result.Tools = append(result.Tools, observed)
		if observed.Status != "passed" {
			result.Status = "failed"
			return
		}
	}
	return
}
