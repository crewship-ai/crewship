package api

import (
	"errors"
	"math"
	"net/http"

	"github.com/crewship-ai/crewship/internal/harbormaster"
	"github.com/crewship-ai/crewship/internal/paymaster"
	"github.com/crewship-ai/crewship/internal/policy"
)

type chatRunBudget struct {
	ID           string  `json:"id"`
	Scope        string  `json:"scope"`
	Window       string  `json:"window"`
	Mode         string  `json:"mode"`
	LimitUSD     float64 `json:"limit_usd"`
	SpentUSD     float64 `json:"spent_usd"`
	RemainingUSD float64 `json:"remaining_usd"`
	State        string  `json:"state"`
}
type chatRunGateProjection struct {
	ConfiguredMode string `json:"configured_mode"`
	PredictedMode  string `json:"predicted_mode"`
	RuleMatches    bool   `json:"rule_matches"`
	Blocking       bool   `json:"blocking"`
	Reason         string `json:"reason"`
}

// Credential identity and vault delivery metadata are deliberately absent.
// The label follows existing credential visibility; otherwise it is generic.
type chatRunPayer struct {
	State    string `json:"state"`
	Category string `json:"category"`
	Label    string `json:"label"`
	Provider string `json:"provider,omitempty"`
}

type chatRunPreflightResponse struct {
	Scope         string                `json:"scope"`
	AgentID       string                `json:"agent_id"`
	Model         string                `json:"model"`
	Payer         chatRunPayer          `json:"payer"`
	Budgets       []chatRunBudget       `json:"budgets"`
	Gate          chatRunGateProjection `json:"gate"`
	EstimateUSD   *float64              `json:"estimate_usd"`
	EstimateState string                `json:"estimate_state"`
	Constraints   []string              `json:"constraints"`
}

// chatRunPreflight reads current facts for this chat's configured agent/model.
// It neither enqueues an approval nor reserves funds. Paid-provider admission
// remains authoritative and checks live policy after any human wait.
func (r *Router) chatRunPreflight(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	chat := req.PathValue("chatId")
	audience, err := r.entityRefAudience(req, chat)
	if err != nil {
		if errors.Is(err, errEntityAudienceDenied) {
			replyError(w, 404, "Chat not found")
		} else {
			replyError(w, 503, "run_preflight_unavailable")
		}
		return
	}
	payer, err := resolveAgentCredentialReadiness(req, r.db, audience.WorkspaceID, audience.AgentID)
	if err != nil {
		replyError(w, 503, "run_preflight_unavailable")
		return
	}
	var crew, slug, role, model, configured string
	err = r.db.QueryRowContext(req.Context(), `SELECT a.crew_id,a.slug,a.agent_role,COALESCE(a.llm_model,'') FROM agents a JOIN crews c ON c.id=a.crew_id AND c.workspace_id=a.workspace_id WHERE a.id=? AND a.workspace_id=? AND a.deleted_at IS NULL AND c.deleted_at IS NULL`, audience.AgentID, audience.WorkspaceID).Scan(&crew, &slug, &role, &model)
	if err != nil {
		replyError(w, 503, "run_preflight_unavailable")
		return
	}
	pol, err := r.PolicyResolver().Resolve(req.Context(), crew)
	if err != nil {
		replyError(w, 503, "run_preflight_unavailable")
		return
	}
	configured = policy.ApprovalModeForLevel(pol.AutonomyLevel)
	tx, err := r.db.BeginTx(req.Context(), nil)
	if err != nil {
		replyError(w, 503, "run_preflight_unavailable")
		return
	}
	defer tx.Rollback()
	statuses, err := paymaster.CheckTx(req.Context(), tx, paymaster.Scope{WorkspaceID: audience.WorkspaceID, CrewID: crew, AgentID: audience.AgentID})
	if err != nil {
		replyError(w, 503, "run_preflight_unavailable")
		return
	}
	tx.Rollback()
	mode := harbormaster.ModeNone
	switch configured {
	case "sync":
		mode = harbormaster.ModeSync
	case "async":
		mode = harbormaster.ModeAsync
	}
	args := map[string]any{"agent_slug": slug, "agent_role": role}
	evaluator, err := harbormaster.EvaluatorForCrew(req.Context(), r.db, audience.WorkspaceID, crew, nil)
	if err != nil {
		replyError(w, 503, "run_preflight_unavailable")
		return
	}
	matches, reason, _ := evaluator.Evaluate(req.Context(), "agent_run", args)
	if mode != harbormaster.ModeNone && matches {
		mode, _, err = harbormaster.AdjustMode(req.Context(), r.db, audience.WorkspaceID, "agent_run", args, mode)
		if err != nil {
			replyError(w, 503, "run_preflight_unavailable")
			return
		}
	}
	out := chatRunPreflightResponse{Scope: "configured_chat_agent", AgentID: audience.AgentID, Model: model, Payer: safeChatRunPayer(payer.ModelCredential), Budgets: []chatRunBudget{}, Gate: chatRunGateProjection{ConfiguredMode: configured, PredictedMode: mode.String(), RuleMatches: matches, Blocking: matches && mode == harbormaster.ModeSync, Reason: reason}, EstimateState: "unknown", Constraints: []string{"Snapshot only: funds are not reserved and approval is not granted.", "Model or provider overrides require a fresh preflight for that launch.", "Provider admission rechecks live budget policy before paid requests."}}
	for _, s := range statuses {
		out.Budgets = append(out.Budgets, chatRunBudget{ID: s.Budget.ID, Scope: string(s.Budget.ScopeKind), Window: string(s.Budget.Window), Mode: string(s.Budget.Mode), LimitUSD: s.LimitUSD, SpentUSD: s.SpentUSD, RemainingUSD: math.Max(0, s.LimitUSD-s.SpentUSD), State: string(s.State)})
	}
	var hard int
	err = r.db.QueryRowContext(req.Context(), `SELECT COUNT(*) FROM budget_limits WHERE workspace_id=? AND enabled=1 AND mode IN ('hard','tiered') AND (scope_kind='workspace' OR (scope_kind='crew' AND scope_id=?) OR (scope_kind='agent' AND scope_id=?) OR scope_kind='mission')`, audience.WorkspaceID, crew, audience.AgentID).Scan(&hard)
	if err != nil {
		replyError(w, 503, "run_preflight_unavailable")
		return
	}
	if hard > 0 {
		out.Constraints = append(out.Constraints, "Legacy proxy requests under hard budgets require the restricted broker; a positive remaining balance alone does not admit a request.")
	}
	// Do not disclose protected facts after concurrent access withdrawal.
	fresh, err := r.entityRefAudience(req, chat)
	if err != nil || *fresh != *audience {
		replyError(w, 404, "Chat not found")
		return
	}
	writeJSON(w, 200, out)
}

func safeChatRunPayer(payer agentModelCredential) chatRunPayer {
	out := chatRunPayer{State: payer.State, Category: "unknown", Label: "Payer unknown", Provider: payer.Provider}
	if payer.State == "ready" {
		out.Category = "connected_provider_account"
		out.Label = "Connected provider account"
		if payer.CredentialName != "" {
			out.Label = payer.CredentialName
		}
	}
	return out
}
