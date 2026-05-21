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
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/policy"
	"github.com/crewship-ai/crewship/internal/skills"
)

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
// every Phase 2 endpoint. Returns the generated request_id so handlers
// can use it as the inbox source_id (idempotent dedup at the inbox
// layer).
func (h *KeeperPhase2Handler) recordKeeperRequest(
	ctx context.Context,
	reqType keeper.RequestType,
	agentID, crewID, intent, decision, reason string,
	risk int,
	prompt, raw string,
) string {
	id := "kpr_" + shortPrefix(reqType) + "_" + randHexID()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := h.db.ExecContext(ctx, `
		INSERT INTO keeper_requests (
			id, requesting_agent_id, requesting_crew_id, credential_id,
			intent, decision, reason, risk_score, created_at, decided_at,
			request_type, ollama_prompt, ollama_raw_response
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, nullIfEmpty(agentID), nullIfEmpty(crewID), nullIfEmpty(""),
		intent, nullIfEmpty(decision), nullIfEmpty(reason), risk, now, now,
		string(reqType), nullIfEmpty(prompt), nullIfEmpty(raw),
	)
	if err != nil {
		h.logger.Warn("keeper_phase2: insert keeper_requests failed",
			"request_type", reqType, "error", err)
	}
	return id
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

// randHexID is a small local helper. 12 hex chars is plenty for an
// id that's already namespaced by request_type prefix; mirrors
// internal/consolidate/health.go's randomHex.
func randHexID() string {
	const hexdigits = "0123456789abcdef"
	now := time.Now().UnixNano()
	b := make([]byte, 12)
	for i := range b {
		b[i] = hexdigits[int(now>>(i*4))&0xf]
	}
	return string(b)
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
	res, err := h.skillEval.Evaluate(r.Context(), req)
	if err != nil {
		h.logger.Error("keeper_phase2: skill_review eval failed", "error", err)
		replyError(w, http.StatusInternalServerError, "evaluator error")
		return
	}

	reqID := h.recordKeeperRequest(r.Context(), keeper.RequestTypeSkillReview,
		"", body.CrewID, "F4.1 skill review for "+body.SkillName,
		string(res.Decision), res.Reason, res.RiskScore, res.Prompt, res.RawLLMResponse)

	if res.Decision == keeper.DecisionEscalate || res.Decision == keeper.DecisionDeny {
		inbox.Insert(r.Context(), h.db, h.logger, inbox.Item{
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

	pol := h.resolvePolicySafe(r.Context(), body.CrewID)

	res, err := h.behaviorEval.Evaluate(r.Context(), gatekeeper.BehaviorReviewRequest{
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

	reqID := h.recordKeeperRequest(r.Context(), keeper.RequestTypeBehavior,
		body.AgentID, body.CrewID, "F4.2 behavior check on "+body.ToolName,
		string(res.Decision), res.Reason, res.RiskScore, res.Prompt, res.RawLLMResponse)

	// Write to inbox when the PolicyDecision says inbox / block_inbox.
	switch res.PolicyDecision {
	case policy.DecisionInboxApprove, policy.DecisionAutoLogInbox,
		policy.DecisionBlockInbox:
		inbox.Insert(r.Context(), h.db, h.logger, inbox.Item{
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

	pol := h.resolvePolicySafe(r.Context(), body.CrewID)

	res, err := h.memHealthEval.Evaluate(r.Context(), gatekeeper.MemoryHealthRequest{
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

	reqID := h.recordKeeperRequest(r.Context(), keeper.RequestTypeMemoryHealth,
		"", body.CrewID, "F4.3 daily memory health sweep",
		string(res.Decision), res.Reason, res.RiskScore, res.Prompt, res.RawLLMResponse)

	if res.Decision == keeper.DecisionEscalate {
		inbox.Insert(r.Context(), h.db, h.logger, inbox.Item{
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

	pol := h.resolvePolicySafe(r.Context(), body.CrewID)

	res, err := h.negativeEval.Evaluate(r.Context(), gatekeeper.NegativeLearningRequest{
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

	reqID := h.recordKeeperRequest(r.Context(), keeper.RequestTypeNegativeLearning,
		body.AgentID, body.CrewID, "F4.4 negative learning from "+body.Trigger,
		string(res.Decision), res.Reason, res.RiskScore, res.Prompt, res.RawLLMResponse)

	// ALLOW writes to lessons.md via consolidate.WriteLesson. This is
	// the F4.4 → Z.7 hand-off — the integration test in
	// negative_learning_evaluator_test.go covers the round-trip.
	if res.WriteLesson && body.AgentMemoryDir != "" {
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
	}

	if res.Decision == keeper.DecisionEscalate {
		inbox.Insert(r.Context(), h.db, h.logger, inbox.Item{
			WorkspaceID: body.WorkspaceID,
			Kind:        inbox.KindEscalation,
			SourceID:    reqID,
			TargetRole:  "MANAGER",
			Title:       fmt.Sprintf("Negative learning escalation: %s (%s)", body.AgentName, body.Trigger),
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
