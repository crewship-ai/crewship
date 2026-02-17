package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

type AgentRunRequest struct {
	AgentID      string
	AgentSlug    string
	TeamID       string
	TeamSlug     string
	SessionID    string
	ContainerID  string
	CLIAdapter   string // CLAUDE_CODE, OPENCODE, CODEX_CLI, GEMINI_CLI
	SystemPrompt string
	UserMessage  string
	ToolProfile  string // MINIMAL, CODING, MESSAGING, FULL
	Credentials  []Credential
	TimeoutSecs  int
}

type Credential struct {
	ID         string `json:"id,omitempty"`
	EnvVarName string `json:"env_var"`
	PlainValue string `json:"value"`
	Priority   int    `json:"priority"`
	Type       string `json:"type,omitempty"` // API_KEY, AI_CLI_TOKEN, SECRET
}

type RunState struct {
	ID           string    `json:"id"`
	AgentID      string    `json:"agent_id"`
	SessionID    string    `json:"session_id"`
	Status       string    `json:"status"`
	StartedAt    time.Time `json:"started_at"`
	ContainerID  string    `json:"container_id"`
	ExecID       string    `json:"exec_id"`
	LastActivity time.Time `json:"last_activity"`
	CredentialID string    `json:"credential_id,omitempty"`
}

type AgentEvent struct {
	Type      string    `json:"type"`
	Content   string    `json:"content"`
	Metadata  any       `json:"metadata,omitempty"`
	Timestamp time.Time `json:"ts"`
}

type EventHandler func(event AgentEvent)

type Orchestrator struct {
	container provider.ContainerProvider
	state     provider.StateProvider
	logger    *slog.Logger
	cooldown  *CooldownManager
	mu        sync.Mutex
	accepting bool
}

func New(
	container provider.ContainerProvider,
	state provider.StateProvider,
	logger *slog.Logger,
) *Orchestrator {
	return &Orchestrator{
		container: container,
		state:     state,
		logger:    logger,
		cooldown:  NewCooldownManager(),
		accepting: true,
	}
}

func (o *Orchestrator) RunAgent(ctx context.Context, req AgentRunRequest, handler EventHandler) error {
	o.mu.Lock()
	if !o.accepting {
		o.mu.Unlock()
		return fmt.Errorf("orchestrator not accepting new runs")
	}
	o.mu.Unlock()

	runState := RunState{
		ID:          req.SessionID,
		AgentID:     req.AgentID,
		SessionID:   req.SessionID,
		Status:      "running",
		StartedAt:   time.Now(),
		ContainerID: req.ContainerID,
	}

	cred := o.selectCredential(req.Credentials)
	if cred != nil {
		runState.CredentialID = cred.ID
	}

	stateBytes, _ := json.Marshal(runState)
	if err := o.state.Set(ctx, "agent_runs", runState.ID, stateBytes); err != nil {
		o.logger.Error("failed to persist run state", "error", err)
	}

	env := BuildEnvVars(req, cred)
	cmd := BuildCLICommand(req)

	workDir := "/workspace/" + req.AgentSlug
	outputDir := "/output/" + req.AgentSlug

	// Create both workspace and output directories for the agent
	mkdirCfg := provider.ExecConfig{
		ContainerID: req.ContainerID,
		Cmd:         []string{"mkdir", "-p", workDir, outputDir},
		User:        "1001:1001",
	}
	mkResult, err := o.container.Exec(ctx, mkdirCfg)
	if err != nil {
		o.logger.Warn("failed to create agent dirs", "error", err)
	} else {
		io.Copy(io.Discard, mkResult.Reader)
		mkResult.Reader.Close()
	}

	env = append(env, "CREWSHIP_OUTPUT_DIR="+outputDir)

	// Inject Claude OAuth credential files into the container
	if err := setupClaudeCredentials(ctx, o.container, req.ContainerID, cred, o.logger); err != nil {
		o.logger.Warn("failed to inject claude credentials", "error", err, "agent_id", req.AgentID)
	}

	execCfg := provider.ExecConfig{
		ContainerID: req.ContainerID,
		Cmd:         cmd,
		Env:         env,
		WorkingDir:  workDir,
		User:        "1001:1001",
	}

	timeout := time.Duration(req.TimeoutSecs) * time.Second
	if timeout == 0 {
		timeout = 30 * time.Minute
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cIDShort := req.ContainerID
	if len(cIDShort) > 12 {
		cIDShort = cIDShort[:12]
	}
	o.logger.Info("exec agent", "agent_id", req.AgentID, "container_id", cIDShort, "cmd", cmd)

	result, err := o.container.Exec(execCtx, execCfg)
	if err != nil {
		o.logger.Error("exec agent failed", "error", err, "agent_id", req.AgentID)
		o.updateRunStatus(ctx, runState.ID, "error")
		return fmt.Errorf("exec agent: %w", err)
	}

	o.streamOutput(execCtx, result, req, handler)

	running, exitCode, _ := o.container.ExecInspect(ctx, result.ExecID)
	o.logger.Info("exec finished", "agent_id", req.AgentID, "running", running, "exit_code", exitCode)

	if running {
		o.updateRunStatus(ctx, runState.ID, "running")
		return nil
	}

	status := "completed"
	if exitCode != 0 {
		status = "error"
		o.logger.Warn("agent exited with error", "agent_id", req.AgentID, "exit_code", exitCode)
	}
	o.updateRunStatus(ctx, runState.ID, status)

	return nil
}

func (o *Orchestrator) StopAccepting() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.accepting = false
}

func (o *Orchestrator) RecoverFromCrash(ctx context.Context) error {
	runs, err := o.state.List(ctx, "agent_runs")
	if err != nil {
		return fmt.Errorf("list runs: %w", err)
	}

	for key, data := range runs {
		var run RunState
		if err := json.Unmarshal(data, &run); err != nil {
			o.logger.Warn("corrupt run state", "key", key, "error", err)
			continue
		}
		if run.Status != "running" {
			continue
		}

		if run.ExecID == "" {
			o.updateRunStatus(ctx, run.ID, "error")
			continue
		}

		running, _, err := o.container.ExecInspect(ctx, run.ExecID)
		if err != nil || !running {
			o.updateRunStatus(ctx, run.ID, "completed")
			o.logger.Info("recovered stale run", "run_id", run.ID, "agent_id", run.AgentID)
		}
	}
	return nil
}

func (o *Orchestrator) selectCredential(creds []Credential) *Credential {
	if len(creds) == 0 {
		return nil
	}
	for i := range creds {
		if !o.cooldown.IsInCooldown(creds[i].ID) {
			return &creds[i]
		}
	}
	return &creds[0]
}

func (o *Orchestrator) updateRunStatus(ctx context.Context, runID, status string) {
	data, err := o.state.Get(ctx, "agent_runs", runID)
	if err != nil {
		o.logger.Error("updateRunStatus: get failed", "run_id", runID, "error", err)
		return
	}
	if data == nil {
		o.logger.Warn("updateRunStatus: run not found", "run_id", runID)
		return
	}
	var run RunState
	if err := json.Unmarshal(data, &run); err != nil {
		o.logger.Error("updateRunStatus: unmarshal failed", "run_id", runID, "error", err)
		return
	}
	run.Status = status
	run.LastActivity = time.Now()
	updated, err := json.Marshal(run)
	if err != nil {
		o.logger.Error("updateRunStatus: marshal failed", "run_id", runID, "error", err)
		return
	}
	if err := o.state.Set(ctx, "agent_runs", runID, updated); err != nil {
		o.logger.Error("updateRunStatus: set failed", "run_id", runID, "error", err)
	}
}
