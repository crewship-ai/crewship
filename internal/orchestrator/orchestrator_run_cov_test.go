package orchestrator

// Coverage tests for orchestrator_run.go: RunAgentForAssignment,
// SetConversationStore, and the RunAgent branches around the approval gate,
// hooks, prompt assembly (lead/peer/recall/language), memory dirs, the
// sidecar lifecycle (reuse / restart / failure / unknown mode), MCP config
// failure, exit-code mapping, and the response journal tap.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/provider"
)

// ---- fakes ----

type covGate struct {
	mu  sync.Mutex
	dec ApprovalDecision
	err error
	got []ApprovalCheckInput
}

func (g *covGate) Check(_ context.Context, in ApprovalCheckInput) (ApprovalDecision, error) {
	g.mu.Lock()
	g.got = append(g.got, in)
	g.mu.Unlock()
	return g.dec, g.err
}

type covHooks struct {
	mu      sync.Mutex
	events  []string
	blockOn string
}

func (h *covHooks) Dispatch(_ context.Context, event string, _ HookEventContext) error {
	h.mu.Lock()
	h.events = append(h.events, event)
	h.mu.Unlock()
	if h.blockOn != "" && event == h.blockOn {
		return errors.New("blocked by policy hook")
	}
	return nil
}

func (h *covHooks) seen(event string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, e := range h.events {
		if e == event {
			return true
		}
	}
	return false
}

type covJournal struct {
	mu      sync.Mutex
	entries []JournalEntry
}

func (j *covJournal) Emit(_ context.Context, e JournalEntry) (string, error) {
	j.mu.Lock()
	j.entries = append(j.entries, e)
	j.mu.Unlock()
	return "j1", nil
}

func (j *covJournal) byType(typ string) []JournalEntry {
	j.mu.Lock()
	defer j.mu.Unlock()
	var out []JournalEntry
	for _, e := range j.entries {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

type covRecaller struct {
	out string
	err error
}

func (r *covRecaller) Recall(_ context.Context, _ EpisodicRecallInput) (string, error) {
	return r.out, r.err
}

// covFailSetState wraps memState and fails the FIRST Set call so the
// "failed to persist run state" branch executes while later writes succeed.
type covFailSetState struct {
	*memState
	mu     sync.Mutex
	failed bool
}

func (s *covFailSetState) Set(ctx context.Context, bucket, key string, value []byte) error {
	s.mu.Lock()
	first := !s.failed
	s.failed = true
	s.mu.Unlock()
	if first {
		return errors.New("bbolt unavailable")
	}
	return s.memState.Set(ctx, bucket, key, value)
}

// covRunContainer routes the exec calls RunAgent makes. tmux is reported as
// unavailable so the agent CLI runs via the plain `stdbuf -oL claude ...`
// fallback, which keeps the full argv (incl. --system-prompt) observable.
type covRunOpts struct {
	stream       string // agent stdout
	agentExit    int    // agent exit code
	agentRunning bool   // ExecInspect "still running"
	health       string // checkSidecar reply ("" → not running)
	sidecarExit  int    // startSidecar health script exit code
	failMCPWrite bool   // fail the .mcp.json write exec
}

func covNewRunContainer(opts covRunOpts) *covContainer {
	c := &covContainer{}
	c.route = func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
		script := covScript(cfg)
		switch {
		case strings.Contains(script, "command -v tmux"):
			return covResult("tmux-check", ""), nil
		case opts.failMCPWrite && strings.Contains(script, ".mcp.json"):
			return nil, errors.New("mcp write refused")
		case strings.Contains(script, "crewship-sidecar --addr"):
			return covResult("sidecar-start", ""), nil
		case strings.Contains(script, "9119/health"):
			return covResult("sidecar-health", opts.health), nil
		case len(cfg.Cmd) > 0 && cfg.Cmd[0] == "stdbuf":
			return covResult("agent-exec", opts.stream), nil
		}
		return nil, nil
	}
	c.inspect = func(execID string) (bool, int, error) {
		switch execID {
		case "tmux-check":
			return false, 1, nil // tmux missing → stdbuf fallback
		case "agent-exec":
			return opts.agentRunning, opts.agentExit, nil
		case "sidecar-start":
			return false, opts.sidecarExit, nil
		}
		return false, 0, nil
	}
	return c
}

// covAgentPrompt extracts the --system-prompt argv value from the agent exec.
func covAgentPrompt(t *testing.T, c *covContainer) string {
	t.Helper()
	for _, call := range c.snapshotCalls() {
		if len(call.Cmd) == 0 || call.Cmd[0] != "stdbuf" {
			continue
		}
		for i, arg := range call.Cmd {
			if arg == "--system-prompt" && i+1 < len(call.Cmd) {
				return call.Cmd[i+1]
			}
		}
	}
	t.Fatal("agent exec with --system-prompt not captured")
	return ""
}

func covRunReq() AgentRunRequest {
	return AgentRunRequest{
		AgentID:     "a1",
		AgentSlug:   "cov-agent",
		AgentRole:   "AGENT",
		CrewID:      "crew1",
		WorkspaceID: "ws1",
		ChatID:      "chat1",
		ContainerID: "container-abcdef1234567890",
		CLIAdapter:  "CLAUDE_CODE",
		UserMessage: "please do the thing",
		TimeoutSecs: 5,
	}
}

func covRunStatus(t *testing.T, st *memState, chatID string) string {
	t.Helper()
	raw, _ := st.Get(context.Background(), "agent_runs", chatID)
	if raw == nil {
		t.Fatal("no run state persisted")
	}
	var rs RunState
	if err := json.Unmarshal(raw, &rs); err != nil {
		t.Fatalf("unmarshal run state: %v", err)
	}
	return rs.Status
}

// ---- SetConversationStore / RunAgentForAssignment ----

func TestSetConversationStore_StoresPointer(t *testing.T) {
	t.Parallel()
	o := New(nil, nil, covQuietLogger())
	store := &conversation.Store{}
	o.SetConversationStore(store)
	if o.convStore != store {
		t.Error("conversation store was not stored")
	}
}

func TestRunAgentForAssignment_PropagatesRunAgent(t *testing.T) {
	t.Parallel()
	o := New(nil, newMemState(), covQuietLogger())
	o.StopAccepting()
	err := o.RunAgentForAssignment(context.Background(), covRunReq(), nil)
	if err == nil || !strings.Contains(err.Error(), "not accepting") {
		t.Fatalf("expected not-accepting error via RunAgent, got %v", err)
	}
}

func TestRunAgentForAssignment_SuccessfulRun(t *testing.T) {
	t.Parallel()
	c := covNewRunContainer(covRunOpts{stream: "{}\n"})
	st := newMemState()
	o := New(c, st, covQuietLogger())
	if err := o.RunAgentForAssignment(context.Background(), covRunReq(), nil); err != nil {
		t.Fatalf("RunAgentForAssignment: %v", err)
	}
	if got := covRunStatus(t, st, "chat1"); got != "completed" {
		t.Errorf("run status = %q, want completed", got)
	}
}

// ---- approval gate ----

func TestRunAgent_ApprovalGateError(t *testing.T) {
	t.Parallel()
	o := New(covNewRunContainer(covRunOpts{}), newMemState(), covQuietLogger())
	o.SetApprovalGate(&covGate{err: errors.New("gate db down")})
	err := o.RunAgent(context.Background(), covRunReq(), nil)
	if err == nil || !strings.Contains(err.Error(), "approval gate") {
		t.Fatalf("expected approval gate error, got %v", err)
	}
}

func TestRunAgent_ApprovalDeniedFiresHookAndAborts(t *testing.T) {
	t.Parallel()
	o := New(covNewRunContainer(covRunOpts{}), newMemState(), covQuietLogger())
	gate := &covGate{dec: ApprovalDecision{Required: true, Denied: true, Reason: "matched deny rule", RequestID: "rq1"}}
	hooks := &covHooks{}
	o.SetApprovalGate(gate)
	o.SetHooksDispatcher(hooks)

	req := covRunReq()
	req.ApprovalMode = "sync"
	err := o.RunAgent(context.Background(), req, nil)
	if err == nil || !strings.Contains(err.Error(), "run denied by approval") {
		t.Fatalf("expected denial, got %v", err)
	}
	if !hooks.seen("on_approval_requested") {
		t.Error("on_approval_requested hook must fire for gated decisions")
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if len(gate.got) != 1 || gate.got[0].Mode != "sync" || gate.got[0].Tool != "agent_run" {
		t.Errorf("gate input wrong: %+v", gate.got)
	}
}

func TestRunAgent_ApprovalPendingAborts(t *testing.T) {
	t.Parallel()
	o := New(covNewRunContainer(covRunOpts{}), newMemState(), covQuietLogger())
	o.SetApprovalGate(&covGate{dec: ApprovalDecision{Required: true, Pending: true, RequestID: "rq2", Reason: "needs human"}})
	req := covRunReq()
	req.ApprovalMode = "async"
	err := o.RunAgent(context.Background(), req, nil)
	if err == nil || !strings.Contains(err.Error(), "requires approval") || !strings.Contains(err.Error(), "rq2") {
		t.Fatalf("expected pending-approval error with request id, got %v", err)
	}
}

func TestRunAgent_ApprovalRequiredButApprovedProceeds(t *testing.T) {
	t.Parallel()
	st := newMemState()
	o := New(covNewRunContainer(covRunOpts{stream: "{}\n"}), st, covQuietLogger())
	hooks := &covHooks{}
	o.SetApprovalGate(&covGate{dec: ApprovalDecision{Required: true, Approved: true}})
	o.SetHooksDispatcher(hooks)
	if err := o.RunAgent(context.Background(), covRunReq(), nil); err != nil {
		t.Fatalf("approved run must proceed: %v", err)
	}
	if !hooks.seen("on_approval_requested") || !hooks.seen("pre_agent_start") || !hooks.seen("post_agent_stop") {
		t.Errorf("hook sequence incomplete: %v", hooks.events)
	}
	if got := covRunStatus(t, st, "chat1"); got != "completed" {
		t.Errorf("run status = %q, want completed", got)
	}
}

func TestRunAgent_PreAgentStartHookBlocks(t *testing.T) {
	t.Parallel()
	o := New(covNewRunContainer(covRunOpts{}), newMemState(), covQuietLogger())
	o.SetHooksDispatcher(&covHooks{blockOn: "pre_agent_start"})
	err := o.RunAgent(context.Background(), covRunReq(), nil)
	if err == nil || !strings.Contains(err.Error(), "pre_agent_start hook blocked") {
		t.Fatalf("expected hook block, got %v", err)
	}
}

// ---- journal + state branches ----

func TestRunAgent_LongUserMessageJournalTruncation(t *testing.T) {
	t.Parallel()
	j := &covJournal{}
	o := New(covNewRunContainer(covRunOpts{stream: "{}\n"}), newMemState(), covQuietLogger())
	o.SetJournal(j)
	req := covRunReq()
	req.UserMessage = strings.Repeat("u", 300)
	if err := o.RunAgent(context.Background(), req, nil); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	msgs := j.byType("chat.user_message")
	if len(msgs) != 1 {
		t.Fatalf("want 1 chat.user_message entry, got %d", len(msgs))
	}
	if !strings.HasSuffix(msgs[0].Summary, "…") {
		t.Errorf("summary must be truncated with ellipsis: %q", msgs[0].Summary)
	}
	if msgs[0].Payload["length_chars"] != 300 {
		t.Errorf("payload length_chars = %v, want 300", msgs[0].Payload["length_chars"])
	}
}

func TestRunAgent_StateSetFailureIsNonFatal(t *testing.T) {
	t.Parallel()
	st := &covFailSetState{memState: newMemState()}
	o := New(covNewRunContainer(covRunOpts{stream: "{}\n"}), st, covQuietLogger())
	if err := o.RunAgent(context.Background(), covRunReq(), nil); err != nil {
		t.Fatalf("run-state persistence failure must not abort: %v", err)
	}
}

func TestRunAgent_InvalidSlugRejected(t *testing.T) {
	t.Parallel()
	o := New(covNewRunContainer(covRunOpts{}), newMemState(), covQuietLogger())
	req := covRunReq()
	req.AgentSlug = "../escape"
	err := o.RunAgent(context.Background(), req, nil)
	if err == nil || !strings.Contains(err.Error(), "invalid agent slug") {
		t.Fatalf("expected slug rejection, got %v", err)
	}
}

// ---- prompt assembly ----

func TestRunAgent_PromptAssembly(t *testing.T) {
	t.Parallel()
	members := []CrewMember{{ID: "m1", Name: "Eva", Slug: "eva", RoleTitle: "Researcher", ChatID: "c-eva"}}

	t.Run("lead context", func(t *testing.T) {
		t.Parallel()
		c := covNewRunContainer(covRunOpts{stream: "{}\n"})
		o := New(c, newMemState(), covQuietLogger())
		req := covRunReq()
		req.AgentRole = "LEAD"
		req.CrewMembers = members
		req.SkipSidecar = true
		if err := o.RunAgent(context.Background(), req, nil); err != nil {
			t.Fatalf("RunAgent: %v", err)
		}
		prompt := covAgentPrompt(t, c)
		if !strings.Contains(prompt, "[CREW CONTEXT]") {
			t.Error("LEAD prompt must contain [CREW CONTEXT]")
		}
		if strings.Contains(prompt, "[PEER COMMUNICATION]") {
			t.Error("LEAD prompt must not contain peer block")
		}
	})

	t.Run("peer context and language", func(t *testing.T) {
		t.Parallel()
		c := covNewRunContainer(covRunOpts{stream: "{}\n"})
		o := New(c, newMemState(), covQuietLogger())
		req := covRunReq()
		req.CrewMembers = members
		req.PreferredLanguage = "Czech"
		if err := o.RunAgent(context.Background(), req, nil); err != nil {
			t.Fatalf("RunAgent: %v", err)
		}
		prompt := covAgentPrompt(t, c)
		if !strings.Contains(prompt, "[PEER COMMUNICATION]") {
			t.Error("agent prompt must contain [PEER COMMUNICATION]")
		}
		if !strings.Contains(prompt, "[LANGUAGE]") || !strings.Contains(prompt, "Czech") {
			t.Error("prompt must carry the preferred-language block")
		}
	})

	t.Run("episodic recall injected", func(t *testing.T) {
		t.Parallel()
		c := covNewRunContainer(covRunOpts{stream: "{}\n"})
		o := New(c, newMemState(), covQuietLogger())
		o.SetEpisodicRecall(&covRecaller{out: "[EPISODIC RECALL]\npast incident notes"})
		if err := o.RunAgent(context.Background(), covRunReq(), nil); err != nil {
			t.Fatalf("RunAgent: %v", err)
		}
		if prompt := covAgentPrompt(t, c); !strings.Contains(prompt, "past incident notes") {
			t.Error("recall output must be appended to the prompt")
		}
	})

	t.Run("recall failures are non-fatal", func(t *testing.T) {
		t.Parallel()
		for _, recallErr := range []error{
			errors.New("ollama unreachable: dial tcp"),
			errors.New("embedding dimension mismatch"),
		} {
			c := covNewRunContainer(covRunOpts{stream: "{}\n"})
			o := New(c, newMemState(), covQuietLogger())
			o.SetEpisodicRecall(&covRecaller{err: recallErr})
			if err := o.RunAgent(context.Background(), covRunReq(), nil); err != nil {
				t.Fatalf("recall error %v must not abort the run: %v", recallErr, err)
			}
			if prompt := covAgentPrompt(t, c); strings.Contains(prompt, "RECALL") {
				t.Error("failed recall must not inject anything")
			}
		}
	})
}

// ---- memory dirs ----

func TestRunAgent_MemoryEnabledCreatesDirsAndMigrates(t *testing.T) {
	t.Parallel()
	c := covNewRunContainer(covRunOpts{stream: "{}\n"})
	o := New(c, newMemState(), covQuietLogger())
	req := covRunReq()
	req.MemoryEnabled = true
	if err := o.RunAgent(context.Background(), req, nil); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	var sawAgentMem, sawCrewMem, sawMigration bool
	for _, call := range c.snapshotCalls() {
		script := covScript(call)
		if strings.HasPrefix(script, "mkdir -p ") && strings.Contains(script, "/crew/agents/cov-agent/.memory/daily") {
			sawAgentMem = true
		}
		if strings.HasPrefix(script, "mkdir -p ") && strings.Contains(script, "/crew/shared/.memory/topics") {
			sawCrewMem = true
		}
		if strings.Contains(script, "cp -a") && strings.Contains(script, "/output/cov-agent/.memory") {
			sawMigration = true
		}
	}
	if !sawAgentMem {
		t.Error("agent .memory dirs not created")
	}
	if !sawCrewMem {
		t.Error("crew shared .memory dirs not created (CrewID is set)")
	}
	if !sawMigration {
		t.Error("legacy memory migration script not executed")
	}
}

// ---- sidecar lifecycle ----

func TestRunAgent_UnknownNetworkModeFailsRun(t *testing.T) {
	t.Parallel()
	st := newMemState()
	o := New(covNewRunContainer(covRunOpts{}), st, covQuietLogger())
	o.SetSidecarEnabled(true)
	req := covRunReq()
	req.NetworkMode = "yolo"
	err := o.RunAgent(context.Background(), req, nil)
	if err == nil || !strings.Contains(err.Error(), "unknown network mode: yolo") {
		t.Fatalf("expected unknown network mode error, got %v", err)
	}
	if got := covRunStatus(t, st, "chat1"); got != "error" {
		t.Errorf("run status = %q, want error", got)
	}
}

func TestRunAgent_SidecarReusedInFreeMode(t *testing.T) {
	t.Parallel()
	c := covNewRunContainer(covRunOpts{stream: "{}\n", health: `{"status":"ok","network_mode":"free"}`})
	o := New(c, newMemState(), covQuietLogger())
	o.SetSidecarEnabled(true)
	if err := o.RunAgent(context.Background(), covRunReq(), nil); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	for _, call := range c.snapshotCalls() {
		if strings.Contains(covScript(call), "crewship-sidecar --addr") {
			t.Fatal("healthy free-mode sidecar must be reused, not restarted")
		}
	}
}

func TestRunAgent_RestrictedModeRestartsSidecarWithDomains(t *testing.T) {
	t.Parallel()
	c := covNewRunContainer(covRunOpts{stream: "{}\n", health: `{"status":"ok","network_mode":"free"}`})
	o := New(c, newMemState(), covQuietLogger())
	o.SetSidecarEnabled(true)
	o.SetIPCConfig("http://gw:9000", "master-secret")

	req := covRunReq()
	req.AgentRole = "LEAD"
	req.CrewMembers = []CrewMember{{ID: "m1", Name: "Eva", Slug: "eva", ChatID: "c-eva"}}
	req.NetworkMode = "restricted"
	req.AllowedDomains = []string{"example.com"}
	req.MCPServers = []MCPServerConfig{{
		Name: "github", Transport: "stdio", Command: "npx",
		Args: []string{"-y", "@modelcontextprotocol/server-github"},
	}}
	if err := o.RunAgent(context.Background(), req, nil); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	var sawPkill bool
	var launchScript string
	for _, call := range c.snapshotCalls() {
		script := covScript(call)
		if strings.Contains(script, "pkill -f crewship-sidecar") {
			sawPkill = true
		}
		if strings.Contains(script, "crewship-sidecar --addr") {
			launchScript = script
		}
	}
	if !sawPkill {
		t.Error("stale free-mode sidecar must be killed before restricted restart")
	}
	if launchScript == "" {
		t.Fatal("sidecar was never restarted")
	}
	input := covDecodeSidecarInput(t, launchScript)
	np, _ := input["network_policy"].(map[string]any)
	if np["mode"] != "restricted" {
		t.Fatalf("network policy mode = %v", np["mode"])
	}
	domains := fmt.Sprintf("%v", np["allowed_domains"])
	if !strings.Contains(domains, "example.com") || !strings.Contains(domains, "api.github.com") {
		t.Errorf("allowed domains must include explicit + auto MCP domains: %v", domains)
	}
	ipc, _ := input["ipc"].(map[string]any)
	if ipc == nil {
		t.Fatal("LEAD run must carry IPC config")
	}
	if tok, _ := ipc["token"].(string); tok == "" || tok == "master-secret" {
		t.Errorf("IPC token must be a workspace-derived token, never the master: %q", tok)
	}
	cm, _ := input["crew_members"].([]any)
	if len(cm) != 1 || cm[0].(map[string]any)["slug"] != "eva" {
		t.Errorf("crew members not handed to sidecar: %v", input["crew_members"])
	}
}

func TestRunAgent_SidecarStartFailureAborts(t *testing.T) {
	t.Parallel()
	st := newMemState()
	c := covNewRunContainer(covRunOpts{stream: "{}\n", health: "", sidecarExit: 1})
	o := New(c, st, covQuietLogger())
	o.SetSidecarEnabled(true)
	err := o.RunAgent(context.Background(), covRunReq(), nil)
	if err == nil || !strings.Contains(err.Error(), "start sidecar") {
		t.Fatalf("expected sidecar start failure, got %v", err)
	}
	if got := covRunStatus(t, st, "chat1"); got != "error" {
		t.Errorf("run status = %q, want error", got)
	}
}

// ---- MCP config failure ----

func TestRunAgent_MCPWriteFailureAbortsWhenMCPConfigured(t *testing.T) {
	t.Parallel()
	st := newMemState()
	c := covNewRunContainer(covRunOpts{stream: "{}\n", failMCPWrite: true})
	o := New(c, st, covQuietLogger())
	req := covRunReq()
	req.CrewMCPConfigJSON = `{"mcpServers":{"x":{"command":"node","args":["s.js"]}}}`
	err := o.RunAgent(context.Background(), req, nil)
	if err == nil || !strings.Contains(err.Error(), "inject MCP config (CLAUDE_CODE)") {
		t.Fatalf("expected MCP injection failure, got %v", err)
	}
	if got := covRunStatus(t, st, "chat1"); got != "error" {
		t.Errorf("run status = %q, want error", got)
	}
}

// ---- exit code mapping ----

func TestRunAgent_ExitCodeMapping(t *testing.T) {
	t.Parallel()

	t.Run("exit 123 explains token problem", func(t *testing.T) {
		t.Parallel()
		st := newMemState()
		o := New(covNewRunContainer(covRunOpts{stream: "{}\n", agentExit: 123}), st, covQuietLogger())
		err := o.RunAgent(context.Background(), covRunReq(), nil)
		if err == nil || !strings.Contains(err.Error(), "missing or invalid CLI token") {
			t.Fatalf("expected token guidance for exit 123, got %v", err)
		}
		if got := covRunStatus(t, st, "chat1"); got != "error" {
			t.Errorf("run status = %q, want error", got)
		}
	})

	t.Run("generic non-zero exit", func(t *testing.T) {
		t.Parallel()
		o := New(covNewRunContainer(covRunOpts{stream: "{}\n", agentExit: 5}), newMemState(), covQuietLogger())
		err := o.RunAgent(context.Background(), covRunReq(), nil)
		if err == nil || !strings.Contains(err.Error(), "agent exited with code 5") {
			t.Fatalf("expected generic exit error, got %v", err)
		}
	})

	t.Run("still running keeps status running", func(t *testing.T) {
		t.Parallel()
		st := newMemState()
		j := &covJournal{}
		o := New(covNewRunContainer(covRunOpts{stream: "{}\n", agentRunning: true}), st, covQuietLogger())
		o.SetJournal(j)
		if err := o.RunAgent(context.Background(), covRunReq(), nil); err != nil {
			t.Fatalf("still-running exec must return nil: %v", err)
		}
		if got := covRunStatus(t, st, "chat1"); got != "running" {
			t.Errorf("run status = %q, want running", got)
		}
	})
}

// ---- response journal tap ----

func TestRunAgent_ResponseJournalCapAndToolTap(t *testing.T) {
	t.Parallel()
	bigA := strings.Repeat("a", 8000)
	bigB := strings.Repeat("b", 2000)
	line := func(text string) string {
		b, _ := json.Marshal(map[string]any{
			"type": "stream_event",
			"event": map[string]any{
				"type":  "delta",
				"delta": map[string]any{"type": "text_delta", "text": text},
			},
		})
		return string(b)
	}
	stream := line(bigA) + "\n" + line(bigB) + "\n" +
		`{"type":"assistant","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]}` + "\n"

	j := &covJournal{}
	c := covNewRunContainer(covRunOpts{stream: stream})
	o := New(c, newMemState(), covQuietLogger())
	o.SetJournal(j)

	var events []AgentEvent
	var evMu sync.Mutex
	handler := func(e AgentEvent) {
		evMu.Lock()
		events = append(events, e)
		evMu.Unlock()
	}
	if err := o.RunAgent(context.Background(), covRunReq(), handler); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	resp := j.byType("chat.agent_response")
	if len(resp) != 1 {
		t.Fatalf("want 1 chat.agent_response entry, got %d", len(resp))
	}
	if resp[0].Payload["truncated"] != true {
		t.Errorf("response over 8KB must be flagged truncated: %v", resp[0].Payload["truncated"])
	}
	if !strings.HasSuffix(resp[0].Summary, "…") {
		t.Errorf("summary must be capped at 240 chars with ellipsis: len=%d", len(resp[0].Summary))
	}
	content, _ := resp[0].Payload["content"].(string)
	if len(content) != 8192 {
		t.Errorf("captured content must be capped at 8192 bytes, got %d", len(content))
	}

	// The handler still receives the full event flow (text + tool_call).
	evMu.Lock()
	defer evMu.Unlock()
	var sawText, sawTool bool
	for _, e := range events {
		if e.Type == "text" {
			sawText = true
		}
		if e.Type == "tool_call" && e.Content == "Bash" {
			sawTool = true
		}
	}
	if !sawText || !sawTool {
		t.Errorf("handler missed events: text=%v tool=%v", sawText, sawTool)
	}

	// exec.command journal entries must bracket the run (start + end phases).
	execEntries := j.byType("exec.command")
	if len(execEntries) < 2 {
		t.Fatalf("want start+end exec.command entries, got %d", len(execEntries))
	}
	if execEntries[0].Payload["phase"] != "start" {
		t.Errorf("first exec.command phase = %v, want start", execEntries[0].Payload["phase"])
	}
	last := execEntries[len(execEntries)-1]
	if last.Payload["phase"] != "end" || last.Payload["exit_code"] != 0 {
		t.Errorf("closing exec.command wrong: %v", last.Payload)
	}
}
