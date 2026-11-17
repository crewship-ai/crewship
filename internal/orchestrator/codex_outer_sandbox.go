package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/crewship-ai/crewship/internal/managedlaunch"
	"github.com/crewship-ai/crewship/internal/provider"
)

type CodexSandboxPolicy struct {
	Enabled bool
	Launch  *managedlaunch.Descriptor
}
type CodexSandboxPolicyResolver func(context.Context, string, string) (CodexSandboxPolicy, error)
type CodexSandboxRecorder interface {
	RecordCodexSandboxPreflight(context.Context, provider.CodexSandboxEvidence) error
}

// SetCodexSandboxPolicyResolver wires stored workspace-scoped authority before
// serving runs. Request fields, model output and image env are not consent.
func (o *Orchestrator) SetCodexSandboxPolicyResolver(resolve CodexSandboxPolicyResolver) {
	o.codexSandboxPolicy = resolve
}

func (o *Orchestrator) admitCodexSandbox(ctx context.Context, req *AgentRunRequest, use *provider.RuntimeUse) error {
	if req.CLIAdapter != "CODEX_CLI" || req.ToolProfile == "MINIMAL" {
		return nil
	}
	legacy := os.Getenv("CREWSHIP_CODEX_SANDBOX_MODE") == "danger-full-access"
	policy := CodexSandboxPolicy{}
	var err error
	if o.codexSandboxPolicy != nil {
		policy, err = o.codexSandboxPolicy(ctx, req.WorkspaceID, req.CrewID)
		if err != nil {
			return fmt.Errorf("Codex sandbox authority unavailable: %w", err)
		}
	}
	if !policy.Enabled {
		if legacy {
			return errors.New("legacy global Codex sandbox override is configured, but this crew has not opted into qualified outer-container isolation; remove CREWSHIP_CODEX_SANDBOX_MODE or enable the crew setting and pass preflight")
		}
		return nil
	}
	if use == nil || !use.Matches(req.CrewID, req.ContainerID) || policy.Launch == nil {
		return errors.New("Codex outer isolation: reserved runtime and immutable build authority required")
	}
	d := *policy.Launch
	d.EnvKeys = nil
	d.OuterSandbox = true
	if err := d.Validate(); err != nil {
		return err
	}
	req.codexOuterSandbox = true
	if owner, ok := o.agentRuns.Load(req.RunID); ok {
		owner.(*agentRunControl).outerManaged.Store(true)
	}
	req.managedLaunch = &d
	req.runtimeImageID = d.ImageID
	if err := o.attestCodexOuterSandbox(ctx, *req); err != nil {
		return o.denyCodexSandbox(ctx, *req, err)
	}
	// Effective kernel controls are checked by the trusted static launcher
	// immediately before the same open artifact is executed. No extra probe
	// process or untracked RunID is created in this shared runtime.

	return nil
}

func (o *Orchestrator) attestCodexOuterSandbox(ctx context.Context, req AgentRunRequest) error {
	p, ok := o.container.(provider.CodexOuterSandboxProvider)
	if !ok || req.managedLaunch == nil {
		return errors.New("Codex outer isolation: runtime provider qualification unsupported")
	}
	return p.AttestCodexOuterSandbox(ctx, req.ContainerID, req.CrewID, *req.managedLaunch)
}

func (o *Orchestrator) currentManagedDescriptor(ctx context.Context, req AgentRunRequest) (*managedlaunch.Descriptor, error) {
	if req.codexOuterSandbox {
		if o.codexSandboxPolicy == nil {
			return nil, errors.New("Codex outer isolation: stored consent unavailable")
		}
		policy, err := o.codexSandboxPolicy(ctx, req.WorkspaceID, req.CrewID)
		if err != nil {
			return nil, err
		}
		if !policy.Enabled || policy.Launch == nil {
			return nil, errors.New("Codex outer isolation: consent revoked before execution")
		}
		return policy.Launch, nil
	}
	if o.managedLaunchResolver == nil {
		return nil, errors.New("managed launch: build authority unavailable")
	}
	return o.managedLaunchResolver(ctx, req.WorkspaceID, req.CrewID, req.CLIAdapter)
}

func (o *Orchestrator) recordCodexSandbox(ctx context.Context, req AgentRunRequest, status string) error {
	audit, ok := o.getAuditLog().(CodexSandboxRecorder)
	if !ok {
		return errors.New("Codex outer isolation: durable audit unavailable")
	}
	e := provider.CodexSandboxEvidence{WorkspaceID: req.WorkspaceID, CrewID: req.CrewID, AgentID: req.AgentID, RunID: req.RunID, Mode: "danger-full-access", Status: status, Source: "crew_opt_in"}
	if req.managedLaunch != nil {
		e.ImageID = req.managedLaunch.ImageID
		e.ArtifactSHA256 = req.managedLaunch.SHA256
		e.RevisionID = req.managedLaunch.RevisionID
	}
	return audit.RecordCodexSandboxPreflight(ctx, e)
}
func (o *Orchestrator) denyCodexSandbox(ctx context.Context, req AgentRunRequest, cause error) error {
	op, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return errors.Join(cause, o.recordCodexSandbox(op, req, "denied"))
}
