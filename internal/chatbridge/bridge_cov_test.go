package chatbridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"go.opentelemetry.io/otel/trace"

	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/ws"
)

// ---------- capturing resolver ----------

type runUpdateRec struct {
	runID    string
	status   string
	exitCode *int
	errorMsg *string
	metadata map[string]interface{}
}

// capResolver records every lifecycle call the bridge makes so tests can
// assert on run-state transitions, titles, and message-count deltas.
// Individual error knobs let a single test drive the "non-fatal failure"
// branches without a separate mock per call.
type capResolver struct {
	mu sync.Mutex

	info *ChatInfo

	titles       []string
	titleErr     error
	createRunErr error
	updateRunErr error
	incErr       error

	// onUpdateRun runs (unlocked) after each UpdateRun is recorded —
	// lets a test trigger side effects (e.g. context cancellation) at an
	// exact point in the bridge's post-run sequence.
	onUpdateRun func(status string)

	runCreates []string // runIDs
	runUpdates []runUpdateRec
	increments []int
}

func (c *capResolver) CreateChat(context.Context, CreateChatRequest) error { return nil }

func (c *capResolver) ResolveChat(context.Context, string) (*ChatInfo, error) {
	return c.info, nil
}

func (c *capResolver) ResolveAgent(context.Context, string, string) (*ChatInfo, error) {
	return c.info, nil
}

func (c *capResolver) GetWebhookSecret(context.Context, string, string) (string, error) {
	return "", nil
}

func (c *capResolver) CreateRun(_ context.Context, runID, _, _, _, _ string, _ map[string]interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.runCreates = append(c.runCreates, runID)
	return c.createRunErr
}

func (c *capResolver) UpdateRun(_ context.Context, runID, status string, exitCode *int, errorMsg *string, metadata map[string]interface{}) error {
	c.mu.Lock()
	c.runUpdates = append(c.runUpdates, runUpdateRec{
		runID: runID, status: status, exitCode: exitCode, errorMsg: errorMsg, metadata: metadata,
	})
	hook := c.onUpdateRun
	err := c.updateRunErr
	c.mu.Unlock()
	if hook != nil {
		hook(status)
	}
	return err
}

func (c *capResolver) IncrementMessageCount(_ context.Context, _ string, delta int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.increments = append(c.increments, delta)
	return c.incErr
}

func (c *capResolver) UpdateChatTitle(_ context.Context, _, title string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.titles = append(c.titles, title)
	return c.titleErr
}

// ---------- scripted container provider ----------

// scriptedContainer drives the full HandleChatMessage flow. Setup execs get
// empty no-op readers; the agent CLI exec (the only one carrying Env) gets
// agentOutput (claude stream-json) or agentExecErr. exitCode feeds
// ExecInspect for BOTH the tmux probe and the final agent exec, so exitCode=0
// means "tmux available + agent exited 0".
type scriptedContainer struct {
	mu           sync.Mutex
	ensureCfgs   []provider.CrewConfig
	agentOutput  string
	agentExecErr error
	exitCode     int
}

func (s *scriptedContainer) EnsureCrewRuntime(_ context.Context, cc provider.CrewConfig) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureCfgs = append(s.ensureCfgs, cc)
	return "container-abcdef123456", nil
}
func (s *scriptedContainer) StopCrewRuntime(context.Context, string) error   { return nil }
func (s *scriptedContainer) RemoveCrewRuntime(context.Context, string) error { return nil }
func (s *scriptedContainer) ContainerStatus(context.Context, string) (*provider.ContainerStatus, error) {
	return &provider.ContainerStatus{ID: "container-abcdef123456", State: "running"}, nil
}
func (s *scriptedContainer) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	// The agent CLI exec is the only Exec call that carries Env (RunAgent
	// always appends CREWSHIP_SECRETS_DIR / CREWSHIP_OUTPUT_DIR). All setup
	// calls (mkdir, manifest, tmux probe, config writes) pass no Env.
	if len(cfg.Env) > 0 {
		if s.agentExecErr != nil {
			return nil, s.agentExecErr
		}
		return &provider.ExecResult{
			ExecID: "agent-exec",
			Reader: io.NopCloser(strings.NewReader(s.agentOutput)),
		}, nil
	}
	return &provider.ExecResult{ExecID: "noop", Reader: io.NopCloser(strings.NewReader(""))}, nil
}
func (s *scriptedContainer) ExecInspect(context.Context, string) (bool, int, error) {
	return false, s.exitCode, nil
}
func (s *scriptedContainer) ContainerStats(context.Context, string) (*provider.ContainerMetrics, error) {
	return nil, errors.New("stats stub")
}
func (s *scriptedContainer) CrewContainerName(_ string, slug string) string {
	return "crewship-team-" + slug
}
func (s *scriptedContainer) CopyToContainer(context.Context, string, string, io.Reader) error {
	return nil
}

func (s *scriptedContainer) lastEnsureCfg(t *testing.T) provider.CrewConfig {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.ensureCfgs) == 0 {
		t.Fatal("EnsureCrewRuntime was never called")
	}
	return s.ensureCfgs[len(s.ensureCfgs)-1]
}

// sidecarScriptedContainer adds the optional SidecarProvider capability.
type sidecarScriptedContainer struct {
	scriptedContainer
	svcMu       sync.Mutex
	svcCfgs     []provider.CrewConfig
	servicesErr error
}

func (s *sidecarScriptedContainer) EnsureCrewServices(_ context.Context, cc provider.CrewConfig) (map[string]string, error) {
	s.svcMu.Lock()
	defer s.svcMu.Unlock()
	s.svcCfgs = append(s.svcCfgs, cc)
	if s.servicesErr != nil {
		return nil, s.servicesErr
	}
	ids := map[string]string{}
	for _, svc := range cc.Services {
		ids[svc.Name] = "svc-" + svc.Name
	}
	return ids, nil
}

func (s *sidecarScriptedContainer) StopCrewServices(context.Context, string) error   { return nil }
func (s *sidecarScriptedContainer) RemoveCrewServices(context.Context, string) error { return nil }

// Compile-time guarantee that the test double actually advertises the
// sidecar capability the bridge type-asserts for.
var _ provider.SidecarProvider = (*sidecarScriptedContainer)(nil)

func baseInfo() *ChatInfo {
	return &ChatInfo{
		AgentID:     "agent-1",
		AgentSlug:   "valid-slug",
		CrewID:      "crew-1",
		CrewSlug:    "ops",
		WorkspaceID: "ws-1",
		CLIAdapter:  "CLAUDE_CODE",
		ToolProfile: "CODING",
		TimeoutSecs: 30,
	}
}

// claudeSuccessOutput builds a stream-json transcript with streamed text,
// nTools tool calls, one oversized tool result, and a final result envelope
// carrying cost/usage metadata.
func claudeSuccessOutput(nTools int) string {
	var b strings.Builder
	b.WriteString(`{"type":"system","subtype":"init","model":"claude-test"}` + "\n")
	b.WriteString(`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello"}}}` + "\n")
	b.WriteString(`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":" world"}}}` + "\n")
	for i := 0; i < nTools; i++ {
		// Distinct input per call: identical (name+args) tool calls repeated past
		// the orchestrator loop-guard threshold correctly abort the run as a
		// stuck loop, so a realistic multi-tool transcript varies them.
		fmt.Fprintf(&b, `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","id":"t%d","input":{"command":"echo %d"}}]}}`+"\n", i, i)
	}
	// 250-char tool result to exercise the 200-char truncation.
	long := strings.Repeat("A", 250)
	fmt.Fprintf(&b, `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t0","text":"%s"}]}}`+"\n", long)
	b.WriteString(`{"type":"result","subtype":"success","total_cost_usd":0.0042,"num_turns":2,"is_error":false,"usage":{"input_tokens":10,"output_tokens":5},"modelUsage":{"claude-test":{"input_tokens":10}}}` + "\n")
	return b.String()
}

// ---------- success path: text, tools, result metadata, done ----------

func TestHandleChatMessageSuccessFullFlow(t *testing.T) {
	resolver := &capResolver{info: baseInfo()}
	ctr := &scriptedContainer{agentOutput: claudeSuccessOutput(12), exitCode: 0}
	b := testBridgeWithContainer(t, resolver, ctr)

	var events []ws.ChatEvent
	streamFn := func(e ws.ChatEvent) { events = append(events, e) }

	err := b.HandleChatMessage(context.Background(), "user-1", "sess-ok", "hello", streamFn)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}

	// Done event must be the terminal event.
	if len(events) == 0 || events[len(events)-1].Type != "done" {
		t.Fatalf("last event = %+v, want done", events[len(events)-1:])
	}
	var sawText bool
	for _, e := range events {
		if e.Type == "text" {
			sawText = true
		}
		if e.Type == "error" {
			t.Errorf("success path must not stream error events, got %+v", e)
		}
	}
	if !sawText {
		t.Error("expected streamed text events")
	}

	// Run lifecycle: one CreateRun, one COMPLETED update with exit code 0
	// and the result metadata forwarded.
	if len(resolver.runCreates) != 1 {
		t.Fatalf("CreateRun calls = %d, want 1", len(resolver.runCreates))
	}
	if len(resolver.runUpdates) != 1 {
		t.Fatalf("UpdateRun calls = %d, want 1: %+v", len(resolver.runUpdates), resolver.runUpdates)
	}
	upd := resolver.runUpdates[0]
	if upd.status != "COMPLETED" {
		t.Errorf("run status = %q, want COMPLETED", upd.status)
	}
	if upd.runID != resolver.runCreates[0] {
		t.Errorf("UpdateRun runID %q != CreateRun runID %q", upd.runID, resolver.runCreates[0])
	}
	if upd.exitCode == nil || *upd.exitCode != 0 {
		t.Errorf("exitCode = %v, want 0", upd.exitCode)
	}
	if upd.metadata["total_cost_usd"] == nil {
		t.Errorf("completed metadata missing total_cost_usd: %v", upd.metadata)
	}
	if upd.metadata["num_turns"] == nil {
		t.Errorf("completed metadata missing num_turns: %v", upd.metadata)
	}
	if upd.metadata["usage"] == nil {
		t.Errorf("completed metadata missing usage: %v", upd.metadata)
	}
	if upd.metadata["model_usage"] == nil {
		t.Errorf("completed metadata missing model_usage: %v", upd.metadata)
	}
	if upd.metadata["duration_ms"] == nil {
		t.Errorf("completed metadata missing duration_ms: %v", upd.metadata)
	}

	// Conversation: user + assistant persisted, count incremented by 2.
	msgs, err := b.convStore.Read(context.Background(), "sess-ok", 0, 0)
	if err != nil {
		t.Fatalf("read conversation: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("persisted messages = %d, want 2", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "hello" {
		t.Errorf("first message = %+v, want user 'hello'", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "Hello world" {
		t.Errorf("assistant message = role %q content %q, want assistant 'Hello world'", msgs[1].Role, msgs[1].Content)
	}
	// 12 tool calls + 1 tool result = 13 summaries → capped at 10 + overflow note.
	if !strings.Contains(msgs[1].ToolSummary, "[tool: Bash]") {
		t.Errorf("tool summary missing tool call entries: %q", msgs[1].ToolSummary)
	}
	if !strings.Contains(msgs[1].ToolSummary, "...and 3 more") {
		t.Errorf("tool summary missing overflow note: %q", msgs[1].ToolSummary)
	}
	if len(resolver.increments) != 1 || resolver.increments[0] != 2 {
		t.Errorf("increments = %v, want [2]", resolver.increments)
	}
	// Auto-title from first message.
	if len(resolver.titles) != 1 || resolver.titles[0] != "hello" {
		t.Errorf("titles = %v, want [hello]", resolver.titles)
	}
}

// Short tool transcripts (≤10 summaries) join without the overflow note and
// the oversized tool result is truncated at 200 chars.
func TestHandleChatMessageToolSummaryShortAndTruncated(t *testing.T) {
	resolver := &capResolver{info: baseInfo()}
	ctr := &scriptedContainer{agentOutput: claudeSuccessOutput(2), exitCode: 0}
	b := testBridgeWithContainer(t, resolver, ctr)

	err := b.HandleChatMessage(context.Background(), "user-1", "sess-short", "hi", func(ws.ChatEvent) {})
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	msgs, err := b.convStore.Read(context.Background(), "sess-short", 0, 0)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("messages = %d (err %v), want 2", len(msgs), err)
	}
	ts := msgs[1].ToolSummary
	if strings.Contains(ts, "more") {
		t.Errorf("short summary must not carry overflow note: %q", ts)
	}
	// Tool result content was 250 'A's; persisted summary holds 200 + "...".
	if !strings.Contains(ts, strings.Repeat("A", 200)+"...") {
		t.Errorf("tool result not truncated at 200 chars: %q", ts)
	}
	if strings.Contains(ts, strings.Repeat("A", 201)) {
		t.Errorf("tool result kept more than 200 chars: %q", ts)
	}
}

// ---------- auto-title truncation + non-fatal title failure ----------

func TestHandleChatMessageTitleTruncationAndTitleError(t *testing.T) {
	resolver := &capResolver{info: baseInfo(), titleErr: errors.New("title write failed")}
	// No container provider: the flow errors AFTER the title update, which
	// is exactly the window this test needs.
	b, _ := testBridge(t, resolver)

	// 80 runes incl. multi-byte — truncation must cut at 57 runes + "...".
	content := strings.Repeat("é", 80)
	err := b.HandleChatMessage(context.Background(), "user-1", "sess-title", content, func(ws.ChatEvent) {})
	if err == nil || !strings.Contains(err.Error(), "no container provider") {
		t.Fatalf("expected provider error after title update, got: %v", err)
	}
	if len(resolver.titles) != 1 {
		t.Fatalf("titles = %v, want exactly one update (titleErr is non-fatal)", resolver.titles)
	}
	want := strings.Repeat("é", 57) + "..."
	if resolver.titles[0] != want {
		t.Errorf("title = %q (%d runes), want 57 runes + ellipsis", resolver.titles[0], len([]rune(resolver.titles[0])))
	}
}

// ---------- non-fatal CreateRun error + UpdateRun(FAILED) error ----------

func TestHandleChatMessageRunRecordErrorsAreNonFatal(t *testing.T) {
	resolver := &capResolver{
		info:         baseInfo(),
		createRunErr: errors.New("runs table locked"),
		updateRunErr: errors.New("runs table still locked"),
	}
	ctr := &scriptedContainer{agentExecErr: errors.New("exec exploded"), exitCode: 0}
	b := testBridgeWithContainer(t, resolver, ctr)

	var errEvents []ws.ChatEvent
	streamFn := func(e ws.ChatEvent) {
		if e.Type == "error" {
			errEvents = append(errEvents, e)
		}
	}
	err := b.HandleChatMessage(context.Background(), "user-1", "sess-recerr", "hi", streamFn)
	if err == nil {
		t.Fatal("expected run agent error")
	}
	// The surfaced error must be the agent failure, not the bookkeeping
	// failures — those are warn-only.
	if !strings.Contains(err.Error(), "run agent") {
		t.Errorf("error = %v, want 'run agent' wrap", err)
	}
	if len(resolver.runCreates) != 1 {
		t.Errorf("CreateRun calls = %d, want 1 (error must not retry/abort)", len(resolver.runCreates))
	}
	if len(resolver.runUpdates) != 1 || resolver.runUpdates[0].status != "FAILED" {
		t.Fatalf("runUpdates = %+v, want one FAILED", resolver.runUpdates)
	}
	if resolver.runUpdates[0].errorMsg == nil || !strings.Contains(*resolver.runUpdates[0].errorMsg, "exec exploded") {
		t.Errorf("FAILED errorMsg = %v, want exec failure text", resolver.runUpdates[0].errorMsg)
	}
	if len(errEvents) == 0 {
		t.Error("expected a streamed error event for the failed run")
	}
}

// ---------- cancellation mid-run ----------

// User presses stop while text is streaming: run is marked CANCELLED, the
// partial assistant response is persisted, message count bumps by 2, and no
// error event leaks to the client.
func TestHandleChatMessageCancelMidRunPersistsPartial(t *testing.T) {
	resolver := &capResolver{info: baseInfo()}
	ctr := &scriptedContainer{agentOutput: claudeSuccessOutput(0), exitCode: 0}
	b := testBridgeWithContainer(t, resolver, ctr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var errEvents []ws.ChatEvent
	streamFn := func(e ws.ChatEvent) {
		if e.Type == "text" {
			cancel() // simulate stop button mid-stream
		}
		if e.Type == "error" {
			errEvents = append(errEvents, e)
		}
	}

	err := b.HandleChatMessage(ctx, "user-1", "sess-cancel", "hi", streamFn)
	if err == nil || !strings.Contains(err.Error(), "run agent") {
		t.Fatalf("expected wrapped run agent cancellation error, got: %v", err)
	}
	if len(errEvents) != 0 {
		t.Errorf("cancellation must not stream error events, got %+v", errEvents)
	}
	if len(resolver.runUpdates) != 1 {
		t.Fatalf("runUpdates = %+v, want one CANCELLED", resolver.runUpdates)
	}
	upd := resolver.runUpdates[0]
	if upd.status != "CANCELLED" {
		t.Errorf("status = %q, want CANCELLED", upd.status)
	}
	if upd.errorMsg == nil || *upd.errorMsg != "cancelled" {
		t.Errorf("errorMsg = %v, want 'cancelled'", upd.errorMsg)
	}
	// Partial response was streamed before cancel → persisted + count of 2.
	msgs, rerr := b.convStore.Read(context.Background(), "sess-cancel", 0, 0)
	if rerr != nil {
		t.Fatalf("read conversation: %v", rerr)
	}
	if len(msgs) != 2 || msgs[1].Role != "assistant" || msgs[1].Content == "" {
		t.Fatalf("messages = %+v, want user + non-empty partial assistant", msgs)
	}
	if len(resolver.increments) != 1 || resolver.increments[0] != 2 {
		t.Errorf("increments = %v, want [2]", resolver.increments)
	}
}

// Stop before any text arrived: only the user message counts (delta 1) and
// no assistant message is persisted.
func TestHandleChatMessageCancelBeforeOutputCountsUserOnly(t *testing.T) {
	// updateRunErr also exercises the warn-only branch for a failed
	// CANCELLED status write — it must not change the returned error.
	resolver := &capResolver{info: baseInfo(), updateRunErr: errors.New("runs table locked")}
	ctr := &scriptedContainer{agentOutput: "", exitCode: 0}
	b := testBridgeWithContainer(t, resolver, ctr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	streamFn := func(e ws.ChatEvent) {
		if e.Type == "status" && e.Content == "Starting agent..." {
			cancel()
		}
	}
	err := b.HandleChatMessage(ctx, "user-1", "sess-cancel2", "hi", streamFn)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if len(resolver.runUpdates) != 1 || resolver.runUpdates[0].status != "CANCELLED" {
		t.Fatalf("runUpdates = %+v, want one CANCELLED", resolver.runUpdates)
	}
	msgs, rerr := b.convStore.Read(context.Background(), "sess-cancel2", 0, 0)
	if rerr != nil {
		t.Fatalf("read conversation: %v", rerr)
	}
	if len(msgs) != 1 || msgs[0].Role != "user" {
		t.Fatalf("messages = %+v, want only the user message", msgs)
	}
	if len(resolver.increments) != 1 || resolver.increments[0] != 1 {
		t.Errorf("increments = %v, want [1]", resolver.increments)
	}
}

// ---------- auto-provision enqueue outcomes ----------

func TestHandleChatMessageEnqueueFailureSurfacesFailedEvent(t *testing.T) {
	info := baseInfo()
	info.DevcontainerConfig = `{"image":"x","features":{"ghcr.io/devcontainers/features/go:1":{}}}`
	info.CachedImage = ""
	resolver := &capResolver{info: info}
	b, _ := testBridge(t, resolver)
	enq := &stubEnqueuer{returnError: errors.New("builder offline")}
	b.SetProvisioningEnqueuer(enq)

	var events []ws.ChatEvent
	err := b.HandleChatMessage(context.Background(), "u", "sess-enqfail", "hi", func(e ws.ChatEvent) { events = append(events, e) })
	if err == nil || !strings.Contains(err.Error(), "auto-provision enqueue failed") {
		t.Fatalf("error = %v, want auto-provision enqueue failure", err)
	}
	if !strings.Contains(err.Error(), "builder offline") {
		t.Errorf("underlying enqueue error must be wrapped (%%w), got: %v", err)
	}
	var evt *ws.ChatEvent
	for i := range events {
		if events[i].Type == "crew_provisioning" {
			evt = &events[i]
		}
	}
	if evt == nil {
		t.Fatalf("expected crew_provisioning event, got %+v", events)
	}
	meta, _ := evt.Metadata.(map[string]any)
	if meta["status"] != "failed" {
		t.Errorf("event status = %v, want failed", meta["status"])
	}
	if errStr, _ := meta["error"].(string); !strings.Contains(errStr, "builder offline") {
		t.Errorf("event metadata.error = %v, want enqueue error text", meta["error"])
	}
	if !strings.Contains(evt.Content, "Could not start build") {
		t.Errorf("event content = %q, want failure copy", evt.Content)
	}
}

func TestHandleChatMessageEnqueueAlreadyRunning(t *testing.T) {
	info := baseInfo()
	info.DevcontainerConfig = `{"image":"x","features":{"ghcr.io/devcontainers/features/go:1":{}}}`
	info.CachedImage = ""
	resolver := &capResolver{info: info}
	b, _ := testBridge(t, resolver)
	b.SetProvisioningEnqueuer(&stubEnqueuer{resRunning: true})

	var events []ws.ChatEvent
	err := b.HandleChatMessage(context.Background(), "u", "sess-enqrun", "hi", func(e ws.ChatEvent) { events = append(events, e) })
	if err == nil || !strings.Contains(err.Error(), "provisioning kicked off") {
		t.Fatalf("error = %v, want kicked-off sentinel", err)
	}
	var saw bool
	for _, e := range events {
		if e.Type == "crew_provisioning" {
			saw = true
			meta, _ := e.Metadata.(map[string]any)
			if meta["status"] != "running" {
				t.Errorf("event status = %v, want running (job already in flight)", meta["status"])
			}
		}
	}
	if !saw {
		t.Fatalf("expected crew_provisioning event, got %+v", events)
	}
}

// ---------- cold-start CrewConfig assembly from cached requirements ----------

func TestHandleChatMessageColdStartAppliesCachedRequirements(t *testing.T) {
	info := baseInfo()
	info.ContainerEnv = map[string]string{"FOO": "root"}
	info.RootPostStart = []string{"user-hook"}
	info.CachedRequirements = &devcontainer.AggregatedRequirements{
		ContainerEnv:      map[string]string{"FOO": "feature", "ONLY_FEATURE": "x"},
		Privileged:        true,
		Init:              true,
		CapAdd:            []string{"SYS_ADMIN"},
		SecurityOpt:       []string{"seccomp=unconfined"},
		Mounts:            []devcontainer.FeatureMount{{Source: "dind-${devcontainerId}", Target: "/var/lib/docker", Type: "volume"}},
		PostStartCommands: []string{"feature-hook"},
	}
	resolver := &capResolver{info: info}
	ctr := &scriptedContainer{agentExecErr: errors.New("exec stub"), exitCode: 0}
	b := testBridgeWithContainer(t, resolver, ctr)

	_ = b.HandleChatMessage(context.Background(), "u", "sess-req", "hi", func(ws.ChatEvent) {})

	cc := ctr.lastEnsureCfg(t)
	if !cc.Privileged || !cc.Init {
		t.Errorf("Privileged/Init = %v/%v, want true/true", cc.Privileged, cc.Init)
	}
	if len(cc.CapAdd) != 1 || cc.CapAdd[0] != "SYS_ADMIN" {
		t.Errorf("CapAdd = %v", cc.CapAdd)
	}
	if len(cc.SecurityOpt) != 1 || cc.SecurityOpt[0] != "seccomp=unconfined" {
		t.Errorf("SecurityOpt = %v", cc.SecurityOpt)
	}
	// Root env wins on conflict; feature-only keys survive.
	if cc.ContainerEnv["FOO"] != "root" {
		t.Errorf("ContainerEnv[FOO] = %q, want root", cc.ContainerEnv["FOO"])
	}
	if cc.ContainerEnv["ONLY_FEATURE"] != "x" {
		t.Errorf("ContainerEnv[ONLY_FEATURE] = %q, want x", cc.ContainerEnv["ONLY_FEATURE"])
	}
	// ${devcontainerId} must be expanded before Docker sees it.
	if len(cc.ExtraMounts) != 1 {
		t.Fatalf("ExtraMounts = %v", cc.ExtraMounts)
	}
	m := cc.ExtraMounts[0]
	if strings.Contains(m.Source, "$") {
		t.Errorf("mount source not expanded: %q", m.Source)
	}
	if want := devcontainer.ExpandVars("dind-${devcontainerId}", info.CrewID); m.Source != want {
		t.Errorf("mount source = %q, want %q", m.Source, want)
	}
	if m.Target != "/var/lib/docker" || m.Type != "volume" {
		t.Errorf("mount = %+v", m)
	}
	// Feature hooks first, user intent last.
	if len(cc.PostStartCommands) != 2 || cc.PostStartCommands[0] != "feature-hook" || cc.PostStartCommands[1] != "user-hook" {
		t.Errorf("PostStartCommands = %v, want [feature-hook user-hook]", cc.PostStartCommands)
	}
}

// ---------- sidecar services ----------

func TestHandleChatMessageServicesStartViaSidecarProvider(t *testing.T) {
	info := baseInfo()
	info.ServicesJSON = `[{"name":"db","image":"postgres:16","env_refs":["PG_PASS","MISSING"]}]`
	info.ServiceEnvLookup = func(envVar string) string {
		if envVar == "PG_PASS" {
			return "hunter2"
		}
		return ""
	}
	resolver := &capResolver{info: info}
	ctr := &sidecarScriptedContainer{
		scriptedContainer: scriptedContainer{agentOutput: claudeSuccessOutput(0), exitCode: 0},
	}
	b := testBridgeWithContainer(t, resolver, ctr)

	err := b.HandleChatMessage(context.Background(), "u", "sess-svc", "hi", func(ws.ChatEvent) {})
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}

	ctr.svcMu.Lock()
	defer ctr.svcMu.Unlock()
	if len(ctr.svcCfgs) != 1 {
		t.Fatalf("EnsureCrewServices calls = %d, want 1", len(ctr.svcCfgs))
	}
	svcs := ctr.svcCfgs[0].Services
	if len(svcs) != 1 || svcs[0].Name != "db" || svcs[0].Image != "postgres:16" {
		t.Fatalf("Services = %+v", svcs)
	}
	if svcs[0].Env["PG_PASS"] != "hunter2" {
		t.Errorf("env_ref not resolved into service env: %v", svcs[0].Env)
	}
	if _, ok := svcs[0].Env["MISSING"]; ok {
		t.Errorf("unresolvable env_ref must be dropped: %v", svcs[0].Env)
	}
}

func TestHandleChatMessageServicesErrorAborts(t *testing.T) {
	info := baseInfo()
	info.ServicesJSON = `[{"name":"db","image":"postgres:16"}]`
	resolver := &capResolver{info: info}
	ctr := &sidecarScriptedContainer{
		scriptedContainer: scriptedContainer{agentOutput: claudeSuccessOutput(0), exitCode: 0},
		servicesErr:       errors.New("port already allocated"),
	}
	b := testBridgeWithContainer(t, resolver, ctr)

	var errEvents []ws.ChatEvent
	err := b.HandleChatMessage(context.Background(), "u", "sess-svcerr", "hi", func(e ws.ChatEvent) {
		if e.Type == "error" {
			errEvents = append(errEvents, e)
		}
	})
	if err == nil || !strings.Contains(err.Error(), "ensure crew services") {
		t.Fatalf("error = %v, want 'ensure crew services' wrap", err)
	}
	found := false
	for _, e := range errEvents {
		if strings.Contains(e.Content, "failed to start sidecar services") && strings.Contains(e.Content, "port already allocated") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected sidecar failure error event, got %+v", errEvents)
	}
}

func TestHandleChatMessageInvalidServicesJSONSkipsSidecars(t *testing.T) {
	info := baseInfo()
	info.ServicesJSON = `[{"image":"postgres:16"}]` // missing name → decode rejects
	resolver := &capResolver{info: info}
	ctr := &sidecarScriptedContainer{
		scriptedContainer: scriptedContainer{agentOutput: claudeSuccessOutput(0), exitCode: 0},
	}
	b := testBridgeWithContainer(t, resolver, ctr)

	var statuses []string
	err := b.HandleChatMessage(context.Background(), "u", "sess-svcbad", "hi", func(e ws.ChatEvent) {
		if e.Type == "status" {
			statuses = append(statuses, e.Content)
		}
	})
	// Invalid services config must NOT fail the run — the agent runs solo.
	if err != nil {
		t.Fatalf("invalid services_json must be non-fatal, got: %v", err)
	}
	var saw bool
	for _, s := range statuses {
		if strings.Contains(s, "Sidecar services skipped") {
			saw = true
		}
	}
	if !saw {
		t.Errorf("expected 'Sidecar services skipped' status, got %v", statuses)
	}
	ctr.svcMu.Lock()
	defer ctr.svcMu.Unlock()
	if len(ctr.svcCfgs) != 0 {
		t.Errorf("EnsureCrewServices must not be called for invalid config, calls = %d", len(ctr.svcCfgs))
	}
}

func TestHandleChatMessageServicesUnsupportedProvider(t *testing.T) {
	info := baseInfo()
	info.ServicesJSON = `[{"name":"db","image":"postgres:16"}]`
	resolver := &capResolver{info: info}
	// Plain scriptedContainer does NOT implement SidecarProvider.
	ctr := &scriptedContainer{agentOutput: claudeSuccessOutput(0), exitCode: 0}
	b := testBridgeWithContainer(t, resolver, ctr)

	var statuses []string
	err := b.HandleChatMessage(context.Background(), "u", "sess-nosp", "hi", func(e ws.ChatEvent) {
		if e.Type == "status" {
			statuses = append(statuses, e.Content)
		}
	})
	if err != nil {
		t.Fatalf("missing sidecar capability must be non-fatal, got: %v", err)
	}
	var saw bool
	for _, s := range statuses {
		if strings.Contains(s, "provider doesn't support them yet") {
			saw = true
		}
	}
	if !saw {
		t.Errorf("expected dormant-services status event, got %v", statuses)
	}
}

// ---------- completed-run bookkeeping failures are non-fatal ----------

func TestHandleChatMessageCompletedBookkeepingErrorsNonFatal(t *testing.T) {
	resolver := &capResolver{
		info:         baseInfo(),
		updateRunErr: errors.New("runs table locked"),
		incErr:       errors.New("chats table locked"),
	}
	ctr := &scriptedContainer{agentOutput: claudeSuccessOutput(0), exitCode: 0}
	b := testBridgeWithContainer(t, resolver, ctr)

	var events []ws.ChatEvent
	err := b.HandleChatMessage(context.Background(), "u", "sess-bk", "hi", func(e ws.ChatEvent) { events = append(events, e) })
	if err != nil {
		t.Fatalf("bookkeeping failures must be non-fatal, got: %v", err)
	}
	if len(events) == 0 || events[len(events)-1].Type != "done" {
		t.Errorf("expected terminal done event, got %+v", events)
	}
	// COMPLETED update was still attempted despite the configured error.
	if len(resolver.runUpdates) != 1 || resolver.runUpdates[0].status != "COMPLETED" {
		t.Errorf("runUpdates = %+v, want one COMPLETED", resolver.runUpdates)
	}
	msgs, rerr := b.convStore.Read(context.Background(), "sess-bk", 0, 0)
	if rerr != nil || len(msgs) != 2 {
		t.Errorf("messages = %d (err %v), want 2 despite bookkeeping errors", len(msgs), rerr)
	}
}

// ---------- assistant persist failure ----------

// Cancelling the context exactly when the COMPLETED run update lands makes
// the subsequent assistant-message Append fail (the conversation store
// checks ctx.Err() first), driving the "failed to save response" branch.
func TestHandleChatMessagePersistAssistantFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	resolver := &capResolver{info: baseInfo()}
	resolver.onUpdateRun = func(status string) {
		if status == "COMPLETED" {
			cancel()
		}
	}
	ctr := &scriptedContainer{agentOutput: claudeSuccessOutput(0), exitCode: 0}
	b := testBridgeWithContainer(t, resolver, ctr)

	var errEvents []ws.ChatEvent
	err := b.HandleChatMessage(ctx, "u", "sess-persistfail", "hi", func(e ws.ChatEvent) {
		if e.Type == "error" {
			errEvents = append(errEvents, e)
		}
	})
	if err == nil || !strings.Contains(err.Error(), "persist assistant message") {
		t.Fatalf("error = %v, want 'persist assistant message' wrap", err)
	}
	found := false
	for _, e := range errEvents {
		if e.Content == "failed to save response" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'failed to save response' error event, got %+v", errEvents)
	}
	// User message persisted, assistant message not.
	msgs, rerr := b.convStore.Read(context.Background(), "sess-persistfail", 0, 0)
	if rerr != nil {
		t.Fatalf("read conversation: %v", rerr)
	}
	if len(msgs) != 1 || msgs[0].Role != "user" {
		t.Errorf("messages = %+v, want only the user message", msgs)
	}
}

// ---------- done event carries trace_id when a span is active ----------

func TestHandleChatMessageDoneCarriesTraceID(t *testing.T) {
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10},
		SpanID:     trace.SpanID{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08},
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	resolver := &capResolver{info: baseInfo()}
	ctr := &scriptedContainer{agentOutput: claudeSuccessOutput(0), exitCode: 0}
	b := testBridgeWithContainer(t, resolver, ctr)

	var done *ws.ChatEvent
	err := b.HandleChatMessage(ctx, "u", "sess-trace", "hi", func(e ws.ChatEvent) {
		if e.Type == "done" {
			cp := e
			done = &cp
		}
	})
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if done == nil {
		t.Fatal("no done event streamed")
	}
	meta, _ := done.Metadata.(map[string]any)
	if meta == nil {
		t.Fatalf("done metadata = %T, want map with trace_id", done.Metadata)
	}
	if meta["trace_id"] != sc.TraceID().String() {
		t.Errorf("trace_id = %v, want %s", meta["trace_id"], sc.TraceID().String())
	}
}

// ---------- log writer failure is non-fatal ----------

// A broken log collector (base path under a regular file) makes every
// logBuf.Append fail; the bridge must log at debug and keep streaming.
func TestHandleChatMessageLogWriteFailureNonFatal(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("file, not dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	logger := slog.Default()
	convStore := conversation.NewStore(dir, logger)
	logWriter := logcollector.NewWriter(filepath.Join(blocker, "logs"), logger)
	resolver := &capResolver{info: baseInfo()}
	ctr := &scriptedContainer{agentOutput: claudeSuccessOutput(1), exitCode: 0}
	orch := orchestrator.New(ctr, &memState{data: make(map[string]map[string][]byte)}, logger)
	b := New(orch, ctr, convStore, logWriter, resolver, BridgeConfig{}, logger)

	var sawDone bool
	err := b.HandleChatMessage(context.Background(), "u", "sess-logfail", "hi", func(e ws.ChatEvent) {
		if e.Type == "done" {
			sawDone = true
		}
	})
	if err != nil {
		t.Fatalf("log write failures must be non-fatal, got: %v", err)
	}
	if !sawDone {
		t.Error("expected done event despite log writer failure")
	}
	msgs, rerr := b.convStore.Read(context.Background(), "sess-logfail", 0, 0)
	if rerr != nil || len(msgs) != 2 {
		t.Errorf("messages = %d (err %v), want 2", len(msgs), rerr)
	}
}
