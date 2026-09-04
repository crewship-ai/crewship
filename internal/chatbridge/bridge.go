package chatbridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/admission"
	"github.com/crewship-ai/crewship/internal/askforms"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/crewstart"
	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/telemetry"
	"github.com/crewship-ai/crewship/internal/ws"
)

// noOutputChatMessage is the user-facing copy for a run that finished
// cleanly but produced zero output (issue #545 — safety refusal
// swallowed by the adapter, prompt-budget pressure, or the agent CLI
// exiting 0 with no stdout). Kept short and actionable, matching the
// generic-error copy style used elsewhere in the chat stream.
const noOutputChatMessage = "The agent returned no output — try again"

// ledgerWriteTimeout bounds the terminal bookkeeping writes that must outlive
// the turn's own context — the paymaster ledger write, and the CANCELLED
// branch's run-status/transcript writes. Both run after the agent has stopped,
// on a context that may already be cancelled, so they get a fresh deadline
// rather than an inherited (possibly dead) one.
const ledgerWriteTimeout = 5 * time.Second

// AgentStatusPendingReview is the agents.status sentinel set by the
// hire endpoint when the per-crew autonomy policy returns
// DecisionInboxApprove (guided autonomy). The chatbridge refuses to
// start an agent in this state until the approve-hire endpoint flips
// it back to IDLE. Lives in chatbridge instead of api to avoid a
// circular import (api → chatbridge → api).
const AgentStatusPendingReview = "PENDING_REVIEW"

// ChatResolver provides the data layer for the chat bridge, resolving chat
// sessions to agent configurations and managing run lifecycle records.
type ChatResolver interface {
	CreateChat(ctx context.Context, req CreateChatRequest) error
	ResolveChat(ctx context.Context, chatID string) (*ChatInfo, error)
	// ResolveAgent resolves an agent ID to its configuration. workspaceID
	// is an OPTIONAL tenant scope: when non-empty the resolver constrains
	// the lookup to that workspace and a cross-tenant agent id yields a
	// 404 (treated as "not found"). Callers that have already
	// workspace-validated the agent id (e.g. the pipeline runner, which
	// resolves the id via a workspace-joined query) pass it so the
	// server-side scope engages; callers without a known workspace pass "".
	ResolveAgent(ctx context.Context, agentID, workspaceID string) (*ChatInfo, error)
	// NOTE: GetWebhookSecret was removed from this interface (#999). The
	// webhook trigger handler now reads agents.webhook_secret straight from
	// its local DB (crew-scoped) — the signing secret must never transit an
	// IPC/HTTP hop in plaintext, and no other consumer existed.
	CreateRun(ctx context.Context, runID, agentID, chatID, workspaceID, triggerType string, metadata map[string]interface{}) error
	UpdateRun(ctx context.Context, runID, status string, exitCode *int, errorMsg *string, metadata map[string]interface{}) error
	IncrementMessageCount(ctx context.Context, chatID string, delta int) error
	UpdateChatTitle(ctx context.Context, chatID, title string) error
	// RecordCost forwards a completed run's CLI-reported token usage to the
	// paymaster cost ledger (#1205). Best-effort by contract: callers log
	// and continue on error rather than fail the run over a billing write.
	RecordCost(ctx context.Context, usage RunCostUsage) error
}

// ChatInfo holds the resolved configuration for a chat session, including
// agent identity, crew context, credentials, and resource settings.
type ChatInfo struct {
	AgentID   string
	AgentSlug string
	AgentRole string
	// AgentStatus is the agents.status column at resolve time. Used by
	// the bridge to refuse to start an agent that's PENDING_REVIEW
	// (guided-autonomy hire waiting on operator approval). Empty when
	// the resolver doesn't surface status (legacy paths default to
	// permissive — only PENDING_REVIEW is treated as blocking).
	AgentStatus string
	CrewID      string
	CrewSlug    string
	ContainerID string
	CLIAdapter  string
	LLMModel    string
	// LLMProvider is the agent's configured provider (ANTHROPIC, OPENAI,
	// GOOGLE, OLLAMA, …). Carried so the OPENCODE adapter can qualify a bare
	// model into "provider/model" form (#1007); ignored by other adapters.
	LLMProvider string
	// LocalModelBaseURL is the OpenAI-compatible local-model endpoint the
	// server resolved from the vault (ENDPOINT_URL credential, #955). Empty
	// → orchestrator applies the deprecated env fallback.
	LocalModelBaseURL string
	// LocalModelAPIKey / LocalModelHeaders carry optional endpoint auth (#961).
	LocalModelAPIKey  string
	LocalModelHeaders map[string]string
	SystemPrompt      string
	ToolProfile       string
	Credentials       []orchestrator.Credential
	TimeoutSecs       int
	WorkspaceID       string
	MemoryEnabled     bool
	CrewMembers       []orchestrator.CrewMember
	// ConnectedCrews is what a LEAD may dispatch across. Empty for every
	// other role: only a lead orchestrates.
	ConnectedCrews        []orchestrator.ConnectedCrew
	NetworkMode           string
	AllowedDomains        []string
	AllowPrivateEndpoints bool
	MemoryMB              int
	CPUs                  float64
	TTLHours              int
	RuntimeImage          string
	CachedImage           string
	DevcontainerConfig    string
	MiseConfig            string
	ServicesJSON          string
	// ServiceEnvLookup resolves a credential env-var name to its
	// plaintext value (for env_refs in services_json). Nil is a
	// safe default — env_refs that can't be resolved are simply
	// not injected. Provided by the agent-config loader which has
	// access to the credential vault.
	ServiceEnvLookup func(envVar string) string
	ContainerEnv     map[string]string
	// CachedRequirements are aggregated feature requirements (privileged,
	// capAdd, mounts, securityOpt) persisted at provision time and applied
	// to the HostConfig. Nil means no extra requirements.
	CachedRequirements *devcontainer.AggregatedRequirements
	// RootPostStart is the normalized root-level postStartCommand parsed from
	// the crew's devcontainer_config. Appended to feature-level post-start
	// hooks (from CachedRequirements.PostStartCommands) so that user intent
	// wins over feature defaults.
	RootPostStart      []string
	MCPServers         []orchestrator.MCPServerConfig
	CrewMCPConfigJSON  string
	AgentMCPConfigJSON string
	PreferredLanguage  string
	InstalledSkills    []orchestrator.SkillBundle

	// PR-E F6 — opener identity for per-user peer card injection.
	// Sourced from chats.created_by by the resolver. Empty for
	// non-chat invocations (routine dispatch). RoleTitle is the
	// human-facing title used as the DefaultPersona seed when both
	// PERSONA layers are empty.
	OpenedByUserID string
	RoleTitle      string

	// Visibility is "group" for a multi-user group chat (agent runs only when
	// @mentioned) or "private"/empty for a normal 1:1 chat. Sourced from
	// chats.visibility by the resolver.
	Visibility string

	// ApprovalMode is the harbormaster HITL gate mode ("none"|"async"|
	// "sync") derived from the crew's autonomy_level policy by the resolver
	// (#810). Empty is treated as "none". Threaded through the builder so
	// EVERY dispatch path (chat, pipeline, cron, webhook, mission, peer)
	// stamps it onto the run — before this it was never set and the gate
	// short-circuited Approved on every path.
	ApprovalMode string
}

// ProvisioningEnqueueResult mirrors api.EnqueueResult shape locally so the
// chatbridge interface doesn't import the api package — api depends on
// chatbridge (ChatHandler), which would create a cycle.
type ProvisioningEnqueueResult struct {
	Started        bool
	AlreadyRunning bool
	Status         string
}

// PendingChatMessage is a chat send that HandleChatMessage deferred because
// its crew's devcontainer needed a build (the needsProvision branch below).
// It is handed to AttachPendingMessage so the provisioning job itself can
// resume (or fail) it once the build reaches a terminal state — the server
// owns the resume, instead of leaving a client to notice the build finished
// and resend on its own (which is the race this type exists to close: the
// completion frame on the workspace realtime channel is easy to miss, but
// nothing can miss a callback made from inside the job's own goroutine).
type PendingChatMessage struct {
	UserID  string
	ChatID  string
	Content string
	// Opts mirrors the variadic ws.ChatMessageOption HandleChatMessage
	// received on the original send, so the resumed run honours the same
	// MaxTurns / Metadata the deferred message carried.
	Opts ws.ChatMessageOption
}

// ProvisioningEnqueuer kicks off an asynchronous provisioning job for a crew
// whose devcontainer image hasn't been built yet. Wired in by the server so
// the bridge can auto-trigger a build when a user's first message lands on
// an unprovisioned crew, instead of erroring with "run `crewship crew
// provision …` first" — the GUI has no terminal context for that hint.
type ProvisioningEnqueuer interface {
	EnqueueForCrew(ctx context.Context, crewID, workspaceID string) (ProvisioningEnqueueResult, error)
	// AttachPendingMessage attaches a deferred send to crewID's tracked
	// provisioning job so the job resumes or fails it exactly once when it
	// reaches a terminal state — see PendingChatMessage and
	// api.ProvisioningHandler.AttachPendingMessage for the at-most-once
	// mechanics (keyed by ChatID, drained atomically with the status
	// transition). Returns false only when no job is tracked for crewID at
	// all, which is not treated as fatal by the caller: the
	// crew_provisioning event already told the user to resend manually.
	AttachPendingMessage(crewID string, msg PendingChatMessage) bool
}

// imagePresenceChecker is the optional capability the bridge uses to detect
// when a crew's recorded cached image has been pruned from the local Docker
// daemon. The docker provider implements it (ImagePresentLocally); providers
// that don't are treated as "image present" so we never spuriously
// re-provision. Local interface (not provider.ContainerProvider) to keep the
// capability opt-in and avoid forcing every provider to implement it.
type imagePresenceChecker interface {
	ImagePresentLocally(ctx context.Context, ref string) (bool, error)
}

// Bridge connects the WebSocket chat interface to the orchestrator, resolving
// sessions, managing containers, persisting conversations, and streaming events.
type Bridge struct {
	orch         *orchestrator.Orchestrator
	container    provider.ContainerProvider
	convStore    *conversation.Store
	logWriter    *logcollector.Writer
	resolver     ChatResolver
	provisioning ProvisioningEnqueuer // optional; nil means auto-provision is disabled
	cfg          BridgeConfig
	logger       *slog.Logger

	// containerCache maps crewID → containerID so subsequent messages
	// skip the "Starting container..." status events (container is warm).
	containerMu    sync.RWMutex
	containerCache map[string]string

	// activeRunsMu guards activeRuns, the per-chat in-flight run counter
	// that powers mid-turn steering. It mirrors containerMu's role:
	// a small, dedicated lock for one map, not a single coarse Bridge
	// mutex. A count (not a bool) tolerates overlapping runs on the same
	// chat (e.g. a retried turn) without one finishing run clearing the
	// flag while another is still live.
	activeRunsMu sync.Mutex
	activeRuns   map[string]int

	// agentRunLock is the cross-surface, per-AGENT exclusivity lock: at most
	// one live RunAgent exec per agent, regardless of whether it was started
	// by a chat send (this file) or an assignment/@mention dispatch
	// (api.AssignmentHandler, wired to the SAME instance via SetAgentRunLock
	// so both doors share one claim). See AgentRunLock's doc for why chat id
	// alone (activeRuns above) doesn't cover the assignment case. Never
	// nil — always constructed in New().
	agentRunLock *AgentRunLock

	// assignmentPumper drains an agent's crew queue after THIS door
	// (HandleChatMessage) releases agentRunLock — see the doc on
	// SetAssignmentPumper. Nil until wired at boot (cmd_start.go); a nil
	// pumper is fail-open, same convention as api.AssignmentHandler's own
	// nil agentRunLock handling — a queued assignment then waits for an
	// unrelated crew completion or the stuck-QUEUED sweeper instead of
	// draining the instant this chat turn frees the agent.
	assignmentPumper AssignmentPumper

	// steerBroadcaster announces steering_queued events on the chat's
	// session channel. Optional: nil means the WS announcement is
	// skipped (the durable persist is the contract; the event is a UI
	// nicety). Wired post-construction via SetSteerBroadcaster because
	// the ws.Hub is built in the server boot sequence, same as the
	// ProvisioningEnqueuer.
	steerBroadcaster SteerBroadcaster

	// replyNotifier projects persisted assistant replies into the
	// unified inbox for users who aren't watching the session live.
	// Optional (nil = disabled); wired via SetReplyNotifier. See
	// notify.go.
	replyNotifier ReplyNotifier
}

// SetProvisioningEnqueuer wires the auto-provision trigger after Bridge
// construction. Done as a setter (not a constructor argument) because the
// api.ProvisioningHandler is built later in the server boot sequence and
// needs the Bridge already initialised for its WS handler hookup.
func (b *Bridge) SetProvisioningEnqueuer(p ProvisioningEnqueuer) {
	b.provisioning = p
}

// BridgeConfig holds default resource limits for containers created by the bridge.
type BridgeConfig struct {
	DefaultMemoryMB int
	DefaultCPUs     float64
}

// New creates a Bridge that connects WebSocket chat to the orchestrator.
func New(
	orch *orchestrator.Orchestrator,
	container provider.ContainerProvider,
	convStore *conversation.Store,
	logWriter *logcollector.Writer,
	resolver ChatResolver,
	cfg BridgeConfig,
	logger *slog.Logger,
) *Bridge {
	// Fallback only — primary path is crews.container_memory_mb threaded
	// through resolver. Kept generous because the old 512 MiB caused
	// Docker OOM-kills on real agent workloads (claude/gemini CLI +
	// MCP servers easily exceed 512 MiB once warmed up).
	// Use <=0 so a hand-rolled "-1 means unset" pattern (or any other
	// non-positive misconfig) lands on the safe default instead of
	// reaching Docker, which rejects negative resource limits.
	if cfg.DefaultMemoryMB <= 0 {
		cfg.DefaultMemoryMB = 8192
	}
	if cfg.DefaultCPUs <= 0 {
		cfg.DefaultCPUs = 2.0
	}
	return &Bridge{
		orch:           orch,
		container:      container,
		convStore:      convStore,
		logWriter:      logWriter,
		resolver:       resolver,
		cfg:            cfg,
		logger:         logger,
		containerCache: make(map[string]string),
		activeRuns:     make(map[string]int),
		agentRunLock:   NewAgentRunLock(),
	}
}

// AgentRunLock returns the Bridge's cross-surface per-agent exclusivity
// lock, so it can be shared with api.AssignmentHandler (see
// AssignmentHandler.SetAgentRunLock) — one lock, claimed by whichever door
// starts a live agent exec first, chat or assignment.
func (b *Bridge) AgentRunLock() *AgentRunLock {
	return b.agentRunLock
}

// ErrAgentBusyElsewhere wraps ws.ErrAgentBusy for the specific case where
// HandleChatMessage lost the CROSS-SURFACE AgentRunLock claim (below)
// rather than the per-chat tryMarkRunStart claim above it. errors.Is(err,
// ws.ErrAgentBusy) stays true either way, so a caller that only cares "was
// this rejected as busy" (ws/client.go's handleSendMessage, which renders
// the same sender-only busy notice regardless of which check failed) needs
// no change. A caller that DOES need to tell the two apart —
// crew_provisioning_jobs.go's deferred-message resume, which used to
// assume "another run for the SAME chat will settle the UI" and drop the
// message on ANY ErrAgentBusy — checks this sentinel first: for a
// cross-surface bounce, nothing is running for the deferred message's own
// chat, and dropping it loses the message outright (#2269 follow-up,
// defect 8).
var ErrAgentBusyElsewhere = errors.New("agent has a live run in progress on another chat or assignment")

// AssignmentPumper drains an agent's crew queue after that agent's
// AgentRunLock claim frees — implemented by api.AssignmentHandler
// (PumpForAgent). Declared here, not imported, to avoid an internal/api ->
// internal/chatbridge -> internal/api import cycle (the same reason
// AgentRunLock itself lives in this package rather than internal/api).
type AssignmentPumper interface {
	PumpForAgent(ctx context.Context, agentID string)
}

// SetAssignmentPumper wires the completion-path queue pump so a chat send's
// AgentRunLock release can drain any assignment that was queued behind it —
// see assignmentPumper's field doc for why this door needed its own pump
// trigger (#2269 follow-up, defect 5: HandleChatMessage's deferred
// agentRunLock.End previously triggered no pump at all). Called once at
// boot (cmd_start.go), alongside the existing SetAgentRunLock wiring that
// shares the lock itself between this Bridge and AssignmentHandler.
func (b *Bridge) SetAssignmentPumper(p AssignmentPumper) {
	b.assignmentPumper = p
}

func truncateID(id string, n int) string {
	if len(id) <= n {
		return id
	}
	return id[:n]
}

// devcontainerNeedsProvision reports whether the given devcontainer/mise
// configuration requires a provisioning pass before the crew can start.
// Configs that only set container metadata (e.g. containerEnv) are no-ops at
// provision time and the crew can launch directly from runtime_image.
func devcontainerNeedsProvision(cfgJSON, miseJSON string) bool {
	if strings.TrimSpace(miseJSON) != "" {
		return true
	}
	if strings.TrimSpace(cfgJSON) == "" {
		return false
	}
	cfg, err := devcontainer.ParseBytes([]byte(cfgJSON))
	if err != nil {
		// Unparseable config can't be provisioned either — don't block
		// the crew on something we can't act on.
		return false
	}
	return len(cfg.Features) > 0 || cfg.PostCreateCommand != nil
}

func generateMsgID() string {
	b := make([]byte, 8)
	now := time.Now().UnixNano()
	if _, err := rand.Read(b); err != nil {
		// Fallback format preserved: "msg_<unix-nano>" (no random suffix).
		var buf [32]byte
		out := append(buf[:0], "msg_"...)
		out = strconv.AppendInt(out, now, 10)
		return string(out)
	}
	// "msg_" + up-to-19-digit int64 + "_" + 16 hex chars ≤ 40 bytes.
	// Direct byte-append avoids the fmt.Sprintf + hex.EncodeToString
	// intermediate string allocations of the previous implementation.
	var buf [48]byte
	out := append(buf[:0], "msg_"...)
	out = strconv.AppendInt(out, now, 10)
	out = append(out, '_')
	out = hex.AppendEncode(out, b)
	return string(out)
}

// terminalRunMeta builds the metadata attached to a run's final UpdateRun.
//
// It exists so every terminal path carries session provenance, not just the
// happy one. "Which CLI binary answered, on which credential, and did it drop
// an MCP server at startup?" is a question asked about the run that went
// WRONG — recording it only on COMPLETED answers it exactly when nobody is
// asking. A cancelled or failed run has the same init event; the only thing it
// lacks is a result envelope.
//
// extra carries a path's own diagnosis (e.g. reason=no_output) and is applied
// last, so a caller can always override.
func terminalRunMeta(startedAt time.Time, acc *orchestrator.Accumulator, extra map[string]interface{}) map[string]interface{} {
	meta := map[string]interface{}{
		"duration_ms": time.Since(startedAt).Milliseconds(),
	}
	// Everything the accumulator captured, in one call — provenance AND result
	// usage. Merging only the provenance half here is what left a FAILED chat
	// run without permission_denials while a COMPLETED one had them, and the
	// no-output failure is exactly the run that gets read for "why did it do
	// nothing" (#1949).
	orchestrator.MergeRunAccumulator(meta, acc, "")
	for k, v := range extra {
		meta[k] = v
	}
	return meta
}

// HandleChatMessage processes an incoming chat message by resolving the session,
// ensuring the container is running, persisting the message, and streaming the
// agent's response back to the client.
func (b *Bridge) HandleChatMessage(ctx context.Context, userID, chatID, content string, streamFn func(ws.ChatEvent), opts ...ws.ChatMessageOption) error {
	b.logger.Debug("HandleChatMessage", "chat_id", chatID, "content_len", len(content))

	// Per-message run overrides (currently just MaxTurns from the `--max-turns`
	// CLI flag). Only the first option is honoured; callers that pass none keep
	// the adapter defaults.
	var msgOpt ws.ChatMessageOption
	if len(opts) > 0 {
		msgOpt = opts[0]
	}

	// Resolve chat BEFORE persisting user message so we can fail-fast on
	// config errors (e.g. unprovisioned devcontainer) without polluting
	// conversation history.
	b.logger.Debug("resolving chat", "chat_id", chatID)
	info, err := b.resolver.ResolveChat(ctx, chatID)
	if err != nil {
		b.logger.Debug("ResolveChat failed", "error", err)
		streamFn(ws.ChatEvent{Type: "error", Content: "failed to resolve chat"})
		return fmt.Errorf("resolve chat: %w", err)
	}
	b.logger.Debug("chat resolved", "agent_id", info.AgentID, "crew_id", info.CrewID)

	// PR-D F5: refuse to start an agent whose hire is still awaiting
	// operator approval (guided autonomy lands the row with
	// status='PENDING_REVIEW'). The agent_config resolver surfaces the
	// status; if it's the pending-review sentinel we short-circuit
	// before any container provisioning side-effect runs. The operator
	// must POST /api/v1/agents/{id}/approve-hire to flip the row to
	// IDLE, after which the next message proceeds normally.
	if info.AgentStatus == AgentStatusPendingReview {
		msg := "Agent hire is awaiting operator approval — once approved on the inbox, send your message again."
		streamFn(ws.ChatEvent{
			Type:    "error",
			Content: msg,
			Metadata: map[string]any{
				"reason":   "pending_review",
				"agent_id": info.AgentID,
			},
		})
		return fmt.Errorf("agent %s pending review: hire not approved", info.AgentID)
	}

	// What the client attached TO this message rather than inside it. Today
	// that is the ask-form submission envelope: which form was answered, at
	// what version, with what values, and which upload answered which field.
	//
	// It is REBUILT rather than copied. msgOpt.Metadata is a raw map straight
	// off an untrusted socket, and storing it verbatim would let any client
	// write arbitrary structure into a durable conversation record. Reading it
	// back through askforms.EnvelopeFromMetadata means exactly one shape can
	// survive the hop — a valid envelope — and anything else (junk, a partial
	// envelope, a metadata map with no envelope in it) leaves the message
	// exactly as it would have been persisted before any of this existed.
	//
	// Nothing here touches Content. A form submission is an ordinary user
	// message; strip the metadata and the conversation reads identically,
	// which is what lets every CLI adapter stay unaware of forms.
	var userMsgMetadata any
	if env, ok := askforms.EnvelopeFromMetadata(msgOpt.Metadata); ok {
		userMsgMetadata = map[string]any{askforms.EnvelopeMetadataKey: env}
	}

	// The human turn is recorded and fanned out to the other participants
	// regardless of whether the agent will respond. streamFn's BroadcastExcept
	// skips the sender (who already rendered it optimistically); harmless in a
	// private 1:1 chat where there are no other subscribers.
	persistUserMsg := func() error {
		return b.convStore.Append(ctx, chatID, conversation.Message{
			ID:           generateMsgID(),
			AgentID:      info.AgentID,
			Role:         conversation.RoleUser,
			Content:      content,
			Metadata:     userMsgMetadata,
			AuthorUserID: userID,
			Timestamp:    time.Now().UTC(),
		})
	}
	broadcastUserMsg := func() {
		streamFn(ws.ChatEvent{
			Type:     "user_message",
			Content:  content,
			Metadata: map[string]interface{}{"author_user_id": userID},
		})
	}

	// Group-chat turn-taking gate — evaluated BEFORE any container/provisioning
	// side-effect, so a line that doesn't @mention the agent never kicks off an
	// image build or container start. In a group chat the agent stays silent
	// unless @mentioned; the human line is still persisted + broadcast + counted
	// so the shared transcript records it, and a clean "done" settles the
	// sender's UI. Private (1:1) chats always respond.
	if !ShouldAgentRespond(info.Visibility, content, info.AgentSlug) {
		if err := persistUserMsg(); err != nil {
			b.logger.Error("failed to persist user message", "error", err)
			streamFn(ws.ChatEvent{Type: "error", Content: "failed to save message"})
			return fmt.Errorf("persist user message: %w", err)
		}
		broadcastUserMsg()
		if err := b.resolver.IncrementMessageCount(ctx, chatID, 1); err != nil {
			b.logger.Warn("increment message count (group, no mention)", "error", err)
		}
		// no_reply marks a done that INTENTIONALLY carries no assistant
		// turn (the agent wasn't @mentioned), so the frontend's
		// "done arrived but nothing replied → show the no-output error"
		// fallback doesn't misfire on the sender.
		streamFn(ws.ChatEvent{Type: "done", Content: "", Metadata: map[string]any{"no_reply": true}})
		return nil
	}

	containerKey := info.CrewID

	// If the crew has a devcontainer config that actually needs provisioning
	// (features / postCreateCommand / mise) but no cached image has been
	// built, auto-trigger the build instead of erroring out — the GUI has
	// no terminal in front of the user to run `crewship crew provision …`,
	// and the toolbar progress popover (plus the chat-side build card the
	// frontend renders off this event) lets the user watch the build land.
	// Configs that are no-ops at provision time (e.g. only containerEnv)
	// launch directly from runtime_image.
	//
	// Re-provision in TWO cases: (a) no cached image has ever been built
	// (CachedImage == ""), or (b) a cached image was recorded but is now
	// missing from the local Docker daemon (pruned). Case (b) is the durable
	// fix for the dead crewship-cache:* tag: that tag exists in no registry,
	// so without rebuilding it the run path would ImagePull it and fail with
	// "pull access denied", leaving the crew permanently broken.
	needsProvision := info.DevcontainerConfig != "" && devcontainerNeedsProvision(info.DevcontainerConfig, info.MiseConfig)
	cachedImageMissing := false
	if needsProvision && info.CachedImage != "" {
		if checker, ok := b.container.(imagePresenceChecker); ok {
			present, err := checker.ImagePresentLocally(ctx, info.CachedImage)
			if err != nil {
				// Couldn't determine presence (transport error / wedged
				// daemon). Assume present and let the normal run path proceed
				// rather than triggering a spurious rebuild on every message.
				b.logger.Warn("could not check cached image presence; assuming present",
					"crew_slug", info.CrewSlug, "image", info.CachedImage, "error", err)
			} else if !present {
				cachedImageMissing = true
			}
		}
	}
	if needsProvision && (info.CachedImage == "" || cachedImageMissing) {
		if cachedImageMissing {
			b.logger.Info("cached image missing locally; re-provisioning",
				"crew_slug", info.CrewSlug, "crew_id", info.CrewID, "cached_image", info.CachedImage)
		} else {
			b.logger.Info("agent start auto-triggering devcontainer build",
				"crew_slug", info.CrewSlug, "crew_id", info.CrewID)
		}
		var (
			status     string
			enqErr     error
			alreadyJob bool
		)
		if b.provisioning != nil {
			res, e := b.provisioning.EnqueueForCrew(ctx, info.CrewID, info.WorkspaceID)
			enqErr = e
			if enqErr != nil {
				b.logger.Warn("auto-provision enqueue failed",
					"crew_slug", info.CrewSlug, "error", enqErr)
				status = "failed"
			} else if res.AlreadyRunning {
				status = "running"
				alreadyJob = true
			} else if res.Started {
				status = "pending"
			}
		} else {
			// No provisioner wired (e.g. server started without Docker).
			// Fall back to the original "run the CLI" hint so the user has
			// something to act on.
			msg := fmt.Sprintf("Crew %q has devcontainer configuration but no provisioned image. Run `crewship crew provision %s`.", info.CrewSlug, info.CrewSlug)
			streamFn(ws.ChatEvent{Type: "error", Content: msg})
			return fmt.Errorf("%s", msg)
		}

		// Emit a structured event the chat surface renders as a build card.
		// On enqueue failure the event MUST carry status="failed" + error so
		// the UI can render a real error state instead of an indefinite
		// spinner — the WS hub will never emit provision.* events for a job
		// that never started.
		evtMeta := map[string]any{
			"crew_id":   info.CrewID,
			"crew_slug": info.CrewSlug,
			"status":    status,
		}
		var evtContent string
		if enqErr != nil {
			evtMeta["error"] = enqErr.Error()
			evtContent = fmt.Sprintf("Could not start build for %s: %s", info.CrewSlug, enqErr.Error())
		} else {
			// Actionable, not alarming: this is the first time (or the first
			// time since its cached image was pruned) that %s needs a build
			// before it can run at all — say what's happening, rather than
			// leaving the user to guess from a bare "building…" line why
			// nothing happened. Does NOT ask the user to resend: the message
			// is attached to the job below and the server runs it
			// automatically once the build finishes (or reports a real error
			// if it doesn't) — see AttachPendingMessage.
			evtContent = fmt.Sprintf("%s's environment is being built — this is a one-time setup step. Your message will run automatically once the build finishes.", info.CrewSlug)
		}
		streamFn(ws.ChatEvent{
			Type:     "crew_provisioning",
			Content:  evtContent,
			Metadata: evtMeta,
		})

		// Tell the caller the message did NOT actually run. When enqueue
		// failed, propagate the underlying error so callers/log handlers
		// can distinguish "build kicked off, retry later" from "build
		// never started, you need to act on this". `errors.Is` against
		// api.ErrRateLimited / ErrProvisionerUnavailable still works
		// because the API wraps with %w.
		if enqErr != nil {
			return fmt.Errorf("auto-provision enqueue failed for crew %q: %w", info.CrewSlug, enqErr)
		}
		_ = alreadyJob

		// Attach the deferred message to the job so the SERVER resumes it —
		// exactly once — when the build finishes, instead of relying on a
		// client to notice completion (the workspace realtime broadcast a
		// client might miss entirely, see docs/prd/conversational-onboarding.md)
		// or on the user to remember to resend. AttachPendingMessage's
		// at-most-once contract (keyed by chat id, drained atomically with the
		// job's terminal-state transition) means a second deferred send on the
		// same chat — e.g. an impatient manual resend while the build is still
		// running — coalesces onto this one rather than queuing a duplicate
		// run. A false return (no job tracked for this crew at all) is not
		// fatal: the crew_provisioning event above already told the user what
		// is happening, and it's only reachable if the job vanished between
		// the enqueue call above and here.
		if b.provisioning != nil {
			if !b.provisioning.AttachPendingMessage(info.CrewID, PendingChatMessage{
				UserID:  userID,
				ChatID:  chatID,
				Content: content,
				Opts:    msgOpt,
			}) {
				b.logger.Warn("could not attach deferred message to provisioning job",
					"crew_id", info.CrewID, "chat_id", chatID)
			}
		}

		// NOT a failure — the build just started (or was already running) and
		// the crew_provisioning event above already told the user what's
		// happening; the message itself is now attached to the job (above)
		// and will run automatically. This return exists only to stop this
		// function from falling through to persist/broadcast the message and
		// run the agent a second time; wrapping ws.ErrCrewProvisioning (rather
		// than a bare fmt.Errorf) lets the ws layer recognize this specific
		// control-flow case and route it away from the generic error path —
		// see ErrCrewProvisioning's doc comment for the governing rule.
		return fmt.Errorf("crew %q provisioning kicked off: %w", info.CrewSlug, ws.ErrCrewProvisioning)
	}

	// Cross-sender exclusivity: at most one live agent run per chat, no
	// matter which user's message triggered it. Two different users
	// messaging the same group chat concurrently must never race two
	// RunAgent execs into the same agent container/tmux session —
	// interleaved stdout and corrupted tmux state. tryMarkRunStart shares
	// the SAME per-chat counter Steer already uses (single source of
	// truth): it atomically claims the run slot only if the chat is
	// currently idle. The claim covers the REST of this call (persist +
	// container setup + RunAgent), released via defer on every exit path
	// (success, error, cancel).
	//
	// The claim MUST come before the user message is persisted/broadcast: a
	// bounced send has to leave no trace. Persisting it would write a turn
	// the agent never processes, and the busy notice invites a resend that
	// would then duplicate the message in the transcript. Likewise the
	// rejection must not emit ANY frame through streamFn — in production
	// streamFn fans out on the shared session channel, so an agent_busy or
	// terminal done here would reach every subscriber and make the WINNING
	// user's client finalize its live streaming turn mid-generation. We
	// return ws.ErrAgentBusy instead and the ws layer (handleSendMessage)
	// replies to the rejected sender alone.
	//
	// The ws layer's cancelKey guard (handleSendMessage) already rejects a
	// SAME user double-sending concurrently, so by the time we get here the
	// only remaining race is a DIFFERENT user hitting the same chat.
	if !b.tryMarkRunStart(chatID) {
		b.logger.Info("rejecting send: agent already running for chat", "chat_id", chatID, "user_id", userID)
		return fmt.Errorf("chat %s: %w", chatID, ws.ErrAgentBusy)
	}
	defer b.markRunEnd(chatID)

	// Cross-surface exclusivity: at most one live RunAgent exec per AGENT,
	// not just per chat. The check above stops two sends racing in THIS
	// chat, but the same agent can also be started by an assignment or an
	// @mention dispatch (api.AssignmentHandler.runAssignment) under an
	// entirely different chat/mission id — see AgentRunLock's doc comment.
	// Claimed after the per-chat check (so a same-chat double-send still
	// bounces off the cheaper, existing map first) and released immediately
	// on loss so this call leaves no claim behind; the per-chat claim above
	// is released too (via the defer already registered) so a bounce here
	// leaves the SAME no-trace guarantee bridge.go documents for the
	// per-chat case.
	if !b.agentRunLock.TryStart(info.AgentID) {
		b.logger.Info("rejecting send: agent already running (cross-surface)",
			"chat_id", chatID, "agent_id", info.AgentID, "user_id", userID)
		return fmt.Errorf("agent %s: %w: %w", info.AgentID, ErrAgentBusyElsewhere, ws.ErrAgentBusy)
	}
	// The pump runs AFTER End releases the lock (not before — a pump while
	// still holding the claim would just see this agent as busy and requeue
	// right back), and on a FRESH context: this deferred call outlives the
	// request ctx HandleChatMessage runs under, same reasoning as
	// pumpAndDispatch's own context.Background() (internal/api). Nil
	// pumper (not wired, or a test) is a no-op — see assignmentPumper's
	// field doc.
	defer func() {
		b.agentRunLock.End(info.AgentID)
		if b.assignmentPumper != nil {
			b.assignmentPumper.PumpForAgent(context.Background(), info.AgentID)
		}
	}()

	// Agent IS responding (private chat, or @mentioned in a group) and this
	// send owns the run slot. Persist + broadcast the human turn now that
	// provisioning has cleared.
	if err := persistUserMsg(); err != nil {
		b.logger.Error("failed to persist user message", "error", err)
		streamFn(ws.ChatEvent{Type: "error", Content: "failed to save message"})
		return fmt.Errorf("persist user message: %w", err)
	}
	broadcastUserMsg()

	// Auto-title: use first user message (truncated) as session title
	title := content
	if len([]rune(title)) > 60 {
		title = string([]rune(title)[:57]) + "..."
	}
	if err := b.resolver.UpdateChatTitle(ctx, chatID, title); err != nil {
		b.logger.Debug("auto-title failed (non-fatal)", "error", err)
	}

	// Look up cached container ID for this crew (avoids status noise on repeat messages)
	b.containerMu.RLock()
	containerID := b.containerCache[containerKey]
	b.containerMu.RUnlock()
	b.logger.Debug("container cache lookup", "crew_id", containerKey, "cached_id", containerID)

	// Verify cached container still exists and is running.
	// A stopped container (e.g. after network policy change) must be recreated.
	if containerID != "" && b.container != nil {
		status, err := b.container.ContainerStatus(ctx, containerID)
		if err != nil || (status != nil && status.State != "running" && status.State != "idle") {
			reason := "gone"
			if status != nil {
				reason = status.State
			}
			b.logger.Warn("cached container not usable, will recreate",
				"container_id", truncateID(containerID, 12), "state", reason)
			containerID = ""
			b.containerMu.Lock()
			delete(b.containerCache, containerKey)
			b.containerMu.Unlock()
		}
	}

	coldStart := containerID == ""

	memoryMB := info.MemoryMB
	if memoryMB <= 0 {
		memoryMB = b.cfg.DefaultMemoryMB
	}
	cpuVal := info.CPUs
	if cpuVal <= 0 {
		cpuVal = b.cfg.DefaultCPUs
	}

	if containerID == "" && b.container != nil {
		b.logger.Info("creating container", "crew_slug", info.CrewSlug)
		streamFn(ws.ChatEvent{Type: "status", Content: "Starting container..."})
		// One assembly, shared with the scheduler, the webhook handler and the
		// pipeline's agent step (crew_config.go). Sidecar services declared in
		// the crew's services_json are part of it, with env_refs resolved; the
		// provider starts them on the agent's network before the crew is
		// reported ready, so the agent's first DB call hits a live endpoint.
		cc, cfgErr := info.CrewRuntimeConfig(b.cfg.DefaultMemoryMB, b.cfg.DefaultCPUs)
		if cfgErr != nil {
			// services_json was validated on write, but a future schema bump or
			// DB tamper could still produce a body we can't decode. Surface as a
			// status, not a hard failure — the agent can still run, just without
			// its sidecars.
			b.logger.Warn("decode services_json", "crew_slug", info.CrewSlug, "error", cfgErr)
			streamFn(ws.ChatEvent{Type: "status", Content: "Sidecar services skipped (config invalid)"})
		}

		// Ask the provider what it will drop for this crew and say so, before
		// the crew starts believing otherwise (#1648). This generalises the
		// SidecarProvider check below, which did the same thing for exactly
		// one field. The REFUSED half needs no handling here: the provider
		// enforces it itself and it arrives as the error from
		// EnsureCrewRuntime, so a caller that forgets this block still cannot
		// start a crew whose containment control is unenforceable.
		support := provider.InspectCrewConfigSupport(b.container, cc)
		var reportedServices bool
		for _, msg := range support.DegradedMessages() {
			streamFn(ws.ChatEvent{Type: "status", Content: "Not honoured by this container provider — " + msg})
		}
		if len(support.Degraded) > 0 {
			b.logger.Warn("container provider drops crew config fields",
				"crew_slug", info.CrewSlug, "fields", support.Fields())
			for _, f := range support.Degraded {
				if f.Field == "Services" {
					reportedServices = true
				}
			}
		}

		// Attached AFTER the capability report on purpose. Every other field
		// that report covers is something the crew asked for and will not get;
		// this one is server plumbing the crew never named, so listing it as
		// "not honoured by this container provider" would put a line about our
		// own diagnostics in front of the user on every cold start. Both
		// providers deliver the one event the sink consumes.
		//
		// Its job: a start held for host capacity says so on the stream the
		// person waiting is already watching, instead of leaving
		// "Starting container..." on screen for up to thirty minutes (#1675).
		cc.ProvisionSink = capacityHoldSink(streamFn)

		// The runtime container and the crew's sidecars come up as one step:
		// the sidecars start after the runtime (the crew bridge network has to
		// exist first) and before the crew is reported ready.
		cID, err := b.crewStarter().StartNotify(ctx, cc, func(n crewstart.Notice) {
			// Only when the provider's own capability report didn't already
			// name Services — telling the user the same thing twice reads as
			// two separate faults.
			if n.Kind == crewstart.NoticeSidecarsUnsupported && reportedServices {
				return
			}
			streamFn(ws.ChatEvent{Type: "status", Content: n.Message})
		})
		if err != nil {
			if errors.Is(err, crewstart.ErrSidecarStart) {
				streamFn(ws.ChatEvent{Type: "error", Content: "failed to start sidecar services: " + err.Error()})
				return fmt.Errorf("ensure crew services: %w", err)
			}
			// Classify the cause into a closed set so the user gets an
			// actionable message + machine-readable code instead of the opaque
			// "failed to start agent container". The raw error still flows to
			// logs / the run record via the wrapped return below.
			code, msg := classifyCrewRuntimeError(err)
			b.logger.Warn("ensure crew runtime failed", "crew_slug", info.CrewSlug, "code", code, "error", err)
			streamFn(ws.ChatEvent{Type: "error", Content: msg, Metadata: map[string]any{"code": code}})
			return fmt.Errorf("ensure team runtime: %w", err)
		}
		containerID = cID
		b.containerMu.Lock()
		b.containerCache[containerKey] = containerID
		b.containerMu.Unlock()
		// Hand the container to the stats collector so Crow's Nest's Resources
		// panel actually fills (without this, chat-driven runs — the main
		// path — would never produce container.metrics journal entries).
		b.orch.RegisterStatsContainer(containerID, info.CrewID, info.WorkspaceID)
		streamFn(ws.ChatEvent{Type: "status", Content: "Container ready"})
		b.logger.Info("team container ensured", "crew_id", info.CrewID, "container_id", truncateID(containerID, 12))
	} else if containerID == "" {
		streamFn(ws.ChatEvent{Type: "error", Content: "container provider not configured"})
		return fmt.Errorf("no container provider and no container ID")
	}

	var toolSummaries []string
	// partAcc assembles the ordered, structured parts (text / thinking / tool
	// calls / tool results) of the assistant turn for faithful re-rendering on
	// reload. It works on the normalized AgentEvent stream, so it is identical
	// across CLI adapters. fullResponse/toolSummaries stay as the flattened
	// text + compact summary used for search and prompt-context recall.
	partAcc := conversation.NewPartAccumulator()

	req := info.ToAgentRunRequest(AgentRunOverrides{
		ChatID:      chatID,
		ContainerID: containerID,
		UserMessage: content,
		LLMModel:    info.LLMModel,
		TimeoutSecs: info.TimeoutSecs,
		MemoryMB:    memoryMB,
		CPUs:        cpuVal,
		MaxTurns:    msgOpt.MaxTurns,
	})
	// This path's caller (ws.Client.handleSendMessage) already records the
	// whole turn on `session:{chatID}` — container-start status events, the
	// agent's output via streamFn, and the terminal error/done pair AFTER
	// RunAgent returns. Letting the orchestrator open a second recording
	// underneath it (#1823) would publish every agent event twice and close
	// the stream with a `done` while the turn is still finishing. The
	// orchestrator-level publication exists for the paths that have no such
	// caller: scheduler, webhook, routine step, agent-start IPC.
	req.SuppressSessionStream = true

	// Only show "Starting agent..." on cold start (first message, container freshly created).
	// On subsequent messages the container is warm — no progress noise.
	if coldStart {
		streamFn(ws.ChatEvent{Type: "status", Content: "Starting agent..."})
	}

	logBuf := logcollector.NewOutputBuffer(b.logWriter, info.CrewID, info.AgentSlug)
	defer logBuf.Close()

	// The shared buffering handler owns the uniform per-event work:
	// accumulating assistant text, capturing the final "result" metadata,
	// and appending a LogEntry to logBuf. The wrapper below layers on the
	// chat-only extras (live streaming, structured part assembly, tool
	// summaries) and runs them BEFORE the base handler to preserve the
	// previous ordering.
	base, acc := orchestrator.NewBufferingHandler(orchestrator.BufferingHandlerOpts{
		LogBuf:            logBuf,
		AgentSlug:         info.AgentSlug,
		AccumulateText:    true,
		CaptureResultMeta: true,
		OnLogError: func(err error) {
			b.logger.Debug("log write error", "error", err)
		},
	})

	handler := func(event orchestrator.AgentEvent) {
		// Stamp the RESOLVED model onto the terminal result event.
		//
		// The result event carries `model_usage`, a map keyed by every model
		// the turn touched — which includes the small housekeeping model
		// Claude Code uses for its own bookkeeping, not just the one the agent
		// reasons with. It carries no scalar model of its own, so the client
		// was left picking a key out of that map, and encoding/json emits map
		// keys in sorted order: "claude-haiku-4-5-…" sorts before
		// "claude-opus-5", so the footer reliably showed the housekeeping
		// model and an operator asking "which model is this agent on?" got the
		// wrong answer from the one place in the UI that claims to say.
		//
		// The accumulator already holds the authoritative value from the
		// system/init event, which always precedes result, and
		// MergeRunAccumulator already writes that same value to the durable
		// run row — so this makes the live badge agree with the record instead
		// of disagreeing with it.
		if event.Type == "result" {
			if meta, ok := event.Metadata.(map[string]interface{}); ok {
				if resolved := acc.ResolvedModel(); resolved != "" {
					meta["model"] = resolved
				}
			}
		}
		streamFn(ws.ChatEvent{
			Type:     event.Type,
			Content:  event.Content,
			Metadata: event.Metadata,
		})
		// Assemble the structured parts for faithful re-rendering. The
		// accumulator itself decides which event types are content parts
		// (text/thinking/tool_call/tool_result/image) and ignores transport
		// events (status/system/result/error).
		partAcc.Add(event.Type, event.Content, event.Metadata)
		// Track tool calls for conversation context (compact summary, not full output).
		if event.Type == "tool_call" {
			toolSummaries = append(toolSummaries, fmt.Sprintf("[tool: %s]", event.Content))
		}
		if event.Type == "tool_result" {
			truncated := event.Content
			if len(truncated) > 200 {
				truncated = truncated[:200] + "..."
			}
			toolSummaries = append(toolSummaries, fmt.Sprintf("[result: %s]", truncated))
		}
		base(event)
	}

	runID := generateMsgID()
	runMeta := map[string]interface{}{
		"cli_adapter": info.CLIAdapter,
		"crew_id":     info.CrewID,
		"crew_slug":   info.CrewSlug,
		"agent_slug":  info.AgentSlug,
		"tags":        []string{"chat", info.CLIAdapter},
	}
	if err := b.resolver.CreateRun(ctx, runID, info.AgentID, chatID, info.WorkspaceID, "USER", runMeta); err != nil {
		b.logger.Warn("failed to create run record", "error", err)
	}
	// Put the run on the context so every journal entry emitted beneath it
	// inherits trace_id = runID. The orchestrator's JournalEntry has no
	// TraceID field of its own — it reads the id from here — so without this
	// every entry it writes during a chat run (exec.command,
	// chat.agent_response, run.session_init, sidecar.stale) landed unlinked,
	// and `crewship journal --run-id <id>` answered nothing for a chat run
	// while working fine for an assignment (assignments_run.go does the same
	// stamping). run.started/run.completed were the only linked rows because
	// the API layer sets TraceID itself.
	ctx = journal.WithRunID(ctx, runID)

	startedAt := time.Now()
	// The run slot was already claimed (tryMarkRunStart, above) before
	// container setup began — this keeps the chat marked "in flight" for
	// the whole call so a steering message arriving mid-turn (POST
	// /chats/{id}/steer) is detected and QUEUED instead of racing a second
	// Exec into the same container. Released via the defer registered there.
	runErr := b.orch.RunAgent(ctx, req, handler)

	// Bill the ledger before branching on runErr, the way the scheduler does.
	// A run that spent money and then failed spent the money: #1950 taught
	// every terminal branch to record the cost on the run record, but billing
	// stayed on the COMPLETED branch, so `crewship run get` showed the spend
	// while the paymaster ledger showed nothing — budget enforcement
	// under-counting on exactly the runs that burn tokens without producing an
	// answer (#1954). Best-effort by contract: never fails the turn.
	//
	// The model is session-init ground truth when the CLI reported one,
	// otherwise the requested one; ResultUsageForLedger returns ok=false when
	// there is no usage to bill, so a run that never reached the API bills
	// nothing.
	//
	// The write is DETACHED from ctx. ctx is the per-turn run context, and Stop
	// cancels it (ws.Client.handleCancelMessage); IPCResolver.RecordCost builds
	// its POST with http.NewRequestWithContext, so on a cancelled context the
	// request dies before it leaves the process and the spend survives only as
	// the WARN below — the very under-counting this hoist removed, moved from
	// failed runs to cancelled ones. WithoutCancel keeps the values on ctx (the
	// run id stamped above, trace/auth) and drops only the cancellation; the
	// timeout bounds the now-unparented call. Same shape the CANCELLED branch
	// below uses for its own writes, and the same one crew_sidecar_teardown.go
	// uses for post-request cleanup.
	if usage, ok := ResultUsageForLedger(info.WorkspaceID, info.CrewID, info.AgentID,
		orchestrator.EffectiveModel(acc, info.LLMModel), acc.ResultMeta()); ok {
		costCtx, costCancel := context.WithTimeout(context.WithoutCancel(ctx), ledgerWriteTimeout)
		if err := b.resolver.RecordCost(costCtx, usage); err != nil {
			b.logger.Warn("failed to record run cost usage", "run_id", runID, "error", err)
		}
		costCancel()
	}
	if runErr != nil {
		// If context was cancelled (user pressed stop), don't emit error -- the hub
		// sends a clean "done" event. Emitting error here would cause an error flash.
		if ctx.Err() == context.Canceled {
			b.logger.Info("run cancelled by user", "chat_id", chatID, "duration_ms", time.Since(startedAt).Milliseconds())
			cancelMsg := "cancelled"
			cleanCtx, cleanCancel := context.WithTimeout(context.Background(), ledgerWriteTimeout)
			defer cleanCancel()
			if err := b.resolver.UpdateRun(cleanCtx, runID, "CANCELLED", nil, &cancelMsg, terminalRunMeta(startedAt, acc, nil)); err != nil {
				b.logger.Warn("failed to update run status", "run_id", runID, "status", "CANCELLED", "error", err)
			}
			// Persist whatever the run produced before it was stopped so the
			// partial reply is never silently dropped. Gate on parts too, not
			// just text: a run cancelled after a tool call but before its text
			// delta still has tool/thinking parts worth keeping (the old
			// text-only gate discarded those, leaving the chat looking empty).
			if acc.Text() != "" || len(partAcc.Parts()) > 0 {
				repliedAt := time.Now().UTC()
				_ = b.convStore.Append(cleanCtx, chatID, conversation.Message{
					ID:        generateMsgID(),
					AgentID:   info.AgentID,
					Role:      conversation.RoleAssistant,
					Content:   acc.Text(),
					Parts:     partAcc.Parts(),
					Timestamp: repliedAt,
				})
				_ = b.resolver.IncrementMessageCount(cleanCtx, chatID, 2)
				// The partial reply is persisted — it counts as "a reply
				// landed" for the never-miss-a-reply projection too.
				b.notifyReply(cleanCtx, chatID, userID, info, acc.Text(), repliedAt)
			} else {
				_ = b.resolver.IncrementMessageCount(cleanCtx, chatID, 1)
			}
			return fmt.Errorf("run agent: %w", runErr)
		}

		errMsg := runErr.Error()
		if err := b.resolver.UpdateRun(ctx, runID, "FAILED", nil, &errMsg, terminalRunMeta(startedAt, acc, nil)); err != nil {
			b.logger.Warn("failed to update run status", "run_id", runID, "error", err)
		}
		// In-band failure: the CLI exited 0 and then reported that the turn
		// failed. Unlike a crashed exec, such a run usually DID produce output —
		// a refusal, or a partial answer before the error — and the failure
		// reason is itself the most useful thing on the screen. Persist the turn
		// as well as streaming the error, the same way the #545 zero-output
		// branch below does: live viewers get the error event, and a later
		// reload still shows what the agent said and why the run failed.
		// Without this, the (correct) FAILED status would cost the user text
		// they could previously see.
		if errors.Is(runErr, orchestrator.ErrAgentInBandFailure) {
			b.persistInBandFailureTurn(ctx, chatID, info, acc.Text(), partAcc.Parts(), errMsg)
		}
		// Classify adapter-exec failures (the CLI process itself exiting
		// non-zero inside the container) the same way container-start
		// failures already are above: a stable code, an actionable message,
		// and — new here — the container's own captured output attached as
		// metadata instead of only reaching the journal. Any runErr that
		// isn't an *orchestrator.AdapterExecError (in-band failure, MCP
		// injection, oversized prompt, …) falls through to its own
		// unmodified .Error() text with no metadata, unchanged from before.
		code, msg, meta := classifyAdapterExecError(runErr)
		if msg == "" {
			msg = runErr.Error()
		}
		if meta != nil {
			meta["code"] = code
			b.logger.Warn("agent exec failed", "chat_id", chatID, "code", code, "error", runErr)
		}
		streamFn(ws.ChatEvent{Type: "error", Content: msg, Metadata: meta})
		return fmt.Errorf("run agent: %w", runErr)
	}

	// Issue #545 — the run finished "successfully" but emitted ZERO output
	// (no text, no tool activity, no image). Causes: a safety refusal the
	// adapter swallowed, prompt-budget pressure pushing the response to 0
	// tokens, or the agent CLI exiting cleanly with no stdout. Silence here
	// is indistinguishable from a broken app, so surface it explicitly on
	// BOTH surfaces: an error event (then a terminal done) for live
	// viewers, and a persisted system/error turn so a later reload shows
	// the failure too. The run is recorded FAILED, not COMPLETED — a run
	// that answered nothing didn't complete anything.
	if acc.Text() == "" && len(partAcc.Parts()) == 0 {
		b.logger.Warn("agent run produced no output; surfacing error to chat (#545)",
			"chat_id", chatID, "run_id", runID, "agent_slug", info.AgentSlug)
		noOutputErr := "agent returned no output (#545)"
		if err := b.resolver.UpdateRun(ctx, runID, "FAILED", nil, &noOutputErr, terminalRunMeta(startedAt, acc, map[string]interface{}{
			"reason": "no_output",
		})); err != nil {
			b.logger.Warn("failed to update run status", "run_id", runID, "error", err)
		}
		if err := b.convStore.Append(ctx, chatID, conversation.Message{
			ID:        generateMsgID(),
			AgentID:   info.AgentID,
			Role:      conversation.RoleSystem,
			Content:   noOutputChatMessage,
			Parts:     []conversation.Part{{Type: "error", Content: noOutputChatMessage}},
			Timestamp: time.Now().UTC(),
		}); err != nil {
			b.logger.Error("failed to persist no-output error turn", "error", err, "chat_id", chatID)
		}
		if err := b.resolver.IncrementMessageCount(ctx, chatID, 2); err != nil {
			b.logger.Warn("failed to update message count", "chat_id", chatID, "error", err)
		}
		streamFn(ws.ChatEvent{
			Type:     "error",
			Content:  noOutputChatMessage,
			Metadata: map[string]any{"reason": "no_output"},
		})
		streamFn(ws.ChatEvent{Type: "done", Content: ""})
		return nil
	}

	exitCode := 0
	completedMeta := map[string]interface{}{
		"duration_ms": time.Since(startedAt).Milliseconds(),
	}
	// The ledger was billed right after the run (see above), so this only has
	// to stamp the record.
	orchestrator.MergeRunAccumulator(completedMeta, acc, info.LLMModel)
	if err := b.resolver.UpdateRun(ctx, runID, "COMPLETED", &exitCode, nil, completedMeta); err != nil {
		b.logger.Warn("failed to update run status", "run_id", runID, "error", err)
	}

	// Build compact tool summary for conversation context (cap at 10 entries
	// — keep the comment honest with the slice bound below to avoid future
	// edits "fixing" the wrong side).
	var toolSummary string
	if len(toolSummaries) > 10 {
		toolSummary = strings.Join(toolSummaries[:10], "\n") + fmt.Sprintf("\n...and %d more", len(toolSummaries)-10)
	} else if len(toolSummaries) > 0 {
		toolSummary = strings.Join(toolSummaries, "\n")
	}

	repliedAt := time.Now().UTC()
	// The setup agent has no write tools. Its only structured output is a
	// hidden, narrowly validated template suggestion in its final text. Put
	// that suggestion on both durable message metadata (reload/history) and
	// the terminal done event (the live chat) so the proposal card is not
	// timing-dependent. Ordinary agents can never emit this metadata: the
	// extractor is additionally pinned to the reserved setup-agent slug.
	assistantMetadata := onboardingProposalMetadata(info.AgentSlug, acc.Text())
	if err := b.convStore.Append(ctx, chatID, conversation.Message{
		ID:          generateMsgID(),
		AgentID:     info.AgentID,
		Role:        conversation.RoleAssistant,
		Content:     acc.Text(),
		Parts:       partAcc.Parts(),
		ToolSummary: toolSummary,
		Metadata:    assistantMetadata,
		Timestamp:   repliedAt,
	}); err != nil {
		b.logger.Error("failed to persist assistant message", "error", err, "chat_id", chatID)
		streamFn(ws.ChatEvent{Type: "error", Content: "failed to save response"})
		return fmt.Errorf("persist assistant message: %w", err)
	}

	// Update message count in DB (user + assistant = 2 messages)
	if err := b.resolver.IncrementMessageCount(ctx, chatID, 2); err != nil {
		b.logger.Warn("failed to update message count", "chat_id", chatID, "error", err)
	}

	// The reply is durably persisted — project it into the unified inbox
	// for chat users who aren't watching this session live (never miss a
	// reply). Runs after persist so a notification can never point at a
	// reply that failed to save, and carries the persist timestamp so a
	// racing mark-read (cursor >= repliedAt) suppresses it.
	b.notifyReply(ctx, chatID, userID, info, acc.Text(), repliedAt)

	// Stamp the active OTel trace id onto the "done" event so the
	// frontend can attach it to the assistant turn. This is what
	// powers feedback signal correlation — the user's thumb-down on
	// a turn lands in message_feedback with trace_id pointing back
	// at the routine run that produced the answer. When no telemetry
	// provider is configured the trace context is invalid and
	// ResolveTrace returns ok=false; we just omit the field in that
	// case so the frontend's optional read stays clean.
	doneMeta := map[string]any{}
	for key, value := range assistantMetadata {
		doneMeta[key] = value
	}
	if traceID, _, ok := telemetry.ResolveTrace(ctx); ok {
		doneMeta["trace_id"] = traceID
	}
	doneEvt := ws.ChatEvent{Type: "done", Content: ""}
	if len(doneMeta) > 0 {
		doneEvt.Metadata = doneMeta
	}
	streamFn(doneEvt)

	return nil
}

// persistInBandFailureTurn writes the conversation turn for a run the agent
// itself reported as failed while exiting 0. Mirrors the #545 zero-output
// branch: the run stays FAILED, but the turn is persisted so a reload shows
// what happened instead of nothing.
//
// The reason is always appended as an `error` part — that is what renders the
// failure in the transcript. Whether the turn is an assistant reply or a system
// notice depends on whether the agent actually said anything: a refusal is the
// agent speaking (assistant, with its text preserved), while a silent internal
// error is a system notice whose content is the reason itself.
//
// Best-effort by design: a persist failure is logged, never returned. The caller
// is already on its way to returning the run error, and swapping that for a
// storage error would hide the real cause.
func (b *Bridge) persistInBandFailureTurn(
	ctx context.Context,
	chatID string,
	info *ChatInfo,
	text string,
	parts []conversation.Part,
	reason string,
) {
	role := conversation.RoleAssistant
	content := text
	if text == "" && len(parts) == 0 {
		role = conversation.RoleSystem
		content = reason
	}
	// Copy before appending: `parts` is the accumulator's slice, and appending
	// into spare capacity would write through to the array it still owns. No
	// consequence today (the caller is done with it), but it is the kind of trap
	// that only shows up once someone reuses the accumulator afterwards.
	failParts := make([]conversation.Part, 0, len(parts)+1)
	failParts = append(failParts, parts...)
	failParts = append(failParts, conversation.Part{Type: "error", Content: reason})
	if err := b.convStore.Append(ctx, chatID, conversation.Message{
		ID:        generateMsgID(),
		AgentID:   info.AgentID,
		Role:      role,
		Content:   content,
		Parts:     failParts,
		Timestamp: time.Now().UTC(),
	}); err != nil {
		b.logger.Error("failed to persist in-band failure turn", "error", err, "chat_id", chatID)
		return
	}
	// user turn + failure turn
	if err := b.resolver.IncrementMessageCount(ctx, chatID, 2); err != nil {
		b.logger.Warn("failed to update message count", "chat_id", chatID, "error", err)
	}
}

// classifyCrewRuntimeError maps a raw EnsureCrewRuntime failure to a stable code
// + a safe, actionable user message. Like userFacingAssignmentError it is a
// substring classifier (the provider wraps most causes as %w strings, not typed
// sentinels), and it never leaks the raw daemon text — that goes to logs / the
// run record via the caller's wrapped return. Codes are a closed set the UI /
// agent can branch on: provider_capability, capacity, legacy_volume_conflict,
// image_missing, resource_limit, provision_failed, internal.
func classifyCrewRuntimeError(err error) (code, message string) {
	raw := ""
	if err != nil {
		raw = err.Error()
	}
	// A start held until its deadline is a CAPACITY failure, and the gate
	// already knows which host resource ran out and by how much. Checked
	// first, and structurally, because the substring rules below are exactly
	// what destroyed this reason (#1675): the gate's own wrapper reads
	// "admission control refused a container start for crew X", so the
	// provision_failed case claimed it and reported "provisioning error" —
	// sending the operator to the journal to find something the error was
	// already holding. A second substring rule would have been the same bug
	// with a different keyword, so the gate grew a typed error instead.
	var held *admission.HoldExpiredError
	if errors.As(err, &held) {
		return "capacity", capacityHoldMessage(held)
	}
	// A capability refusal is our own text, not the daemon's, and the field it
	// names is the only thing the operator can act on — so it is passed
	// through rather than reduced to "the container could not be started".
	// Checked first, and structurally, so no substring rule below can claim it.
	var refused *provider.CrewConfigRefusedError
	if errors.As(err, &refused) {
		return "provider_capability", fmt.Sprintf(
			"This crew asks for %s, which the %s container provider cannot apply, so it was not started "+
				"rather than run with that setting reported but unenforced. Change the setting, or move the "+
				"crew to a provider that supports it.",
			refused.FieldList(), refused.Provider)
	}
	lower := strings.ToLower(raw)
	const hint = " Details are in the run journal / server logs."
	switch {
	case strings.Contains(lower, "legacy") && strings.Contains(lower, "volume"):
		return "legacy_volume_conflict",
			"This crew has a legacy storage volume that must be migrated or removed before it can start." + hint
	case strings.Contains(lower, "image missing"),
		strings.Contains(lower, "reprovision"),
		strings.Contains(lower, "needs reprovisioning"):
		return "image_missing",
			"The crew's container image is missing locally — reprovision the crew, then retry." + hint
	case strings.Contains(lower, "resource limit"),
		strings.Contains(lower, "memory limit"),
		strings.Contains(lower, "cpu limit"),
		strings.Contains(lower, "insufficient memory"),
		strings.Contains(lower, "out of memory"):
		return "resource_limit",
			"The agent container was refused because of a resource limit (memory/CPU)." + hint
	case strings.Contains(lower, "container create"),
		strings.Contains(lower, "container start"),
		strings.Contains(lower, "start existing container"),
		strings.Contains(lower, "ensure image"),
		strings.Contains(lower, "ensure network"):
		return "provision_failed",
			"The agent container failed to start (provisioning error)." + hint
	default:
		return "internal", "The agent container could not be started." + hint
	}
}

// classifyAdapterExecError maps a RunAgent failure that comes from the agent
// CLI process itself — as opposed to classifyCrewRuntimeError's job, the
// container never starting — to a stable code + actionable message, and to
// metadata the chat client can render alongside it (the container's own
// diagnostic text, which used to reach nowhere the operator could see it).
//
// THE RULE THIS CODEBASE HAS BROKEN REPEATEDLY, restated here because it is
// this function's entire job: a check that could not RUN must never be
// reported as a check that FAILED, and a failure whose cause is known must
// not be reported generically. Exit 127 with a shell "No such file or
// directory" naming the CLI binary is not "the agent failed" — the agent
// never ran — and reporting it as "agent exited with code 127 — check the
// journal" sends the operator to grep a journal for a fact this function
// already had in hand: the crew's image does not have the adapter's binary.
// The same rule cuts the other way too — a non-zero exit with no captured
// output at all is NOT known to be a missing binary, and must not be
// upgraded to that specific a claim just because 127 usually means that in a
// shell; see the exit-127-without-confirming-text case below.
//
// Structural check before substring, same as classifyCrewRuntimeError:
// errors.As on the typed *orchestrator.AdapterExecError comes first, and
// only its fields (ExitCode, Output) are pattern-matched — never runErr's
// rendered .Error() string, which for every other error class this function
// might be handed (cancellation, in-band failure, MCP injection) has nothing
// to do with an adapter exec and would make substring matches coincidental
// at best.
func classifyAdapterExecError(err error) (code, message string, meta map[string]any) {
	var execErr *orchestrator.AdapterExecError
	if !errors.As(err, &execErr) {
		// Not an adapter-exec failure at all (a caller that reaches this
		// function is expected to have already routed cancellation/in-band
		// failures elsewhere) — fall back to the raw message rather than
		// guess. This branch is what keeps this function safe to call
		// defensively: it never fabricates a code for an error shape it
		// does not recognise.
		if err == nil {
			return "internal", "", nil
		}
		return "internal", err.Error(), nil
	}

	meta = map[string]any{
		"exit_code": execErr.ExitCode,
		"adapter":   execErr.Adapter,
	}
	if execErr.Binary != "" {
		meta["binary"] = execErr.Binary
	}
	if execErr.Output != "" {
		meta["container_output"] = execErr.Output
	}

	// Exit code 0 reaching this function would mean something upstream
	// mislabeled a successful run as a failure — the exact "could-not-check
	// reported as failed" bug this function exists to prevent elsewhere.
	// Refuse to invent a failure story for it.
	if execErr.ExitCode == 0 {
		return "internal", "The agent run was reported as failed but the process exited 0 — this should not happen; check the journal for what actually went wrong.", meta
	}

	lowerOut := strings.ToLower(execErr.Output)
	const hint = " Details are in the run journal / server logs."
	switch {
	case execErr.ExitCode == 127 &&
		(strings.Contains(lowerOut, "no such file or directory") || strings.Contains(lowerOut, "command not found")):
		// The shell tried to exec the adapter's binary and it was not on
		// PATH — the crew's image was built without it (or from before the
		// adapter was added). Not a crash: the agent never started.
		bin := execErr.Binary
		if bin == "" {
			bin = execErr.Adapter
		}
		return "adapter_missing", fmt.Sprintf(
			"The %q binary is not installed in this crew's container image, so the %s adapter could not run at all — "+
				"this is a missing binary, not a crash. Reprovision the crew with an image that includes %q, "+
				"or move the crew to an adapter its image supports.",
			bin, execErr.Adapter, bin), meta
	case strings.TrimSpace(execErr.Output) != "":
		// Non-zero exit with the CLI's own output captured: the process ran
		// and told us something on its way out, so pass that on instead of
		// reducing a known cause to a generic sentence.
		return "adapter_crashed", fmt.Sprintf(
			"The %s agent process exited with code %d. Container output: %s",
			execErr.Adapter, execErr.ExitCode, truncateForChatMeta(execErr.Output)) + hint, meta
	default:
		// Non-zero exit, nothing captured — genuinely unknown cause. Say so
		// rather than guess at "missing binary" or paraphrase the CLI's own
		// (absent) words.
		return "internal", fmt.Sprintf(
			"The %s agent process exited with code %d and produced no output that explains why.",
			execErr.Adapter, execErr.ExitCode) + hint, meta
	}
}

// truncateForChatMeta bounds how much container output rides in the chat
// error message body itself (the untruncated text still goes into
// meta["container_output"] for anything that wants the whole thing) — a
// crash that dumped a stack trace should not turn the chat bubble into a
// wall of text.
func truncateForChatMeta(s string) string {
	const maxLen = 500
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "…"
}

// capacityHoldMessage turns an expired capacity hold into the sentence the
// person waiting gets. It carries the gate's own figures rather than a generic
// phrase: "the host does not have enough memory" tells an operator what to do,
// "provisioning error" tells them to go and read the journal for a number the
// error already had.
func capacityHoldMessage(h *admission.HoldExpiredError) string {
	var b strings.Builder
	phrase := admission.ReasonPhrase(h.Reason)
	b.WriteString(strings.ToUpper(phrase[:1]))
	b.WriteString(phrase[1:])
	if h.Detail != "" {
		b.WriteString(" — ")
		b.WriteString(h.Detail)
	}
	fmt.Fprintf(&b, ". The start waited %s for capacity and then gave up.",
		h.Waited.Round(time.Second))
	if remedy := admission.ReasonRemedy(h.Reason); remedy != "" {
		b.WriteString(" ")
		b.WriteString(remedy)
	}
	return b.String()
}

// capacityHoldSink is the ProvisionSink the bridge hands the container
// provider. It forwards exactly one thing onto the caller's chat stream: the
// admission hold.
//
// Narrow on purpose. The rest of the provisioning pipeline already has two
// surfaces (the journal and the Activity Bar, via the API's
// RuntimeProvisionSink) and replaying it into the chat would be noise. The
// hold is different: it is the only step that can last thirty minutes, and
// until now it produced no line anywhere the person waiting was looking — the
// chat showed "Starting container..." for the whole wait, which reads exactly
// like a hang (#1675).
//
// The gate rate-limits its own notices, so this cannot become a per-poll line.
func capacityHoldSink(streamFn func(ws.ChatEvent)) func(devcontainer.ProvisionEvent) {
	if streamFn == nil {
		return nil
	}
	return func(ev devcontainer.ProvisionEvent) {
		if ev.Step != devcontainer.ProvStepCapacityHold {
			return
		}
		var b strings.Builder
		b.WriteString("Waiting for host capacity — ")
		b.WriteString(admission.ReasonPhrase(ev.Reason))
		if ev.Detail != "" {
			b.WriteString(" (")
			b.WriteString(ev.Detail)
			b.WriteString(")")
		}
		if ev.DurationMs > 0 {
			fmt.Fprintf(&b, "; waiting %s so far",
				(time.Duration(ev.DurationMs) * time.Millisecond).Round(time.Second))
		}
		streamFn(ws.ChatEvent{Type: "status", Content: b.String()})
	}
}

// crewStarter is the bridge's handle on the crew-start contract
// (internal/crewstart). Chat resolves the crew's whole config itself off the
// agent-resolve response, so it needs no completer — but it goes through the
// same Start as every other path, which is what keeps "start a crew" meaning
// one thing.
func (b *Bridge) crewStarter() *crewstart.Starter {
	return crewstart.New(b.container, nil, b.logger)
}
