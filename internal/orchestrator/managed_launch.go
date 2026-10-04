package orchestrator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/managedlaunch"
	"github.com/crewship-ai/crewship/internal/provider"
)

type ManagedLaunchResolver func(context.Context, string, string, string) (*managedlaunch.Descriptor, error)

func managedLaunchPilots() map[string]bool {
	result := map[string]bool{}
	for _, id := range strings.Split(os.Getenv("CREWSHIP_MANAGED_LAUNCH_CREWS"), ",") {
		if id = strings.TrimSpace(id); id != "" {
			result[id] = true
		}
	}
	return result
}

// SetManagedLaunchResolver is wired once by the server before it accepts runs.
// It reads stored crew/build authority; dispatch callers cannot opt out.
func (o *Orchestrator) SetManagedLaunchResolver(resolve ManagedLaunchResolver) {
	o.managedLaunchResolver = resolve
}

func (o *Orchestrator) admitManagedLaunch(ctx context.Context, req *AgentRunRequest, use *provider.RuntimeUse) error {
	if !o.managedLaunchCrews[req.CrewID] {
		return nil
	}
	if req.CLIAdapter == "CLAUDE_CODE" {
		return errors.New("managed launch: pilot A1 podporuje statický Codex; dynamický ELF (Claude Code) zatím nepodporován, A2")
	}
	if req.CLIAdapter != "CODEX_CLI" {
		return errors.New("managed launch: pilot A1 supports only the static Codex adapter")
	}
	if use == nil || !use.Matches(req.CrewID, req.ContainerID) || o.managedLaunchResolver == nil {
		return errors.New("managed launch: runtime reservation or build authority unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	d, err := o.managedLaunchResolver(ctx, req.WorkspaceID, req.CrewID, req.CLIAdapter)
	if err != nil {
		return err
	}
	if d == nil {
		return errors.New("managed launch: build descriptor unavailable")
	}
	if err := d.Validate(); err != nil {
		return err
	}
	if err := o.attestManagedLaunch(ctx, req.ContainerID, *d); err != nil {
		return err
	}
	copy := *d
	copy.EnvKeys = nil
	req.managedLaunch = &copy
	req.runtimeImageID = copy.ImageID
	return nil
}

func (o *Orchestrator) attestManagedLaunch(ctx context.Context, id string, d managedlaunch.Descriptor) error {
	p, ok := o.container.(provider.ManagedLaunchProvider)
	if !ok {
		return errors.New("managed launch: runtime attestation unsupported")
	}
	if err := p.AttestManagedLaunch(ctx, id, d); err != nil {
		return err
	}
	return nil
}

func managedExecConfig(req AgentRunRequest, cmd, env []string, workDir string) (provider.ExecConfig, error) {
	d := *req.managedLaunch
	if err := d.Validate(); err != nil {
		return provider.ExecConfig{}, err
	}
	if len(cmd) == 0 || cmd[0] != d.Binary {
		return provider.ExecConfig{}, errors.New("managed launch: adapter does not match build descriptor")
	}
	if large, _ := firstOversizedArg(cmd); large {
		return provider.ExecConfig{}, errors.New("managed launch: CLI argument exceeds execve limit")
	}
	values, keys, err := managedlaunch.Environment(env)
	if err != nil {
		return provider.ExecConfig{}, err
	}
	d.EnvKeys = keys
	raw, err := json.Marshal(d)
	if err != nil {
		return provider.ExecConfig{}, err
	}
	args := append([]string{managedlaunch.LauncherPath, "--managed-launch", base64.RawURLEncoding.EncodeToString(raw)}, cmd[1:]...)
	var stdin io.Reader
	if getAdapter(req.CLIAdapter).PromptViaStdin(req) {
		stdin = strings.NewReader(req.UserMessage)
	}
	return provider.ExecConfig{ContainerID: req.ContainerID, Cmd: args, Env: values, WorkingDir: workDir, User: "1001:1001", Stdin: stdin}, nil
}
