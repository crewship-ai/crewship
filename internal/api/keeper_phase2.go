// Keeper Phase 2 (PR-C / PRD §6 F4) HTTP surface.
//
// Four endpoints share this file because they share the same wire
// shape (decision triple-state + reason + risk + ESCALATE-to-inbox)
// and the same dependencies (gatekeeper, policy resolver, inbox
// writer, consolidate.WriteLesson for F4.4). Splitting into four
// files would multiply boilerplate without buying anything; the
// switch-on-RequestType pattern from gatekeeper.buildPrompt is the
// model.
//
// Endpoints:
//
//	POST /api/v1/keeper/skill-review     — F4.1
//	POST /api/v1/keeper/behavior         — F4.2
//	POST /api/v1/keeper/memory-health    — F4.3
//	POST /api/v1/keeper/negative-learning — F4.4
//
// All four are internal-auth (X-Internal-Token) routes because they
// are platform-triggered (routines + hook handler) not operator-
// triggered. Each handler:
//
//  1. Validates the request body.
//  2. Resolves the per-crew policy (PR-B) to derive the inbox
//     blocking flag (strict/guided → Blocking=true; trusted/full → false).
//  3. Invokes the matching evaluator.
//  4. Persists a keeper_requests row with request_type = matching enum.
//  5. On ESCALATE (and on DENY for F4.1 / F4.4), writes an inbox_items
//     row via the inbox.Insert plumbing PR-Z Z.4 established.
//  6. For F4.4 ALLOW: writes a lessons.md entry via consolidate.WriteLesson.
//  7. Returns the decision payload to the caller.
//
// Persistence side-effects fan out from a single keeper_requests
// INSERT so the audit trail is uniform: every Phase 2 decision shows
// up in the same dedicated UI surface as access / execute decisions,
// discriminated only by request_type.
package api

import (
	"context"
	crand "crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/lookout"
	"github.com/crewship-ai/crewship/internal/policy"
	"github.com/crewship-ai/crewship/internal/skills"
)

// assertBodyWorkspaceMatchesCtx (audit round 3 defense) rejects F4
// requests where the body workspace_id doesn't match the request
// context workspace_id. The X-Internal-Token auth path doesn't bind
// the token to a workspace, so both values are caller-controlled —
// but enforcing consistency closes the asymmetric-bypass vector
// where a caller could pass workspace A in the query (which feeds
// policy resolution + paymaster scope) while passing workspace B in
// the body (which fed the self_learning gate lookup before this
// fix). Symmetric bypass (caller picks one workspace consistently)
// still requires PR-F24 token-to-workspace binding to close.
//
// Returns false (and writes the error response) when the values
// disagree — caller should immediately `return`.
//
// SECURITY round-8: a missing ctx workspace MUST be rejected, not
// treated as "no check needed". The original round-3 helper
// returned true when ctxWS=="" on the theory that the middleware
// would have ensured one — but that's the very assumption the
// gate is supposed to defend, so the fallback was a hole. If
// internalWsCtx didn't run (because of a misrouted handler, a
// future change to the middleware chain, or a bug), this helper
// would silently let body.workspace_id through. Now we fail loud:
// no ctx workspace, no gate, no operation.
func assertBodyWorkspaceMatchesCtx(w http.ResponseWriter, r *http.Request, bodyWS string) bool {
	ctxWS := WorkspaceIDFromContext(r.Context())
	if ctxWS == "" {
		replyError(w, http.StatusBadRequest, "request context is missing workspace_id; internal-auth handler reached without internalWsCtx middleware")
		return false
	}
	if ctxWS == bodyWS {
		return true
	}
	replyError(w, http.StatusBadRequest, "workspace_id in body must match workspace_id from request context")
	return false
}

// scopeKeeperRequest attaches a lookout.Scope to ctx so the keeper
// LLM call through paymaster middleware can attribute cost (and not
// fail with "paymaster: workspace_id required"). The F4 endpoints are
// internal-auth POSTs invoked by the platform itself (scheduler
// routines, behavior hook), so the inbound HTTP layer doesn't attach
// the request scope the way operator-facing routes do — we attach it
// explicitly here, mirroring the pattern in pipeline/runner_llm.go.
//
// agentID may be empty (skill_review fans out across crew agents; the
// scope still bills the crew). Empty WorkspaceID returns ctx unchanged
// so caller-side validation (which already rejects empty workspace_id
// at the body layer) is the single source of truth for that error.
func scopeKeeperRequest(ctx context.Context, workspaceID, crewID, agentID string) context.Context {
	if workspaceID == "" {
		return ctx
	}
	return lookout.WithScope(ctx, lookout.Scope{
		WorkspaceID: workspaceID,
		CrewID:      crewID,
		AgentID:     agentID,
	})
}

// KeeperPhase2Handler is the HTTP surface for the four F4 endpoints.
type KeeperPhase2Handler struct {
	db            *sql.DB
	logger        *slog.Logger
	internalToken string
	policy        *policy.Resolver
	skillEval     *gatekeeper.SkillReviewEvaluator
	behaviorEval  *gatekeeper.BehaviorEvaluator
	memHealthEval *gatekeeper.MemoryHealthEvaluator
	negativeEval  *gatekeeper.NegativeLearningEvaluator
}

// NewKeeperPhase2Handler builds the handler. Any evaluator may be nil
// — the corresponding endpoint returns 503 with a "not configured"
// body so partial rollouts (only F4.1 wired, F4.2-4 deferred) don't
// require a router branch.
func NewKeeperPhase2Handler(
	db *sql.DB,
	internalToken string,
	policyResolver *policy.Resolver,
	skillEval *gatekeeper.SkillReviewEvaluator,
	behaviorEval *gatekeeper.BehaviorEvaluator,
	memHealthEval *gatekeeper.MemoryHealthEvaluator,
	negativeEval *gatekeeper.NegativeLearningEvaluator,
	logger *slog.Logger,
) *KeeperPhase2Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &KeeperPhase2Handler{
		db:            db,
		logger:        logger,
		internalToken: internalToken,
		policy:        policyResolver,
		skillEval:     skillEval,
		behaviorEval:  behaviorEval,
		memHealthEval: memHealthEval,
		negativeEval:  negativeEval,
	}
}

// inboxBlockingForPolicy maps the resolved Policy → the inbox.Item
// .Blocking flag PR-B established. Strict/guided crews want a hard
// block; trusted/full crews want a non-blocking ping. Used by every
// F4 ESCALATE write so the operator UX is consistent.
func inboxBlockingForPolicy(p policy.Policy) bool {
	switch p.AutonomyLevel {
	case policy.AutonomyStrict, policy.AutonomyGuided:
		return true
	default:
		return false
	}
}

// resolvePolicySafe resolves the crew policy with a safe fallback. The
// F4 endpoints should never 500 because policy resolution had a
// transient error — the safer default (guided/warn) keeps the
// inbox-blocking surface conservative while letting the evaluator
// still run.
func (h *KeeperPhase2Handler) resolvePolicySafe(ctx context.Context, crewID string) policy.Policy {
	if h.policy == nil || crewID == "" {
		return policy.Policy{AutonomyLevel: policy.AutonomyGuided, BehaviorMode: policy.BehaviorWarn}
	}
	p, err := h.policy.Resolve(ctx, crewID)
	if err != nil {
		h.logger.Warn("keeper_phase2: policy resolve failed; using guided/warn defaults",
			"crew_id", crewID, "error", err)
		return policy.Policy{AutonomyLevel: policy.AutonomyGuided, BehaviorMode: policy.BehaviorWarn}
	}
	return p
}

// recordKeeperRequest is the shared INSERT into keeper_requests for
// every Phase 2 endpoint. Returns the generated request_id alongside
// any persistence error so handlers can fail-fast (HTTP 500) instead
// of replying 200 with an inbox row pointing at a request that was
// never recorded — silent audit loss is a worse failure mode than a
// surfaced 500.
func (h *KeeperPhase2Handler) recordKeeperRequest(
	ctx context.Context,
	reqType keeper.RequestType,
	agentID, crewID, intent, decision, reason string,
	risk int,
	prompt, raw string,
) (string, error) {
	suffix, err := randHexID()
	if err != nil {
		return "", fmt.Errorf("keeper_phase2: generate request id: %w", err)
	}
	id := "kpr_" + shortPrefix(reqType) + "_" + suffix
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := h.db.ExecContext(ctx, `
		INSERT INTO keeper_requests (
			id, requesting_agent_id, requesting_crew_id, credential_id,
			intent, decision, reason, risk_score, created_at, decided_at,
			request_type, ollama_prompt, ollama_raw_response
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, nullIfEmpty(agentID), nullIfEmpty(crewID), nullIfEmpty(""),
		intent, nullIfEmpty(decision), nullIfEmpty(reason), risk, now, now,
		string(reqType), nullIfEmpty(prompt), nullIfEmpty(raw),
	); err != nil {
		return "", fmt.Errorf("keeper_phase2: insert keeper_requests (%s): %w", reqType, err)
	}
	return id, nil
}

// shortPrefix returns a 3-char abbreviation for keeper_requests.id
// prefixes. Mirrors the pattern access/execute use elsewhere.
func shortPrefix(rt keeper.RequestType) string {
	switch rt {
	case keeper.RequestTypeSkillReview:
		return "skr"
	case keeper.RequestTypeBehavior:
		return "bhv"
	case keeper.RequestTypeMemoryHealth:
		return "mhc"
	case keeper.RequestTypeNegativeLearning:
		return "neg"
	}
	return "kp2"
}

// randHexID returns 12 hex chars of CSPRNG output for the suffix of a
// keeper_requests.id. The previous time-derived implementation (UnixNano
// >> 4-bit-shifts) could collide between two requests landing in the
// same nanosecond — paired with the insert-failure-swallow bug above
// that yielded silent audit loss. crypto/rand keeps the per-prefix id
// space large enough that collision is not a realistic concern.
func randHexID() (string, error) {
	b := make([]byte, 6) // 12 hex chars
	if _, err := crand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ----- F4.1: skill-review -----

type skillReviewBody struct {
	WorkspaceID      string                `json:"workspace_id"`
	CrewID           string                `json:"crew_id"`
	SkillID          string                `json:"skill_id"`
	SkillName        string                `json:"skill_name"`
	SkillDescription string                `json:"skill_description"`
	LifecycleState   string                `json:"lifecycle_state"`
	LastUsedAt       string                `json:"last_used_at,omitempty"`
	Assignments      int                   `json:"assignments"`
	AssignedAgents   []string              `json:"assigned_agents,omitempty"`
	Stats            gatekeeper.SkillStats `json:"stats"`
	FailureSnippets  []string              `json:"failure_snippets,omitempty"`
}

// HandleSkillReview is POST /api/v1/keeper/skill-review.
func (h *KeeperPhase2Handler) HandleSkillReview(w http.ResponseWriter, r *http.Request) {
	if h.skillEval == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "skill_review evaluator not configured"})
		return
	}
	var body skillReviewBody
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.WorkspaceID == "" || body.SkillID == "" {
		replyError(w, http.StatusBadRequest, "workspace_id and skill_id required")
		return
	}
	if !assertBodyWorkspaceMatchesCtx(w, r, body.WorkspaceID) {
		return
	}

	pol := h.resolvePolicySafe(r.Context(), body.CrewID)

	var lastUsed time.Time
	if body.LastUsedAt != "" {
		if t, err := time.Parse(time.RFC3339, body.LastUsedAt); err == nil {
			lastUsed = t
		}
	}

	req := gatekeeper.SkillReviewRequest{
		SkillID:          body.SkillID,
		SkillName:        body.SkillName,
		SkillDescription: body.SkillDescription,
		WorkspaceID:      body.WorkspaceID,
		AgentName:        "system",
		CrewName:         body.CrewID,
		LifecycleSnap: skills.LifecycleSnapshot{
			Current:           skills.LifecycleState(body.LifecycleState),
			LastUsedAt:        lastUsed,
			ActiveAssignments: body.Assignments,
			Now:               time.Now().UTC(),
		},
		AssignedAgents:  body.AssignedAgents,
		Stats:           body.Stats,
		FailureSnippets: body.FailureSnippets,
	}
	ctx := scopeKeeperRequest(r.Context(), body.WorkspaceID, body.CrewID, "")
	res, err := h.skillEval.Evaluate(ctx, req)
	if err != nil {
		h.logger.Error("keeper_phase2: skill_review eval failed", "error", err)
		replyError(w, http.StatusInternalServerError, "evaluator error")
		return
	}

	reqID, recErr := h.recordKeeperRequest(r.Context(), keeper.RequestTypeSkillReview,
		"", body.CrewID, "F4.1 skill review for "+body.SkillName,
		string(res.Decision), res.Reason, res.RiskScore, res.Prompt, res.RawLLMResponse)
	if recErr != nil {
		h.logger.Error("keeper_phase2: skill_review record failed", "error", recErr)
		replyError(w, http.StatusInternalServerError, "persistence error")
		return
	}

	if res.Decision == keeper.DecisionEscalate || res.Decision == keeper.DecisionDeny {
		_ = inbox.Insert(r.Context(), h.db, h.logger, inbox.Item{
			WorkspaceID: body.WorkspaceID,
			Kind:        inbox.KindEscalation,
			SourceID:    reqID,
			TargetRole:  "MANAGER",
			Title:       fmt.Sprintf("Skill review: %s (%s)", body.SkillName, res.Decision),
			BodyMD:      res.Reason,
			SenderType:  "system",
			SenderID:    "keeper_skill_review",
			SenderName:  "Skill Curator",
			Priority:    "medium",
			Blocking:    res.Decision == keeper.DecisionDeny || inboxBlockingForPolicy(pol),
			Payload: map[string]interface{}{
				"request_id":            reqID,
				"request_type":          string(keeper.RequestTypeSkillReview),
				"skill_id":              body.SkillID,
				"decision":              string(res.Decision),
				"proposed_lifecycle":    string(res.ProposedLifecycle.Next),
				"verify_after_decide":   res.VerifyAfterDecide,
				"unverify_after_decide": res.UnverifyAfterDecide,
			},
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"request_id":            reqID,
		"decision":              string(res.Decision),
		"reason":                res.Reason,
		"risk_score":            res.RiskScore,
		"verify_after_decide":   res.VerifyAfterDecide,
		"unverify_after_decide": res.UnverifyAfterDecide,
		"proposed_lifecycle":    string(res.ProposedLifecycle.Next),
	})
}

// ----- F4.2: behavior -----

type behaviorBody struct {
	WorkspaceID     string   `json:"workspace_id"`
	CrewID          string   `json:"crew_id"`
	AgentID         string   `json:"agent_id"`
	AgentName       string   `json:"agent_name"`
	CrewName        string   `json:"crew_name"`
	ToolName        string   `json:"tool_name"`
	ToolArgsSnippet string   `json:"tool_args_snippet"`
	RecentToolCalls []string `json:"recent_tool_calls"`
}

// HandleBehavior is POST /api/v1/keeper/behavior.
func (h *KeeperPhase2Handler) HandleBehavior(w http.ResponseWriter, r *http.Request) {
	if h.behaviorEval == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "behavior evaluator not configured"})
		return
	}
	var body behaviorBody
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.WorkspaceID == "" || body.CrewID == "" || body.ToolName == "" {
		replyError(w, http.StatusBadRequest, "workspace_id, crew_id, tool_name required")
		return
	}
	if !assertBodyWorkspaceMatchesCtx(w, r, body.WorkspaceID) {
		return
	}

	pol := h.resolvePolicySafe(r.Context(), body.CrewID)

	ctx := scopeKeeperRequest(r.Context(), body.WorkspaceID, body.CrewID, body.AgentID)
	res, err := h.behaviorEval.Evaluate(ctx, gatekeeper.BehaviorReviewRequest{
		WorkspaceID:     body.WorkspaceID,
		CrewID:          body.CrewID,
		AgentName:       body.AgentName,
		CrewName:        body.CrewName,
		BehaviorMode:    pol.BehaviorMode,
		AutonomyLevel:   pol.AutonomyLevel,
		ToolName:        body.ToolName,
		ToolArgsSnippet: body.ToolArgsSnippet,
		RecentToolCalls: body.RecentToolCalls,
	})
	if err != nil {
		h.logger.Error("keeper_phase2: behavior eval failed", "error", err)
		replyError(w, http.StatusInternalServerError, "evaluator error")
		return
	}

	reqID, recErr := h.recordKeeperRequest(r.Context(), keeper.RequestTypeBehavior,
		body.AgentID, body.CrewID, "F4.2 behavior check on "+body.ToolName,
		string(res.Decision), res.Reason, res.RiskScore, res.Prompt, res.RawLLMResponse)
	if recErr != nil {
		h.logger.Error("keeper_phase2: behavior record failed", "error", recErr)
		replyError(w, http.StatusInternalServerError, "persistence error")
		return
	}

	// Write to inbox when the PolicyDecision says inbox / block_inbox.
	switch res.PolicyDecision {
	case policy.DecisionInboxApprove, policy.DecisionAutoLogInbox,
		policy.DecisionBlockInbox:
		_ = inbox.Insert(r.Context(), h.db, h.logger, inbox.Item{
			WorkspaceID: body.WorkspaceID,
			Kind:        inbox.KindEscalation,
			SourceID:    reqID,
			TargetRole:  "MANAGER",
			Title:       fmt.Sprintf("Behavior monitor: %s on %s (%s)", body.AgentName, body.ToolName, res.Decision),
			BodyMD:      res.Reason,
			SenderType:  "system",
			SenderID:    "keeper_behavior",
			SenderName:  "Behavior Monitor",
			Priority:    behaviorPriorityForDecision(res.Decision),
			Blocking:    res.PolicyDecision == policy.DecisionBlockInbox,
			Payload: map[string]interface{}{
				"request_id":      reqID,
				"request_type":    string(keeper.RequestTypeBehavior),
				"agent_id":        body.AgentID,
				"tool_name":       body.ToolName,
				"decision":        string(res.Decision),
				"policy_decision": string(res.PolicyDecision),
				"should_block":    res.ShouldBlock,
			},
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"request_id":      reqID,
		"decision":        string(res.Decision),
		"reason":          res.Reason,
		"risk_score":      res.RiskScore,
		"should_block":    res.ShouldBlock,
		"policy_decision": string(res.PolicyDecision),
	})
}

func behaviorPriorityForDecision(d gatekeeper.BehaviorDecision) string {
	switch d {
	case gatekeeper.BehaviorDeny:
		return "high"
	case gatekeeper.BehaviorEscalate:
		return "medium"
	case gatekeeper.BehaviorWarn:
		return "low"
	}
	return "medium"
}

// ----- F4.3: memory-health -----

type memoryHealthBody struct {
	WorkspaceID        string                     `json:"workspace_id"`
	CrewID             string                     `json:"crew_id"`
	CrewName           string                     `json:"crew_name"`
	AgentName          string                     `json:"agent_name"`
	Snapshot           consolidate.HealthSnapshot `json:"snapshot"`
	AgentMDBytes       int                        `json:"agent_md_bytes"`
	PersonaMDBytes     int                        `json:"persona_md_bytes"`
	CrewMDBytes        int                        `json:"crew_md_bytes"`
	StalestEntryDays   int                        `json:"stalest_entry_days"`
	ContradictionCount int                        `json:"contradiction_count"`
}

// HandleMemoryHealth is POST /api/v1/keeper/memory-health.
func (h *KeeperPhase2Handler) HandleMemoryHealth(w http.ResponseWriter, r *http.Request) {
	if h.memHealthEval == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "memory_health evaluator not configured"})
		return
	}
	var body memoryHealthBody
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.WorkspaceID == "" {
		replyError(w, http.StatusBadRequest, "workspace_id required")
		return
	}
	if !assertBodyWorkspaceMatchesCtx(w, r, body.WorkspaceID) {
		return
	}

	pol := h.resolvePolicySafe(r.Context(), body.CrewID)

	// memHealth body has no AgentID — health snapshot is crew-scoped.
	ctx := scopeKeeperRequest(r.Context(), body.WorkspaceID, body.CrewID, "")
	res, err := h.memHealthEval.Evaluate(ctx, gatekeeper.MemoryHealthRequest{
		WorkspaceID:        body.WorkspaceID,
		CrewID:             body.CrewID,
		AgentName:          body.AgentName,
		CrewName:           body.CrewName,
		Snapshot:           body.Snapshot,
		AgentMDBytes:       body.AgentMDBytes,
		PersonaMDBytes:     body.PersonaMDBytes,
		CrewMDBytes:        body.CrewMDBytes,
		StalestEntryDays:   body.StalestEntryDays,
		ContradictionCount: body.ContradictionCount,
	})
	if err != nil {
		h.logger.Error("keeper_phase2: memory_health eval failed", "error", err)
		replyError(w, http.StatusInternalServerError, "evaluator error")
		return
	}

	reqID, recErr := h.recordKeeperRequest(r.Context(), keeper.RequestTypeMemoryHealth,
		"", body.CrewID, "F4.3 daily memory health sweep",
		string(res.Decision), res.Reason, res.RiskScore, res.Prompt, res.RawLLMResponse)
	if recErr != nil {
		h.logger.Error("keeper_phase2: memory_health record failed", "error", recErr)
		replyError(w, http.StatusInternalServerError, "persistence error")
		return
	}

	if res.Decision == keeper.DecisionEscalate {
		_ = inbox.Insert(r.Context(), h.db, h.logger, inbox.Item{
			WorkspaceID: body.WorkspaceID,
			Kind:        inbox.KindEscalation,
			SourceID:    reqID,
			TargetRole:  "MANAGER",
			Title:       fmt.Sprintf("Memory health: %s (overall %.0f)", body.CrewName, res.OverallScore),
			BodyMD:      res.Reason,
			SenderType:  "system",
			SenderID:    "keeper_memory_health",
			SenderName:  "Memory Health",
			Priority:    "medium",
			Blocking:    inboxBlockingForPolicy(pol),
			Payload: map[string]interface{}{
				"request_id":          reqID,
				"request_type":        string(keeper.RequestTypeMemoryHealth),
				"overall_score":       res.OverallScore,
				"contradiction_count": body.ContradictionCount,
				"auto_consolidate":    res.AutoConsolidate,
			},
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"request_id":       reqID,
		"decision":         string(res.Decision),
		"reason":           res.Reason,
		"risk_score":       res.RiskScore,
		"auto_consolidate": res.AutoConsolidate,
		"overall_score":    res.OverallScore,
	})
}

// ----- F4.4: negative-learning -----

type negativeLearningBody struct {
	WorkspaceID    string `json:"workspace_id"`
	CrewID         string `json:"crew_id"`
	AgentID        string `json:"agent_id"`
	AgentName      string `json:"agent_name"`
	CrewName       string `json:"crew_name"`
	AgentMemoryDir string `json:"agent_memory_dir"`
	Trigger        string `json:"trigger"`
	ToolName       string `json:"tool_name,omitempty"`
	FailureSnippet string `json:"failure_snippet"`
	PriorLesson    string `json:"prior_lesson,omitempty"`
}

// HandleNegativeLearning is POST /api/v1/keeper/negative-learning.
func (h *KeeperPhase2Handler) HandleNegativeLearning(w http.ResponseWriter, r *http.Request) {
	if h.negativeEval == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "negative_learning evaluator not configured"})
		return
	}
	var body negativeLearningBody
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.WorkspaceID == "" || body.Trigger == "" || body.FailureSnippet == "" {
		replyError(w, http.StatusBadRequest, "workspace_id, trigger, failure_snippet required")
		return
	}
	if !assertBodyWorkspaceMatchesCtx(w, r, body.WorkspaceID) {
		return
	}

	pol := h.resolvePolicySafe(r.Context(), body.CrewID)

	ctx := scopeKeeperRequest(r.Context(), body.WorkspaceID, body.CrewID, body.AgentID)
	res, err := h.negativeEval.Evaluate(ctx, gatekeeper.NegativeLearningRequest{
		WorkspaceID:    body.WorkspaceID,
		CrewID:         body.CrewID,
		AgentName:      body.AgentName,
		CrewName:       body.CrewName,
		AgentMemoryDir: body.AgentMemoryDir,
		Trigger:        gatekeeper.NegativeTrigger(body.Trigger),
		ToolName:       body.ToolName,
		FailureSnippet: body.FailureSnippet,
		PriorLesson:    body.PriorLesson,
	})
	if err != nil {
		h.logger.Error("keeper_phase2: negative_learning eval failed", "error", err)
		replyError(w, http.StatusBadRequest, "evaluator error: "+err.Error())
		return
	}

	reqID, recErr := h.recordKeeperRequest(r.Context(), keeper.RequestTypeNegativeLearning,
		body.AgentID, body.CrewID, "F4.4 negative learning from "+body.Trigger,
		string(res.Decision), res.Reason, res.RiskScore, res.Prompt, res.RawLLMResponse)
	if recErr != nil {
		h.logger.Error("keeper_phase2: negative_learning record failed", "error", recErr)
		replyError(w, http.StatusInternalServerError, "persistence error")
		return
	}

	// PR-G F4.1 UX — self_learning gate on the ALLOW path. The
	// evaluator's WriteLesson=true says "this lesson is worth keeping",
	// but whether it AUTO-applies vs queues operator approval depends
	// on the per-agent self_learning_enabled flag (v106).
	//
	//   self_learning=1  → write the lesson now (current pre-PR-G behavior)
	//   self_learning=0  → don't write; instead queue a blocking inbox
	//                      item so the operator approves before the
	//                      agent's lessons.md changes.
	//
	// AgentID is required to look up the flag. If body.AgentID is empty
	// (legacy callers that haven't been updated), default to OFF —
	// safer to require operator approval than silently auto-apply.
	autoApplyLesson := false
	if res.WriteLesson && body.AgentMemoryDir != "" {
		if body.AgentID == "" {
			h.logger.Warn("keeper_phase2: ALLOW lesson skipped (agent_id missing, can't resolve self_learning)",
				"workspace_id", body.WorkspaceID)
		} else {
			enabled, err := loadSelfLearningEnabled(r.Context(), h.db, body.WorkspaceID, body.AgentID)
			if err != nil {
				h.logger.Warn("keeper_phase2: self_learning lookup failed; defaulting to OFF (require approval)",
					"agent_id", body.AgentID, "error", err)
				enabled = false
			}
			autoApplyLesson = enabled
		}
	}

	if autoApplyLesson {
		werr := consolidate.WriteLesson(r.Context(), body.AgentMemoryDir, consolidate.LessonEntry{
			ID:          res.Proposal.ID,
			Kind:        res.Proposal.Kind,
			Source:      res.Proposal.Source,
			Rule:        res.Proposal.Rule,
			ContextNote: res.Proposal.Note,
		})
		if werr != nil {
			h.logger.Warn("keeper_phase2: WriteLesson failed (decision still recorded)",
				"agent_memory_dir", body.AgentMemoryDir, "error", werr)
		}
	} else if res.WriteLesson && body.AgentMemoryDir != "" {
		// ALLOW but self_learning OFF — queue blocking inbox so an
		// operator can approve the proposed lesson before it lands on
		// the agent's lessons.md. Payload carries the full lesson
		// proposal so the approve handler has everything it needs.
		// Insert failures MUST surface — silently swallowing here
		// would lose the proposal entirely (lessons.md isn't written
		// AND no inbox item exists) while the handler returns 200,
		// which is the worst failure mode.
		if err := inbox.Insert(r.Context(), h.db, h.logger, inbox.Item{
			WorkspaceID: body.WorkspaceID,
			Kind:        inbox.KindEscalation,
			SourceID:    reqID,
			TargetRole:  "MANAGER",
			Title:       fmt.Sprintf("Lesson proposal: %s (%s)", body.AgentName, body.Trigger),
			BodyMD:      fmt.Sprintf("**Proposed lesson** (auto-apply blocked by self_learning=OFF):\n\n%s\n\n_Reason: %s_", res.Proposal.Rule, res.Reason),
			SenderType:  "system",
			SenderID:    "keeper_negative_learning",
			SenderName:  "Negative Learning",
			Priority:    "low",
			Blocking:    true,
			Payload: map[string]interface{}{
				"request_id":         reqID,
				"request_type":       string(keeper.RequestTypeNegativeLearning),
				"agent_id":           body.AgentID,
				"agent_memory_dir":   body.AgentMemoryDir,
				"lesson_id":          res.Proposal.ID,
				"lesson_kind":        string(res.Proposal.Kind),
				"lesson_rule":        res.Proposal.Rule,
				"lesson_context":     res.Proposal.Note,
				"lesson_source":      string(res.Proposal.Source),
				"self_learning_gate": "off",
			},
		}); err != nil {
			h.logger.Error("keeper_phase2: enqueue gated lesson proposal failed",
				"request_id", reqID, "agent_id", body.AgentID, "error", err)
			replyError(w, http.StatusInternalServerError, "failed to queue lesson proposal for approval")
			return
		}
	}

	// Surface BOTH ESCALATE and DENY to the operator inbox. DENY here
	// means the Curator dropped the failure signal as transient/noise;
	// without an inbox row the operator never sees that a failure event
	// landed at the negative-learning surface at all — silently
	// disappearing failure feedback is the opposite of audit-friendly.
	// (ALLOW writes a lessons.md row instead; that's the user-visible
	// surface for the success path.)
	if res.Decision == keeper.DecisionEscalate || res.Decision == keeper.DecisionDeny {
		_ = inbox.Insert(r.Context(), h.db, h.logger, inbox.Item{
			WorkspaceID: body.WorkspaceID,
			Kind:        inbox.KindEscalation,
			SourceID:    reqID,
			TargetRole:  "MANAGER",
			Title:       fmt.Sprintf("Negative learning %s: %s (%s)", res.Decision, body.AgentName, body.Trigger),
			BodyMD:      res.Reason,
			SenderType:  "system",
			SenderID:    "keeper_negative_learning",
			SenderName:  "Negative Learning",
			Priority:    "high",
			Blocking:    inboxBlockingForPolicy(pol),
			Payload: map[string]interface{}{
				"request_id":   reqID,
				"request_type": string(keeper.RequestTypeNegativeLearning),
				"agent_id":     body.AgentID,
				"trigger":      body.Trigger,
				"tool_name":    body.ToolName,
				"decision":     string(res.Decision),
			},
		})
	}

	resp := map[string]any{
		"request_id":   reqID,
		"decision":     string(res.Decision),
		"reason":       res.Reason,
		"risk_score":   res.RiskScore,
		"write_lesson": res.WriteLesson,
	}
	if res.WriteLesson {
		resp["lesson_id"] = res.Proposal.ID
	}
	writeJSON(w, http.StatusOK, resp)
}

// loadSelfLearningEnabled reads agents.self_learning_enabled (v106) for
// the given agent. Returns false if the agent doesn't exist (or has
// the flag off). Used by:
//
//   - F4.4 negative_learning ALLOW path (HandleNegativeLearning) — decides
//     whether a proposed lesson auto-applies or queues for operator approval.
//   - F6 persona_suggest auto-apply path (SuggestAgentPersona) — demotes
//     auto-apply to inbox approval when an agent's flag is off, even on a
//     crew autonomy level that would normally bypass the inbox.
//
// Package-level (not a method) so both handlers can call it without
// dragging a *KeeperPhase2Handler into the persona surface. PR-G F4.1 UX gate.
//
// SECURITY (defense in depth, partial close — see PR-F24 for full):
// workspace_id is part of the WHERE clause so a lookup with an
// agent_id from workspace A and a workspace_id of B returns no row
// (the row's workspace_id IS A, not B). This closes the asymmetric
// case where the caller passes inconsistent workspace identifiers
// across the request surface. It does NOT close the symmetric case
// where a caller consistently passes the same target workspace in
// both query + body — that requires binding the X-Internal-Token to
// a workspace, which is tracked as PR-F24 (token-to-workspace
// binding) in PRD §10. The F4 handlers add a layered defense via
// assertBodyWorkspaceMatchesCtx so body.WorkspaceID can't disagree
// with ctx.WorkspaceID even from the same trusted caller.
//
// Empty workspaceID returns false (safe default) — the caller's own
// validation should have rejected the empty value upstream.
func loadSelfLearningEnabled(ctx context.Context, db *sql.DB, workspaceID, agentID string) (bool, error) {
	if db == nil || agentID == "" || workspaceID == "" {
		return false, nil
	}
	var enabled int
	err := db.QueryRowContext(ctx, `
		SELECT self_learning_enabled
		FROM agents
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		agentID, workspaceID,
	).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return enabled == 1, nil
}
