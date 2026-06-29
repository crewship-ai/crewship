package chatbridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/lookout"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/telemetry"
	"github.com/crewship-ai/crewship/internal/ws"
)

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
	// GetWebhookSecret retrieves the webhook secret for an agent. crewID is
	// an OPTIONAL tenant scope sent as ?crew_id= so the server constrains
	// the lookup to the (crew, agent) pair the webhook URL named — without
	// it any caller could fetch any agent's secret across crew boundaries
	// and forge a validly-signed webhook. Empty crewID keeps the legacy
	// id-only behavior for callers that don't know the crew.
	GetWebhookSecret(ctx context.Context, crewID, agentID string) (string, error)
	CreateRun(ctx context.Context, runID, agentID, chatID, workspaceID, triggerType string, metadata map[string]interface{}) error
	UpdateRun(ctx context.Context, runID, status string, exitCode *int, errorMsg *string, metadata map[string]interface{}) error
	IncrementMessageCount(ctx context.Context, chatID string, delta int) error
	UpdateChatTitle(ctx context.Context, chatID, title string) error
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
	AgentStatus        string
	CrewID             string
	CrewSlug           string
	ContainerID        string
	CLIAdapter         string
	LLMModel           string
	SystemPrompt       string
	ToolProfile        string
	Credentials        []orchestrator.Credential
	TimeoutSecs        int
	WorkspaceID        string
	MemoryEnabled      bool
	CrewMembers        []orchestrator.CrewMember
	NetworkMode        string
	AllowedDomains     []string
	MemoryMB           int
	CPUs               float64
	TTLHours           int
	RuntimeImage       string
	CachedImage        string
	DevcontainerConfig string
	MiseConfig         string
	ServicesJSON       string
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
}

// ProvisioningEnqueueResult mirrors api.EnqueueResult shape locally so the
// chatbridge interface doesn't import the api package — api depends on
// chatbridge (ChatHandler), which would create a cycle.
type ProvisioningEnqueueResult struct {
	Started        bool
	AlreadyRunning bool
	Status         string
}

// ProvisioningEnqueuer kicks off an asynchronous provisioning job for a crew
// whose devcontainer image hasn't been built yet. Wired in by the server so
// the bridge can auto-trigger a build when a user's first message lands on
// an unprovisioned crew, instead of erroring with "run `crewship crew
// provision …` first" — the GUI has no terminal context for that hint.
type ProvisioningEnqueuer interface {
	EnqueueForCrew(ctx context.Context, crewID, workspaceID string) (ProvisioningEnqueueResult, error)
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

	// steerBroadcaster announces steering_queued events on the chat's
	// session channel. Optional: nil means the WS announcement is
	// skipped (the durable persist is the contract; the event is a UI
	// nicety). Wired post-construction via SetSteerBroadcaster because
	// the ws.Hub is built in the server boot sequence, same as the
	// ProvisioningEnqueuer.
	steerBroadcaster SteerBroadcaster
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
	}
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

// HandleChatMessage processes an incoming chat message by resolving the session,
// ensuring the container is running, persisting the message, and streaming the
// agent's response back to the client.
func (b *Bridge) HandleChatMessage(ctx context.Context, userID, chatID, content string, streamFn func(ws.ChatEvent)) error {
	b.logger.Debug("HandleChatMessage", "chat_id", chatID, "content_len", len(content))

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
		streamFn(ws.ChatEvent{Type: "done", Content: ""})
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
			evtContent = fmt.Sprintf("Building %s — your message will run once the image is ready.", info.CrewSlug)
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
		return fmt.Errorf("crew %q provisioning kicked off; resend after build completes", info.CrewSlug)
	}

	// Agent IS responding (private chat, or @mentioned in a group). Persist +
	// broadcast the human turn now that provisioning has cleared.
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
		// Merge feature-level ContainerEnv (from CachedRequirements) with
		// root-level ContainerEnv. Root wins on conflict so user intent in
		// devcontainer.json overrides feature defaults.
		mergedEnv := make(map[string]string)
		if info.CachedRequirements != nil {
			for k, v := range info.CachedRequirements.ContainerEnv {
				mergedEnv[k] = v
			}
		}
		for k, v := range info.ContainerEnv {
			mergedEnv[k] = v
		}
		cc := provider.CrewConfig{
			ID:             info.CrewID,
			Slug:           info.CrewSlug,
			MemoryMB:       memoryMB,
			CPUs:           cpuVal,
			Image:          info.RuntimeImage,
			CachedImage:    info.CachedImage,
			NetworkMode:    info.NetworkMode,
			AllowedDomains: info.AllowedDomains,
			TTLHours:       info.TTLHours,
			ContainerEnv:   mergedEnv,
		}
		if info.CachedRequirements != nil {
			cc.Privileged = info.CachedRequirements.Privileged
			cc.Init = info.CachedRequirements.Init
			cc.CapAdd = append(cc.CapAdd, info.CachedRequirements.CapAdd...)
			cc.SecurityOpt = append(cc.SecurityOpt, info.CachedRequirements.SecurityOpt...)
			for _, m := range info.CachedRequirements.Mounts {
				// Expand devcontainer.json variables (e.g. ${devcontainerId})
				// before passing the source/target to Docker — Docker rejects
				// volume names containing "$" with a cryptic error otherwise.
				cc.ExtraMounts = append(cc.ExtraMounts, provider.CrewMount{
					Source: devcontainer.ExpandVars(m.Source, info.CrewID),
					Target: devcontainer.ExpandVars(m.Target, info.CrewID),
					Type:   m.Type,
				})
			}
			cc.PostStartCommands = append(cc.PostStartCommands, info.CachedRequirements.PostStartCommands...)
		}
		// Root-level postStartCommand runs after feature hooks so user intent
		// (e.g. "start my app-specific DB") wins over feature defaults.
		cc.PostStartCommands = append(cc.PostStartCommands, info.RootPostStart...)

		// Sidecar services declared in the crew's services_json get
		// translated into provider.CrewService entries with env_refs
		// resolved against the workspace credential vault. The
		// docker provider starts them on the same network as the
		// agent before EnsureCrewRuntime returns so the agent's
		// first DB call hits a ready endpoint.
		if info.ServicesJSON != "" {
			svcs, err := decodeServicesForRuntime(info.ServicesJSON, info.ServiceEnvLookup)
			if err != nil {
				// services_json was validated on write, but a future
				// schema bump or DB tamper could still produce a
				// body we can't decode. Surface as a status, not a
				// hard failure — the agent can still run, just
				// without its sidecars.
				b.logger.Warn("decode services_json", "crew_slug", info.CrewSlug, "error", err)
				streamFn(ws.ChatEvent{Type: "status", Content: "Sidecar services skipped (config invalid)"})
			} else {
				cc.Services = svcs
			}
		}

		cID, err := b.container.EnsureCrewRuntime(ctx, cc)
		if err != nil {
			// Surface the real cause, not a bare generic string. A swallowed
			// cause (e.g. the legacy-C1-resource guard, which names the exact
			// orphaned volume and the remediation) leaves operators with
			// nothing to act on — the symptom the user hit. Redact first so a
			// secret embedded in a wrapped error never reaches the client.
			safeCause, _ := lookout.Redact(err.Error())
			streamFn(ws.ChatEvent{Type: "error", Content: "failed to start agent container: " + safeCause})
			return fmt.Errorf("ensure team runtime: %w", err)
		}
		// Start sidecars after the agent runtime is ready so the
		// crew bridge network exists. Providers that don't
		// implement SidecarProvider silently skip — log a
		// one-time warning so the operator knows their services:
		// declarations are dormant.
		if len(cc.Services) > 0 {
			if sp, ok := b.container.(provider.SidecarProvider); ok {
				ids, err := sp.EnsureCrewServices(ctx, cc)
				if err != nil {
					streamFn(ws.ChatEvent{Type: "error", Content: "failed to start sidecar services: " + err.Error()})
					return fmt.Errorf("ensure crew services: %w", err)
				}
				b.logger.Info("sidecar services ready", "crew_slug", info.CrewSlug, "count", len(ids))
			} else {
				b.logger.Warn("container provider does not support sidecars; services skipped",
					"crew_slug", info.CrewSlug, "service_count", len(cc.Services))
				streamFn(ws.ChatEvent{Type: "status",
					Content: "Sidecar services declared but provider doesn't support them yet"})
			}
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
	})

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

	startedAt := time.Now()
	// Mark this chat as having a live run so a steering message arriving
	// mid-turn (POST /chats/{id}/steer) is detected and QUEUED instead of
	// racing a second Exec into the same container. Balanced by markRunEnd.
	b.markRunStart(chatID)
	defer b.markRunEnd(chatID)
	runErr := b.orch.RunAgent(ctx, req, handler)
	if runErr != nil {
		// If context was cancelled (user pressed stop), don't emit error -- the hub
		// sends a clean "done" event. Emitting error here would cause an error flash.
		if ctx.Err() == context.Canceled {
			b.logger.Info("run cancelled by user", "chat_id", chatID, "duration_ms", time.Since(startedAt).Milliseconds())
			cancelMsg := "cancelled"
			cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cleanCancel()
			if err := b.resolver.UpdateRun(cleanCtx, runID, "CANCELLED", nil, &cancelMsg, map[string]interface{}{
				"duration_ms": time.Since(startedAt).Milliseconds(),
			}); err != nil {
				b.logger.Warn("failed to update run status", "run_id", runID, "status", "CANCELLED", "error", err)
			}
			// Persist partial response if any
			if acc.Text() != "" {
				_ = b.convStore.Append(cleanCtx, chatID, conversation.Message{
					ID:        generateMsgID(),
					AgentID:   info.AgentID,
					Role:      conversation.RoleAssistant,
					Content:   acc.Text(),
					Parts:     partAcc.Parts(),
					Timestamp: time.Now().UTC(),
				})
				_ = b.resolver.IncrementMessageCount(cleanCtx, chatID, 2)
			} else {
				_ = b.resolver.IncrementMessageCount(cleanCtx, chatID, 1)
			}
			return fmt.Errorf("run agent: %w", runErr)
		}

		errMsg := runErr.Error()
		if err := b.resolver.UpdateRun(ctx, runID, "FAILED", nil, &errMsg, map[string]interface{}{
			"duration_ms": time.Since(startedAt).Milliseconds(),
		}); err != nil {
			b.logger.Warn("failed to update run status", "run_id", runID, "error", err)
		}
		streamFn(ws.ChatEvent{Type: "error", Content: runErr.Error()})
		return fmt.Errorf("run agent: %w", runErr)
	}

	exitCode := 0
	completedMeta := map[string]interface{}{
		"duration_ms": time.Since(startedAt).Milliseconds(),
	}
	orchestrator.MergeResultUsageMeta(completedMeta, acc.ResultMeta())
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

	if err := b.convStore.Append(ctx, chatID, conversation.Message{
		ID:          generateMsgID(),
		AgentID:     info.AgentID,
		Role:        conversation.RoleAssistant,
		Content:     acc.Text(),
		Parts:       partAcc.Parts(),
		ToolSummary: toolSummary,
		Timestamp:   time.Now().UTC(),
	}); err != nil {
		b.logger.Error("failed to persist assistant message", "error", err, "chat_id", chatID)
		streamFn(ws.ChatEvent{Type: "error", Content: "failed to save response"})
		return fmt.Errorf("persist assistant message: %w", err)
	}

	// Update message count in DB (user + assistant = 2 messages)
	if err := b.resolver.IncrementMessageCount(ctx, chatID, 2); err != nil {
		b.logger.Warn("failed to update message count", "chat_id", chatID, "error", err)
	}

	// Stamp the active OTel trace id onto the "done" event so the
	// frontend can attach it to the assistant turn. This is what
	// powers feedback signal correlation — the user's thumb-down on
	// a turn lands in message_feedback with trace_id pointing back
	// at the routine run that produced the answer. When no telemetry
	// provider is configured the trace context is invalid and
	// ResolveTrace returns ok=false; we just omit the field in that
	// case so the frontend's optional read stays clean.
	doneMeta := map[string]any{}
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
