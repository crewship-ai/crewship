package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/ws"
)

// MissionCallback is notified when assignments linked to mission tasks complete.

type MissionCallback interface {
	OnAssignmentCompleted(ctx context.Context, assignmentID, status, resultSummary, errorMessage string) error
}

// AssignmentHandler handles internal assignment API requests.
// Assignments are created by the sidecar on behalf of lead agents and
// executed as sub-agent runs in the crew container.

// crewProvisioner is the dispatch-time provisioning gate. Satisfied by
// *ProvisioningHandler; an interface so tests can stub it (and so the handler
// works with provisioning disabled — nil means "skip the gate").
type crewProvisioner interface {
	EnsureProvisioned(ctx context.Context, crewID, workspaceID string, timeout time.Duration) error
	// RuntimeProvisionSink builds the sink that journals + live-streams the
	// runtime container-preparation events (start → image_resolved →
	// container_create → ready / failed) the container provider emits during
	// EnsureCrewRuntime, so the agent-run/ensure-container path is audited like
	// the explicit job runner. The ctx supplies the run identity the emitted
	// rows are stamped with; it is used for values only, never cancellation.
	RuntimeProvisionSink(ctx context.Context, crewID, workspaceID string) func(devcontainer.ProvisionEvent)
}

// agentConfigResolver resolves an agent ID to its full runtime config —
// the ASSEMBLED system prompt (skills, persona, ethos, connected
// integrations), MCP servers, installed skills, credentials, resource
// limits, and the crew-policy approval_mode. Satisfied by
// *chatbridge.IPCResolver. Injected so mission/peer dispatch funnels
// through the same builder as chat/pipeline/webhook (#810) instead of
// reading raw system_prompt_legacy with no MCP.
type agentConfigResolver interface {
	ResolveAgent(ctx context.Context, agentID, workspaceID string) (*chatbridge.ChatInfo, error)
}

type AssignmentHandler struct {
	db              *sql.DB
	orch            *orchestrator.Orchestrator
	hub             *ws.Hub
	logger          *slog.Logger
	internalToken   string
	missionCallback MissionCallback
	journal         journal.Emitter
	provisioner     crewProvisioner
	// resolver funnels dispatch through the one request-builder (#810).
	// nil → the legacy raw-SQL build (system_prompt_legacy, no MCP) so
	// tests that construct the handler without a resolver are unaffected.
	resolver agentConfigResolver

	// dispatchWG tracks the async runAssignment/dispatchByID goroutines
	// spawned by Create and pumpAndDispatch. Nothing in the request path
	// waits on it; it exists so shutdown (and tests) can drain in-flight
	// dispatches instead of leaving them racing the DB teardown.
	dispatchWG sync.WaitGroup
}

// WaitDispatches blocks until every async dispatch goroutine spawned so
// far has finished. Call during graceful shutdown after the listener
// stops accepting requests; tests use it to keep spawned dispatches from
// outliving their fixture DB.
func (h *AssignmentHandler) WaitDispatches() {
	h.dispatchWG.Wait()
}

// NewAssignmentHandler creates an AssignmentHandler with the given orchestrator, WebSocket hub, and internal token.

func NewAssignmentHandler(db *sql.DB, orch *orchestrator.Orchestrator, hub *ws.Hub, internalToken string, logger *slog.Logger) *AssignmentHandler {
	return &AssignmentHandler{
		db:            db,
		orch:          orch,
		hub:           hub,
		logger:        logger,
		internalToken: internalToken,
		journal:       noopEmitter{},
	}
}

// SetMissionCallback registers the MissionEngine to receive assignment completion events.

func (h *AssignmentHandler) SetMissionCallback(cb MissionCallback) {
	h.missionCallback = cb
}

// SetResolver wires the agent-config resolver so mission/sidecar-assign
// dispatch builds its run through the one request-builder (#810). nil
// leaves the legacy raw-SQL path in place.
func (h *AssignmentHandler) SetResolver(rz agentConfigResolver) {
	h.resolver = rz
}

// SetProvisioner wires the dispatch-time provisioning gate so a cold crew is
// built (and its container created from the provisioned image) before the
// agent runs. nil disables the gate — the run path falls back to whatever
// image is available. Wired at server boot to the ProvisioningHandler.
func (h *AssignmentHandler) SetProvisioner(p crewProvisioner) {
	h.provisioner = p
}

// SetJournal wires a journal emitter for run lifecycle events. nil maps
// to the no-op so callers don't have to branch.
func (h *AssignmentHandler) SetJournal(j journal.Emitter) {
	if j == nil {
		h.journal = noopEmitter{}
		return
	}
	h.journal = j
}

type targetAgentInfo struct {
	ID             string
	Slug           string
	Name           string
	RoleTitle      string
	SystemPrompt   string
	CLIAdapter     string
	LLMModel       string
	ToolProfile    string
	TimeoutSeconds int
	MemoryEnabled  bool
	CrewSlug       string
	// Status is agents.status at dispatch time, carried so every door can
	// apply refuseHeldAgent to the row it just read rather than re-querying
	// (and rather than forgetting). Empty means the door did not select it;
	// see refuseHeldAgent for why that stays permissive.
	Status string
}

// ── The held-agent gate ────────────────────────────────────────────────────
//
// agentHeldError is a target that EXISTS but has been staged for an operator's
// decision and must not be run. Deliberately not a *delegationRefusal: no cap
// was hit, and no instance setting would change the answer — a person has to
// approve the agent. The two are answered the same way by a mention (recorded
// as `refused`), and differently by /assign (409 conflict rather than 403).
type agentHeldError struct{ msg string }

func (e *agentHeldError) Error() string { return e.msg }

// dispatchRefused marks it as a DECISION rather than a failure, so
// issue_mentions.go's dispatchOne can record "a gate said no" without
// enumerating gate types. *delegationRefusal carries the same marker.
func (e *agentHeldError) dispatchRefused() {}

// DispatchDeferred marks it as PERMANENT UNTIL A HUMAN ACTS, which is a
// different thing from "refused" and the reason this method is exported.
//
// The mission engine (internal/orchestrator) classifies every dispatch error
// into fail-this-task or retry-next-tick, and a hold is neither: failing it
// turns the guided ephemeral-hire flow — where PENDING_REVIEW while an
// operator approves is the SUPPORTED state — into a terminally failed mission
// task that approval cannot revive, while retrying it spins at the tick rate
// forever because the answer does not change until somebody clicks approve.
// The third answer is "wait", and this marker is how a package that cannot
// import this one recognises it (orchestrator/agent_hold.go).
//
// *delegationRefusal deliberately does NOT carry it: a fan-out cap clears on
// its own as siblings finish, so retrying is exactly right there.
func (e *agentHeldError) DispatchDeferred() {}

// dispatchRefusal is the set of errors that mean "a gate declined this
// dispatch", as opposed to "the dispatch broke". A refusal is recorded and
// reported verbatim to whoever asked; a failure is logged as a bug.
type dispatchRefusal interface {
	error
	dispatchRefused()
}

// refuseHeldAgent decides whether an agents.status value means "created, but
// inert until an operator says otherwise".
//
// EXACTLY ONE status is refusable: PENDING_REVIEW. It is the sentinel both
// staging paths write — the guided ephemeral hire (agents_hire.go) and the
// #1768 autonomy gate on agent creation (internal_status.go) — and the only
// one chatbridge treats as blocking. Every other value (IDLE, RUNNING, ERROR,
// …) is a LIFECYCLE state, not a decision: refusing on RUNNING would mean an
// agent could never be given a second task, and refusing on ERROR would turn
// one failed run into a permanent brick. An unknown or empty status stays
// permissive for the same reason chatbridge's guard does — a status nobody has
// decided about must not silently become a deny.
//
// WHAT THIS COSTS THE LEGITIMATE EPHEMERAL HIRE, and how that is paid for.
// A hire lands in PENDING_REVIEW under guided autonomy, where waiting is the
// point: the CLI polls exactly this transition (`crewship hire --wait`,
// cmd_hire.go), and ApproveHire flips the row to IDLE before the hired agent
// is meant to work. A hire that lands IDLE (trusted/full) never sees this
// function say no.
//
// But the mission engine can already be holding a task list that names that
// agent, and it dispatches through DispatchAssignment. The first version of
// this gate returned an ordinary error there, which the engine recorded as a
// terminally FAILED task — so the operator's approval arrived at a mission
// that had given up minutes earlier. Reasoning that the flow was safe is what
// made that ship; it is not safe by construction, it is safe because the
// refusal is now a DEFERRAL (see DispatchDeferred) that the engine unwinds and
// retries, and because TestScheduleReadyTasks_HeldHireWaitsAndCompletesOnceApproved
// drives the whole held → approved → completed flow rather than arguing about it.
//
// Why it exists at all: internal_status.go stages an agent CREATED BY ANOTHER
// AGENT, with a system prompt that agent wrote, and documents the row as unable
// to "serve a single message until an operator approves". That was true only of
// chatbridge — /assign and the @mention trigger both start an agent through
// runAssignment, which never read agents.status. "Created but inert beats
// refused" is the load-bearing claim of the gate design; this is the half that
// makes it true.
func refuseHeldAgent(slug, status string) error {
	if status != chatbridge.AgentStatusPendingReview {
		return nil
	}
	return &agentHeldError{msg: fmt.Sprintf(
		"agent %s is PENDING_REVIEW: it was created or hired by an agent and is held until an "+
			"operator approves it, so it cannot be given work. Approve it from the inbox "+
			"(or with `crewship hire approve <agent-id>`) and ask again — do this task "+
			"yourself or report the block in the meantime.", slug)}
}

// loadAgentCredentials queries and decrypts all credentials for an agent.

func (h *AssignmentHandler) loadAgentCredentials(ctx context.Context, agentID string) ([]orchestrator.Credential, error) {
	// The set is defined once, in loadDeliveredCredentials: explicit
	// agent_credentials grants UNION credentials linked to the agent's own crew
	// via credential_crews. This loader reading agent_credentials on its own was
	// half of PRD-CREDENTIALS-V2 §1.2 — a crew-assigned credential never crossed
	// the sub-agent boundary, so a hired agent ran without the crew's secrets
	// even when its parent had them.
	//
	// The filters that were spelled out here now live in that one query:
	//
	//	status='ACTIVE' (#1051) — PENDING rows (manifest slots, OAuth
	//	  mid-handshake, rotation in progress) carry sentinel encrypted bodies,
	//	  and without the filter this loader would inject "pending_manifest" /
	//	  "pending_oauth" as a real env value at the sub-agent boundary.
	//	the #1373 lease gate — a lapsed lease handed over here is worse than at
	//	  boot: the value crosses to an agent the lease was never issued to.
	delivered, slotNotices, err := loadDeliveredCredentials(ctx, h.db, agentID)
	if err != nil {
		return nil, fmt.Errorf("query credentials: %w", err)
	}
	logDeliveredCredentialNotices(h.logger, agentID, delivered, slotNotices)

	var creds []orchestrator.Credential
	for _, d := range delivered {
		c := orchestrator.Credential{
			ID:             d.ID,
			EnvVarName:     d.EnvVar,
			Priority:       d.Priority,
			Type:           d.Type,
			Provider:       d.Provider,
			LeaseExpiresAt: d.LeaseExpiresAt,
		}
		dec, err := encryption.Decrypt(d.EncryptedValue)
		if err != nil {
			h.logger.Error("decrypt credential", "id", c.ID, "error", err)
			continue
		}
		// Defence in depth: a future code path that decrypts a row the SQL
		// filter missed must not leak a sentinel placeholder to the agent.
		if isPendingSentinel(dec) {
			continue
		}
		c.PlainValue = dec
		// A provider whose upstream lives in the credential (OPENAI_COMPAT)
		// stores {baseURL,apiKey,headers} as one object. PlainValue becomes the
		// sidecar's bearer token, so the object has to be split here too — this
		// loader carries the provider column exactly like the boot path, and
		// without the split the base URL and every custom header would be sent
		// upstream AS the secret.
		if providerNeedsEndpointValue(d.Provider) {
			token, baseURL, headers, perr := providerEndpointFromValue(d.Provider, dec)
			if perr != nil {
				h.logger.Error("endpoint credential rejected at delivery",
					"id", c.ID, "provider", d.Provider, "error", perr)
				continue
			}
			c.PlainValue, c.BaseURL, c.Headers = token, baseURL, headers
		}
		// The credential's parts (PRD §2.2), opened with the same helper the
		// value was. A failure drops the whole credential — a sub-agent handed
		// an AWS key with no secret fails at the point of use, blaming the
		// wrong thing.
		fields, err := decryptDeliveredFields(d, encryption.Decrypt)
		if err != nil {
			h.logger.Error("decrypt credential field", "id", c.ID, "error", err)
			continue
		}
		for _, f := range fields {
			c.Fields = append(c.Fields, orchestrator.CredentialField{
				EnvVar: f.EnvVar, Value: f.Value, IsSecret: f.IsSecret,
			})
		}
		creds = append(creds, c)
	}
	return creds, nil
}

// runAssignment executes the sub-agent for an assignment in a goroutine.

func (h *AssignmentHandler) List(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("crewId")
	workspaceID := WorkspaceIDFromContext(r.Context())

	limit, offset := parsePagination(r, 50, 100)

	type assignmentListItem struct {
		ID             string  `json:"id"`
		Task           string  `json:"task"`
		Status         string  `json:"status"`
		AssignedByName string  `json:"assigned_by_name"`
		AssignedBySlug string  `json:"assigned_by_slug"`
		AssignedToName string  `json:"assigned_to_name"`
		AssignedToSlug string  `json:"assigned_to_slug"`
		ResultSummary  *string `json:"result_summary"`
		ErrorMessage   *string `json:"error_message"`
		StartedAt      *string `json:"started_at"`
		FinishedAt     *string `json:"finished_at"`
		CreatedAt      string  `json:"created_at"`
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT a.id, a.task, a.status, a.result_summary, a.error_message,
		       a.started_at, a.finished_at, a.created_at,
		       by_agent.name, by_agent.slug,
		       to_agent.name, to_agent.slug
		FROM assignments a
		JOIN agents by_agent ON by_agent.id = a.assigned_by_id
		JOIN agents to_agent ON to_agent.id = a.assigned_to_id
		WHERE (by_agent.crew_id = ? OR to_agent.crew_id = ?)
		  AND a.workspace_id = ?
		ORDER BY a.created_at DESC
		LIMIT ? OFFSET ?
	`, crewID, crewID, workspaceID, limit, offset)
	if err != nil {
		replyInternalError(w, h.logger, "list assignments", err)
		return
	}
	defer rows.Close()

	items := make([]assignmentListItem, 0, capacityHint(limit))
	for rows.Next() {
		var item assignmentListItem
		if err := rows.Scan(
			&item.ID, &item.Task, &item.Status, &item.ResultSummary, &item.ErrorMessage,
			&item.StartedAt, &item.FinishedAt, &item.CreatedAt,
			&item.AssignedByName, &item.AssignedBySlug,
			&item.AssignedToName, &item.AssignedToSlug,
		); err != nil {
			replyInternalError(w, h.logger, "scan assignment", err)
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		replyInternalError(w, h.logger, "rows iteration", err)
		return
	}

	writeJSON(w, http.StatusOK, items)
}

// Get handles GET /api/v1/internal/assignments/{assignmentId}.
// Called by the sidecar when a lead agent polls for assignment results.

func (h *AssignmentHandler) Get(w http.ResponseWriter, r *http.Request) {
	assignmentID := r.PathValue("assignmentId")

	// Workspace scope (#1040). This internal route is reached from the sidecar
	// with a workspace-bound token, but requireInternal only scopes handlers
	// that read ?workspace_id — this one never did, so an agent could pass any
	// assignment id and read its full row (task brief, result_summary,
	// error_message) across workspaces: a cross-workspace IDOR. The sidecar now
	// forwards its bound workspace_id; require it and scope the SELECT. Matches
	// the InternalMissionHandler.Get pattern.
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		replyError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}
	// Defense-in-depth, matching InternalMissionHandler.Get/Create: a
	// workspace-bound internal token may only read its own workspace, so the
	// query-param workspace_id must agree with the token's binding. A master
	// (host-side) token has an empty binding and passes unchanged.
	if !assertInternalTokenWorkspace(w, r, wsID) {
		return
	}

	type assignmentResult struct {
		ID            string  `json:"id"`
		WorkspaceID   string  `json:"workspace_id"`
		ChatID        string  `json:"chat_id"`
		AssignedByID  string  `json:"assigned_by_id"`
		AssignedToID  string  `json:"assigned_to_id"`
		Task          string  `json:"task"`
		Status        string  `json:"status"`
		StartedAt     *string `json:"started_at"`
		FinishedAt    *string `json:"finished_at"`
		ResultSummary *string `json:"result_summary"`
		ErrorMessage  *string `json:"error_message"`
		CreatedAt     string  `json:"created_at"`
	}

	var a assignmentResult
	err := h.db.QueryRowContext(r.Context(), `
		SELECT id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status,
		       started_at, finished_at, result_summary, error_message, created_at
		FROM assignments WHERE id = ? AND workspace_id = ?
	`, assignmentID, wsID).Scan(
		&a.ID, &a.WorkspaceID, &a.ChatID, &a.AssignedByID, &a.AssignedToID,
		&a.Task, &a.Status, &a.StartedAt, &a.FinishedAt,
		&a.ResultSummary, &a.ErrorMessage, &a.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, "assignment not found")
			return
		}
		replyInternalError(w, h.logger, "get assignment", err)
		return
	}

	writeJSON(w, http.StatusOK, a)
}

// DispatchAssignment implements orchestrator.TaskDispatcher. It loads the
// target agent's configuration and credentials, then runs the agent in the
// correct crew container -- exactly like the Create handler but driven by the
// MissionEngine instead of a sidecar HTTP call.

// NOTE (#1754): this path is NOT subject to the delegation caps. The row
// already exists — the mission engine created it from a task list — so there is
// no dispatch decision to refuse here, and it stores depth 0 by column default.
// Its tasks therefore read as roots when their agents delegate.
//
// Capping the mission path means capping task-list creation, which is a
// different control on a different door — and #1768 built it there:
// mission_limits.go bounds an agent-created plan's task list and how many such
// missions a crew may have live. This function stays uncapped on purpose. What
// remains true here is the depth: a mission task starts a fresh delegation
// tree rather than continuing the authoring agent's.
func (h *AssignmentHandler) DispatchAssignment(ctx context.Context, req orchestrator.DispatchRequest) error {
	var target targetAgentInfo
	err := h.db.QueryRowContext(ctx, `
		SELECT a.id, a.slug, a.name, COALESCE(a.role_title,''), COALESCE(a.system_prompt_legacy,''),
		       a.cli_adapter, COALESCE(a.llm_model,''), a.tool_profile, a.timeout_seconds, a.memory_enabled, c.slug,
		       COALESCE(a.status,'')
		FROM agents a
		JOIN crews c ON c.id = a.crew_id
		WHERE a.id = ? AND a.deleted_at IS NULL
	`, req.AgentID).Scan(
		&target.ID, &target.Slug, &target.Name, &target.RoleTitle,
		&target.SystemPrompt, &target.CLIAdapter, &target.LLMModel,
		&target.ToolProfile, &target.TimeoutSeconds, &target.MemoryEnabled, &target.CrewSlug,
		&target.Status,
	)
	if err != nil {
		return fmt.Errorf("lookup agent %s: %w", req.AgentID, err)
	}

	// The third door into runAssignment, and the one this file's own NOTE says
	// carries no dispatch decision. That is true of the CAPS; it is not true of
	// the hold. A mission's task list can name a held agent — the same agent
	// another agent just created — so the sentinel has to be honoured here too.
	//
	// It is NOT the only place it is honoured, and it must not be the first.
	// The mission engine reads agents.status in the row lookup it already does
	// and refuses BEFORE it flips a task to IN_PROGRESS or writes an
	// assignment row (orchestrator/agent_hold.go), because a hold stands until
	// a human acts and a per-tick retry that writes a row first is a row per
	// tick forever. What survives here is the race: that read and this write
	// are not one statement, so an agent staged in between arrives at this
	// door, and a door that trusts its caller's check is not a door.
	//
	// The refusal carries DispatchDeferred (see agentHeldError), so the caller
	// unwinds the row it wrote and leaves the task PENDING for the next tick
	// rather than marking it terminally FAILED. Failing it was the bug: it
	// broke the guided ephemeral hire, whose whole flow is to sit in
	// PENDING_REVIEW while an operator approves.
	if held := refuseHeldAgent(target.Slug, target.Status); held != nil {
		h.logger.Info("mission dispatch deferred: target agent is held",
			"assignment_id", req.AssignmentID, "agent_id", req.AgentID, "status", target.Status)
		return held
	}

	// Inject trace context into task for observability
	task := req.Task
	if req.TraceID != "" {
		task = fmt.Sprintf("[trace:%s] %s", req.TraceID, req.Task)
	}

	// Load crew members for peer context (so the agent knows its teammates)
	crewMembers := h.loadCrewMembers(ctx, req.CrewID, req.AgentID)

	body := createAssignmentBody{
		TargetSlug:      target.Slug,
		Task:            task,
		CrewID:          req.CrewID,
		WorkspaceID:     req.WorkspaceID,
		ChatID:          req.ChatID,
		MissionID:       req.MissionID,
		CrewMembers:     crewMembers,
		LeadPlanning:    req.LeadPlanning,
		AuthorAgentID:   req.AuthorAgentID,
		CreatedByUserID: req.CreatedByUserID,
	}

	h.logger.Info("dispatching mission assignment",
		"assignment_id", req.AssignmentID,
		"mission_id", req.MissionID,
		"trace_id", req.TraceID,
		"agent", target.Slug,
		"crew", target.CrewSlug,
		"brief_len", len(body.Task),
	)

	// Per-crew admission control. Lead-planning assignments skip
	// the queue: a deferred lead deadlocks its whole mission while
	// it waits for slots that won't free until the lead's sub-
	// assignments complete. The lead is allowed to oversubscribe by
	// one. Everyone else competes for crew budget.
	if !req.LeadPlanning {
		budget, budgetErr := computeCrewBudget(ctx, h.db, req.CrewID)
		if budgetErr != nil {
			// Fall back to budget=1 so we under-provision rather
			// than oversubscribe. The completion-path pump catches
			// up on the next terminal status.
			h.logger.Warn("computeCrewBudget failed; falling back to budget=1",
				"crew_id", req.CrewID, "error", budgetErr)
			budget = 1
		}
		claimed, claimErr := claimCrewSlot(ctx, h.db, req.AssignmentID, req.CrewID, budget)
		if claimErr != nil {
			return fmt.Errorf("claim crew slot for %s: %w", req.AssignmentID, claimErr)
		}
		if !claimed {
			if err := markAssignmentQueued(ctx, h.db, req.AssignmentID); err != nil {
				return fmt.Errorf("mark queued %s: %w", req.AssignmentID, err)
			}
			h.emitAssignmentQueued(ctx, req.AssignmentID, req.ChatID, req.WorkspaceID, req.CrewID, target.Slug)
			h.logger.Info("assignment queued (crew at budget)",
				"assignment_id", req.AssignmentID,
				"mission_id", req.MissionID,
				"crew_id", req.CrewID,
				"crew", target.CrewSlug,
				"budget", budget,
			)
			// QUEUED is a tracked in-flight state from the
			// orchestrator's perspective — pumpAndDispatch picks it
			// up when an inflight run completes. Return nil so the
			// mission engine treats this as a successful dispatch.
			return nil
		}
		// Claim succeeded — emit the unqueued event for
		// observability (claim turned the row to RUNNING; UI may
		// want to animate even when the wait was zero). The
		// assignment_running event still follows from
		// runAssignment.
		h.emitAssignmentUnqueued(ctx, req.AssignmentID, req.ChatID, req.WorkspaceID, req.CrewID)
	}

	h.runAssignment(ctx, req.AssignmentID, body, target)
	return nil
}

// loadCrewMembers fetches all agents in a crew (except the given agent) for peer context.

func (h *AssignmentHandler) loadCrewMembers(ctx context.Context, crewID, excludeAgentID string) []orchestrator.CrewMember {
	rows, err := h.db.QueryContext(ctx, `
		SELECT a.id, a.slug, a.name, COALESCE(a.role_title, ''), COALESCE(a.description, '')
		FROM agents a
		WHERE a.crew_id = ? AND a.deleted_at IS NULL AND a.id != ?
		ORDER BY a.name ASC`, crewID, excludeAgentID)
	if err != nil {
		h.logger.Warn("load crew members for dispatch", "error", err)
		return nil
	}
	defer rows.Close()

	var members []orchestrator.CrewMember
	for rows.Next() {
		var m orchestrator.CrewMember
		if err := rows.Scan(&m.ID, &m.Slug, &m.Name, &m.RoleTitle, &m.Description); err != nil {
			continue
		}
		members = append(members, m)
	}
	return members
}
