package pipeline

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
)

// ---------------------------------------------------------------------------
// runner_orchestrator.go — RunStep end-to-end against a REAL
// orchestrator with fake container + state providers (the same rig
// internal/orchestrator's own tests use), plus each early-error path.
// ---------------------------------------------------------------------------

// orchCovState is an in-memory provider.StateProvider.
type orchCovState struct {
	data map[string]map[string][]byte
}

func newOrchCovState() *orchCovState {
	return &orchCovState{data: map[string]map[string][]byte{}}
}
func (m *orchCovState) Get(_ context.Context, bucket, key string) ([]byte, error) {
	if b, ok := m.data[bucket]; ok {
		return b[key], nil
	}
	return nil, nil
}
func (m *orchCovState) Set(_ context.Context, bucket, key string, value []byte) error {
	if m.data[bucket] == nil {
		m.data[bucket] = map[string][]byte{}
	}
	m.data[bucket][key] = value
	return nil
}
func (m *orchCovState) Delete(_ context.Context, bucket, key string) error {
	if b, ok := m.data[bucket]; ok {
		delete(b, key)
	}
	return nil
}
func (m *orchCovState) List(_ context.Context, bucket string) (map[string][]byte, error) {
	return m.data[bucket], nil
}
func (m *orchCovState) ListByPrefix(_ context.Context, bucket, prefix string) (map[string][]byte, error) {
	out := map[string][]byte{}
	for k, v := range m.data[bucket] {
		if strings.HasPrefix(k, prefix) {
			out[k] = v
		}
	}
	return out, nil
}
func (m *orchCovState) Close() error { return nil }

// orchCovContainer fakes the container provider: every setup exec is a
// no-op; the tmux-wrapped agent exec streams the canned agent output.
type orchCovContainer struct {
	ensureErr   error
	agentStream string
	execScripts []string
}

func (m *orchCovContainer) EnsureCrewRuntime(_ context.Context, _ provider.CrewConfig) (string, error) {
	if m.ensureErr != nil {
		return "", m.ensureErr
	}
	return "container-cov", nil
}
func (m *orchCovContainer) StopCrewRuntime(_ context.Context, _ string) error   { return nil }
func (m *orchCovContainer) RemoveCrewRuntime(_ context.Context, _ string) error { return nil }
func (m *orchCovContainer) ContainerStatus(_ context.Context, _ string) (*provider.ContainerStatus, error) {
	return &provider.ContainerStatus{State: "running"}, nil
}
func (m *orchCovContainer) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	joined := strings.Join(cfg.Cmd, " ")
	m.execScripts = append(m.execScripts, joined)
	if strings.Contains(joined, "tmux new-session") && strings.Contains(joined, "agent-cov-agent") {
		return &provider.ExecResult{ExecID: "exec-agent", Reader: io.NopCloser(strings.NewReader(m.agentStream))}, nil
	}
	return &provider.ExecResult{ExecID: "noop", Reader: io.NopCloser(strings.NewReader(""))}, nil
}
func (m *orchCovContainer) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	return false, 0, nil
}
func (m *orchCovContainer) ContainerStats(_ context.Context, _ string) (*provider.ContainerMetrics, error) {
	return nil, nil
}
func (m *orchCovContainer) CrewContainerName(slug string) string { return "crewship-team-" + slug }
func (m *orchCovContainer) CopyToContainer(_ context.Context, _ string, _ string, _ io.Reader) error {
	return nil
}

// orchCovResolver fakes chatbridge.ChatResolver with programmable
// ResolveAgent + CreateChat results.
type orchCovResolver struct {
	info            *chatbridge.ChatInfo
	resolveErr      error
	createChatErr   error
	createChatCalls []chatbridge.CreateChatRequest
}

func (r *orchCovResolver) CreateChat(_ context.Context, req chatbridge.CreateChatRequest) error {
	r.createChatCalls = append(r.createChatCalls, req)
	return r.createChatErr
}
func (r *orchCovResolver) ResolveChat(_ context.Context, _ string) (*chatbridge.ChatInfo, error) {
	return nil, errors.New("not used")
}
func (r *orchCovResolver) ResolveAgent(_ context.Context, agentID, workspaceID string) (*chatbridge.ChatInfo, error) {
	if r.resolveErr != nil {
		return nil, r.resolveErr
	}
	info := *r.info
	info.AgentID = agentID
	info.WorkspaceID = workspaceID
	return &info, nil
}
func (r *orchCovResolver) GetWebhookSecret(_ context.Context, _, _ string) (string, error) {
	return "", nil
}
func (r *orchCovResolver) CreateRun(_ context.Context, _, _, _, _, _ string, _ map[string]interface{}) error {
	return nil
}
func (r *orchCovResolver) UpdateRun(_ context.Context, _, _ string, _ *int, _ *string, _ map[string]interface{}) error {
	return nil
}
func (r *orchCovResolver) IncrementMessageCount(_ context.Context, _ string, _ int) error {
	return nil
}
func (r *orchCovResolver) UpdateChatTitle(_ context.Context, _, _ string) error { return nil }

// newOrchRunnerRig assembles a runner against a real orchestrator and
// the seeded agents/crews DB from openAgentResolverTestDB.
func newOrchRunnerRig(t *testing.T, container *orchCovContainer, resolver *orchCovResolver) *OrchestratorRunner {
	t.Helper()
	db := openAgentResolverTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
INSERT INTO crews (id, workspace_id, slug, name) VALUES ('crew_cov', 'ws_cov', 'cov-crew', 'Cov');
INSERT INTO agents (id, crew_id, slug) VALUES ('agent_cov', 'crew_cov', 'cov-agent');`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	orch := orchestrator.New(container, newOrchCovState(), logger)
	r, err := NewOrchestratorRunner(OrchestratorRunnerDeps{
		DB:        db,
		Orch:      orch,
		Container: container,
		Resolver:  resolver,
		LogWriter: logcollector.NewWriter(t.TempDir(), logger),
		ConvStore: conversation.NewStore(t.TempDir(), logger),
		Logger:    logger,
	})
	if err != nil {
		t.Fatalf("construct runner: %v", err)
	}
	return r
}

func covChatInfo() *chatbridge.ChatInfo {
	return &chatbridge.ChatInfo{
		AgentSlug:   "cov-agent",
		AgentRole:   "AGENT",
		CrewID:      "crew_cov",
		CrewSlug:    "cov-crew",
		CLIAdapter:  "CLAUDE_CODE",
		LLMModel:    "claude-default-model",
		TimeoutSecs: 0,
	}
}

func TestOrchestratorRunner_RunStep_HappyPath(t *testing.T) {
	container := &orchCovContainer{
		agentStream: "hello from agent\n" +
			`{"type":"result","subtype":"success","total_cost_usd":0.5,"usage":{"input_tokens":10,"output_tokens":20}}` + "\n",
	}
	resolver := &orchCovResolver{
		info: covChatInfo(),
		// CreateChat failing is explicitly non-fatal — exercise it in
		// the same run.
		createChatErr: errors.New("chat projection down"),
	}
	r := newOrchRunnerRig(t, container, resolver)

	res, err := r.RunStep(context.Background(), AgentStepRequest{
		WorkspaceID:  "ws_cov",
		AuthorCrewID: "crew_cov",
		AgentSlug:    "cov-agent",
		Prompt:       "summarize the day",
		Model:        "claude-override-model",
		TimeoutSec:   30,
		PipelineID:   "pln_cov",
		StepID:       "s1",
	})
	if err != nil {
		t.Fatalf("RunStep: %v", err)
	}
	if !strings.Contains(res.Output, "hello from agent") {
		t.Errorf("output: %q", res.Output)
	}
	if res.CostUSD != 0.5 {
		t.Errorf("cost: %v", res.CostUSD)
	}
	if res.TokensIn != 10 || res.TokensOut != 20 {
		t.Errorf("tokens: %d/%d", res.TokensIn, res.TokensOut)
	}
	if res.DurationMs < 0 {
		t.Errorf("duration: %d", res.DurationMs)
	}

	// The synthetic chat was attempted with the pipeline/step title.
	if len(resolver.createChatCalls) != 1 {
		t.Fatalf("CreateChat calls: %d", len(resolver.createChatCalls))
	}
	cc := resolver.createChatCalls[0]
	if cc.AgentID != "agent_cov" || cc.WorkspaceID != "ws_cov" {
		t.Errorf("chat routing: %+v", cc)
	}
	if !strings.Contains(cc.Title, "pln_cov") || !strings.Contains(cc.Title, "s1") {
		t.Errorf("chat title: %q", cc.Title)
	}

	// The tier override model (not the agent default) must reach the
	// CLI invocation. The args file is written as a base64 payload via
	// `printf '%s' '<b64>'`, so decode every quoted chunk first.
	var decoded strings.Builder
	for _, script := range container.execScripts {
		decoded.WriteString(script)
		decoded.WriteByte('\n')
		for _, part := range strings.Split(script, "'") {
			if len(part) < 24 {
				continue
			}
			if raw, err := base64.StdEncoding.DecodeString(part); err == nil {
				decoded.Write(raw)
				decoded.WriteByte('\n')
			}
		}
	}
	all := decoded.String()
	if !strings.Contains(all, "claude-override-model") {
		t.Errorf("model override did not reach the agent exec")
	}
	if strings.Contains(all, "claude-default-model") {
		t.Errorf("agent default model should have been overridden")
	}
}

func TestOrchestratorRunner_RunStep_ResolveAgentIDFails(t *testing.T) {
	container := &orchCovContainer{}
	resolver := &orchCovResolver{info: covChatInfo()}
	r := newOrchRunnerRig(t, container, resolver)

	_, err := r.RunStep(context.Background(), AgentStepRequest{
		WorkspaceID: "ws_cov", AuthorCrewID: "crew_cov", AgentSlug: "no-such-agent",
	})
	if err == nil || !strings.Contains(err.Error(), "resolve agent") {
		t.Errorf("expected resolve agent error, got %v", err)
	}
}

func TestOrchestratorRunner_RunStep_ResolveConfigFails(t *testing.T) {
	container := &orchCovContainer{}
	resolver := &orchCovResolver{info: covChatInfo(), resolveErr: errors.New("internal api down")}
	r := newOrchRunnerRig(t, container, resolver)

	_, err := r.RunStep(context.Background(), AgentStepRequest{
		WorkspaceID: "ws_cov", AuthorCrewID: "crew_cov", AgentSlug: "cov-agent",
	})
	if err == nil || !strings.Contains(err.Error(), "resolve agent config") {
		t.Errorf("expected config error, got %v", err)
	}
}

func TestOrchestratorRunner_RunStep_EnsureContainerFails(t *testing.T) {
	container := &orchCovContainer{ensureErr: errors.New("docker daemon unreachable")}
	resolver := &orchCovResolver{info: covChatInfo()}
	r := newOrchRunnerRig(t, container, resolver)

	_, err := r.RunStep(context.Background(), AgentStepRequest{
		WorkspaceID: "ws_cov", AuthorCrewID: "crew_cov", AgentSlug: "cov-agent",
	})
	if err == nil || !strings.Contains(err.Error(), "ensure container") {
		t.Errorf("expected ensure container error, got %v", err)
	}
}

func TestOrchestratorRunner_RunStep_OrchestratorErrorPropagates(t *testing.T) {
	container := &orchCovContainer{agentStream: ""}
	resolver := &orchCovResolver{info: covChatInfo()}
	r := newOrchRunnerRig(t, container, resolver)
	// Flip the orchestrator into shutdown so RunAgent refuses the run.
	r.orch.StopAccepting()

	_, err := r.RunStep(context.Background(), AgentStepRequest{
		WorkspaceID: "ws_cov", AuthorCrewID: "crew_cov", AgentSlug: "cov-agent",
		Prompt: "p",
		// TimeoutSec=0 + agent TimeoutSecs=0 exercises the 600s default.
	})
	if err == nil || !strings.Contains(err.Error(), "orchestrator:") {
		t.Errorf("expected orchestrator error, got %v", err)
	}
	if !strings.Contains(err.Error(), "not accepting") {
		t.Errorf("inner refusal lost: %v", err)
	}
}
