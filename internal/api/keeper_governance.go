package api

// Keeper watchdog governance settings (issue #1001, M0): the OWNER/ADMIN
// in-app toggle, the named security contact the watchdog snitches to, and
// the DENY-notify risk threshold. GET/PUT /api/v1/admin/keeper/governance.
//
// No row = the watchdog is OFF for the workspace (opt-in, default OFF) — the
// GET reports configured=false + enabled=false; the first PUT creates the
// explicit row. PUT is a partial update: only the fields the caller sends are
// merged, so single-field edits don't clobber each other.

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/keeper/governance"
)

type KeeperGovernanceHandler struct {
	db      *sql.DB
	logger  *slog.Logger
	journal journal.Emitter
}

func NewKeeperGovernanceHandler(db *sql.DB, logger *slog.Logger, j journal.Emitter) *KeeperGovernanceHandler {
	if j == nil {
		j = noopEmitter{}
	}
	return &KeeperGovernanceHandler{db: db, logger: logger, journal: j}
}

type keeperGovernanceResponse struct {
	Configured bool `json:"configured"`
	governance.Settings
	// EffectiveSecondApprover is the four-eyes rule as ENFORCED, which is not
	// what require_second_approver alone says (issue #1559). See the type.
	EffectiveSecondApprover effectiveSecondApprover `json:"effective_second_approver"`
	// Warning is a non-blocking advisory returned by Put — e.g. enabling the
	// second-approver rule on a workspace that lacks a second eligible approver.
	// Empty on Get and on a clean Put.
	Warning string `json:"warning,omitempty"`
}

// Sources for effectiveSecondApprover.Source. Stable wire values — the console
// and the CLI branch on them.
const (
	effectiveSourceWorkspace = "workspace"
	effectiveSourceTier      = "tier"
	effectiveSourceNone      = "none"
)

// effectiveSecondApprover answers "when does resolving a credential escalation
// actually need a different approver?".
//
// The stored toggle does not answer it. keeper.TierPolicy.SecondApprover forces
// the rule on the top tier regardless of the toggle (escalation_handler.go), so
// a workspace with the toggle OFF still gets four-eyes on its most dangerous
// credentials — and until #1559 nothing said so, which meant the first thing
// that taught an operator was a 403 on their own approval.
//
// Expressed as a floor rather than a boolean because that is the shape of the
// rule: it applies from some security level upward. The tier can only raise
// that floor's reach, never lower it — turning the toggle on cannot exempt L4.
type effectiveSecondApprover struct {
	// MinSecurityLevel is the lowest credential security level at which the
	// rule applies. 0 means it never does.
	MinSecurityLevel int `json:"min_security_level"`
	// MinSecurityLevelLabel is that level's operator-facing name ("L4 ·
	// critical"), taken from the tier table so the console and the CLI cannot
	// invent their own. Empty when the rule never applies.
	MinSecurityLevelLabel string `json:"min_security_level_label,omitempty"`
	// Source is what puts the rule in force at that level: "workspace" (the
	// toggle, which covers every tier), "tier" (only the tier floor does, the
	// toggle being off), or "none".
	Source string `json:"source"`

	// TierFloorSecurityLevel / TierFloorLabel are the level at which the tier
	// table forces the rule ON ITS OWN, reported whatever the toggle says.
	//
	// Separate from the fields above because the console has to answer a
	// question they cannot: "if I turn this toggle off, what is still in
	// force?". With the toggle on, Source is "workspace" and MinSecurityLevel
	// is the bottom tier — true, and no help at all to someone about to switch
	// it off. Zero/empty when no tier forces the rule.
	TierFloorSecurityLevel int    `json:"tier_floor_security_level,omitempty"`
	TierFloorLabel         string `json:"tier_floor_label,omitempty"`
}

// resolveEffectiveSecondApprover folds the workspace toggle and the tier table
// into the one rule the resolve endpoint will actually apply.
func resolveEffectiveSecondApprover(s governance.Settings) effectiveSecondApprover {
	out := effectiveSecondApprover{Source: effectiveSourceNone}
	tierFloor, tierForces := keeper.MinSecondApproverLevel()
	if tierForces {
		out.TierFloorSecurityLevel = int(tierFloor)
		out.TierFloorLabel = tierFloor.Label()
	}

	switch {
	case s.RequireSecondApprover:
		// The toggle is tier-blind: every CREDENTIAL escalation is covered, so
		// the floor is the lowest tier there is.
		lowest := keeper.SecurityLevels()[0]
		out.MinSecurityLevel = int(lowest)
		out.MinSecurityLevelLabel = lowest.Label()
		out.Source = effectiveSourceWorkspace
	case tierForces:
		out.MinSecurityLevel = int(tierFloor)
		out.MinSecurityLevelLabel = tierFloor.Label()
		out.Source = effectiveSourceTier
	}
	return out
}

// Get handles GET /api/v1/admin/keeper/governance (ADMIN+).
func (h *KeeperGovernanceHandler) Get(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())
	s, found, err := governance.Get(r.Context(), h.db, wsID)
	if err != nil {
		h.logger.Error("keeper governance: get", "error", err)
		replyError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, keeperGovernanceResponse{
		Configured:              found,
		Settings:                s,
		EffectiveSecondApprover: resolveEffectiveSecondApprover(s),
	})
}

// keeperGovernancePutBody is a partial update: every field is a pointer, so a
// nil field is "leave unchanged" and only the fields the caller sends are
// merged onto the existing row. This lets each CLI subcommand
// (enable/disable/contact/threshold) send exactly its one field without a
// read-merge-write round-trip — which both removes the foot-gun where a stale
// read clobbers the enabled flag and makes concurrent single-field edits
// commute instead of overwriting each other.
type keeperGovernancePutBody struct {
	Enabled               *bool     `json:"enabled"`
	SecurityContactUserID *string   `json:"security_contact_user_id"`
	DenyNotifyMinRisk     *int      `json:"deny_notify_min_risk"`
	WatchSpec             *string   `json:"watch_spec"`
	WatchPresets          *[]string `json:"watch_presets"`
	RequireSecondApprover *bool     `json:"require_second_approver"`

	// AutoLeaseSeconds is the credential-lease auto-issuance TTL (#1373).
	// 0 turns auto-issuance off (the default); a positive value is clamped to
	// [governance.MinAutoLeaseSeconds, governance.MaxAutoLeaseSeconds].
	AutoLeaseSeconds *int `json:"auto_lease_seconds"`

	// Governance-model selection (M2a, #1001). Empty provider = "use the
	// server/env default". A credential ref must point at an ENDPOINT_URL /
	// API_KEY credential in this workspace.
	GovModelProvider     *string `json:"gov_model_provider"`
	GovModelID           *string `json:"gov_model_id"`
	GovModelCredentialID *string `json:"gov_model_credential_id"`
}

// Put handles PUT /api/v1/admin/keeper/governance (roleManage = OWNER/ADMIN).
// Partial-update semantics: merges the provided fields onto the current row
// (or the opt-in defaults for a first write) so a caller never has to echo
// back settings it isn't changing.
func (h *KeeperGovernanceHandler) Put(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())

	var body keeperGovernancePutBody
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// Start from the current row (or the opt-in defaults: disabled, default
	// threshold, no contact) and apply only what the caller sent.
	cur, _, err := governance.Get(r.Context(), h.db, wsID)
	if err != nil {
		h.logger.Error("keeper governance: load current", "error", err)
		replyError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if body.Enabled != nil {
		cur.Enabled = *body.Enabled
	}
	if body.DenyNotifyMinRisk != nil {
		if *body.DenyNotifyMinRisk < 1 || *body.DenyNotifyMinRisk > 10 {
			replyError(w, http.StatusBadRequest, "deny_notify_min_risk must be between 1 and 10")
			return
		}
		cur.DenyNotifyMinRisk = *body.DenyNotifyMinRisk
	}
	if body.SecurityContactUserID != nil {
		cur.SecurityContactUserID = *body.SecurityContactUserID
	}
	if body.WatchSpec != nil {
		// Cap server-side so the DB row and the compiled prompt block stay
		// bounded regardless of what the CLI/UI send.
		if len(*body.WatchSpec) > governance.MaxWatchSpecLen {
			replyError(w, http.StatusBadRequest, "watch_spec exceeds the maximum length")
			return
		}
		cur.WatchSpec = *body.WatchSpec
	}
	if body.WatchPresets != nil {
		if err := governance.ValidatePresets(*body.WatchPresets); err != nil {
			replyError(w, http.StatusBadRequest, err.Error())
			return
		}
		cur.WatchPresets = *body.WatchPresets
	}
	if body.RequireSecondApprover != nil {
		cur.RequireSecondApprover = *body.RequireSecondApprover
	}
	if body.AutoLeaseSeconds != nil {
		// Reject rather than silently clamp on the API boundary: an operator who
		// asks for a 10-second lease has misunderstood the control, and returning
		// 200 with a quietly-rewritten 60s would hide that. governance.Upsert still
		// clamps as a last-resort invariant for non-HTTP writers.
		v := *body.AutoLeaseSeconds
		if v < 0 {
			replyError(w, http.StatusBadRequest, "auto_lease_seconds must not be negative")
			return
		}
		if v > 0 && v < governance.MinAutoLeaseSeconds {
			replyError(w, http.StatusBadRequest,
				"auto_lease_seconds must be 0 (off) or at least 60 — a shorter lease can lapse inside the gatekeeper's own evaluation")
			return
		}
		if v > governance.MaxAutoLeaseSeconds {
			replyError(w, http.StatusBadRequest, "auto_lease_seconds exceeds the maximum lease of 30 days")
			return
		}
		cur.AutoLeaseSeconds = v
	}
	if body.GovModelProvider != nil {
		// Empty is allowed and means "use the server/env default". A non-empty
		// value must be one the resolver actually understands — validate against
		// the same set governance.ResolveGovModel trusts so the two can't drift.
		if *body.GovModelProvider != "" && !governance.KnownGovProvider(*body.GovModelProvider) {
			replyError(w, http.StatusBadRequest, "gov_model_provider must be one of: ollama, anthropic, openai_compat")
			return
		}
		cur.GovModelProvider = *body.GovModelProvider
	}
	if body.GovModelID != nil {
		if len(*body.GovModelID) > governance.MaxGovModelIDLen {
			replyError(w, http.StatusBadRequest, "gov_model_id exceeds the maximum length")
			return
		}
		cur.GovModelID = *body.GovModelID
	}
	if body.GovModelCredentialID != nil {
		cur.GovModelCredentialID = *body.GovModelCredentialID
	}

	// Coherence: a provider needs a model, and a credential without a provider
	// is meaningless. Enforce on the MERGED row so a partial update that leaves
	// the config half-set is rejected rather than silently producing a broken
	// (and then degraded) evaluator.
	if cur.GovModelProvider != "" && cur.GovModelID == "" {
		replyError(w, http.StatusBadRequest, "gov_model_id is required when gov_model_provider is set")
		return
	}
	if cur.GovModelCredentialID != "" && cur.GovModelProvider == "" {
		replyError(w, http.StatusBadRequest, "gov_model_provider is required when gov_model_credential_id is set")
		return
	}

	// The governance-model credential must be a usable ENDPOINT_URL / API_KEY
	// credential in this workspace. Validate ONLY when this request sets it
	// (body.GovModelCredentialID != nil and non-empty) — an unrelated partial
	// update must not be blocked because a previously-stored credential was
	// since revoked (the resolver's revoke-safety handles that at build time).
	if body.GovModelCredentialID != nil && cur.GovModelCredentialID != "" {
		var credType string
		err := h.db.QueryRowContext(r.Context(), `
			SELECT type FROM credentials
			WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
			cur.GovModelCredentialID, wsID).Scan(&credType)
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusBadRequest, "gov_model_credential_id is not a credential in this workspace")
			return
		}
		if err != nil {
			h.logger.Error("keeper governance: gov model credential lookup", "error", err)
			replyError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if credType != governance.CredTypeEndpointURL && credType != governance.CredTypeAPIKey {
			replyError(w, http.StatusBadRequest, "gov_model_credential_id must reference an ENDPOINT_URL or API_KEY credential")
			return
		}
	}

	// The security contact must be an OWNER/ADMIN member of this workspace —
	// snitching to someone who can't act on (or shouldn't see) escalations
	// is a config foot-gun, so reject it at write time. Validate ONLY when this
	// request is setting/changing the contact (body.SecurityContactUserID != nil):
	// an unrelated partial update (e.g. watch_spec/presets) must not be blocked
	// because a previously-stored contact was demoted since it was last validated.
	if body.SecurityContactUserID != nil && cur.SecurityContactUserID != "" {
		var role string
		err := h.db.QueryRowContext(r.Context(), `
			SELECT role FROM workspace_members
			WHERE workspace_id = ? AND user_id = ?`,
			wsID, cur.SecurityContactUserID).Scan(&role)
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusBadRequest, "security contact is not a member of this workspace")
			return
		}
		if err != nil {
			h.logger.Error("keeper governance: contact lookup", "error", err)
			replyError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if role != "OWNER" && role != "ADMIN" {
			replyError(w, http.StatusBadRequest, "security contact must have OWNER or ADMIN role")
			return
		}
	}

	actor := ""
	if u := UserFromContext(r.Context()); u != nil {
		actor = u.ID
	}

	s := cur
	if err := governance.Upsert(r.Context(), h.db, wsID, s, actor); err != nil {
		h.logger.Error("keeper governance: upsert", "error", err)
		replyError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Governance flips are exactly the kind of event the audit trail is for:
	// who turned the watchdog on/off and where findings are routed.
	if _, jerr := h.journal.Emit(r.Context(), journal.Entry{
		WorkspaceID: wsID,
		Type:        journal.EntryKeeperDecision,
		Severity:    journal.SeverityNotice,
		ActorType:   journal.ActorUser,
		ActorID:     actor,
		Summary:     "keeper watchdog governance updated",
		Payload: map[string]any{
			"enabled":                  s.Enabled,
			"security_contact_user_id": s.SecurityContactUserID,
			"deny_notify_min_risk":     s.DenyNotifyMinRisk,
			// Log the shape of the watch spec, not its text — the full rules
			// can be large and needn't bloat every audit entry.
			"watch_preset_count":      len(s.WatchPresets),
			"watch_spec_len":          len(s.WatchSpec),
			"require_second_approver": s.RequireSecondApprover,
			"auto_lease_seconds":      s.AutoLeaseSeconds,
			// Governance-model selection: log the provider/model + whether a
			// vault credential backs it (never the credential value).
			"gov_model_provider": s.GovModelProvider,
			"gov_model_id":       s.GovModelID,
			"gov_model_has_cred": s.GovModelCredentialID != "",
		},
	}); jerr != nil {
		h.logger.Warn("keeper governance: journal emit failed", "error", jerr)
	}

	// Warn (do NOT block) when enabling the four-eyes rule on a workspace that
	// can't satisfy it: with fewer than two members who can resolve escalations
	// (OWNER/ADMIN/MANAGER), a credential raised via the only eligible member's
	// agent can never be approved by a different person — the rule would deadlock.
	// The operator may be mid-setup (about to invite a second admin), so this is
	// advisory, not a 4xx.
	warning := ""
	if body.RequireSecondApprover != nil && *body.RequireSecondApprover {
		var eligible int
		if err := h.db.QueryRowContext(r.Context(), `
			SELECT COUNT(*) FROM workspace_members
			WHERE workspace_id = ? AND role IN ('OWNER','ADMIN','MANAGER')`,
			wsID).Scan(&eligible); err != nil {
			h.logger.Warn("keeper governance: eligible-approver count failed", "error", err)
		} else if eligible < 2 {
			warning = "second-approver is enabled, but this workspace has fewer than 2 members who can approve escalations (OWNER/ADMIN/MANAGER). A credential raised via the only eligible member's agent cannot be resolved by anyone else — add another OWNER/ADMIN/MANAGER."
			h.logger.Warn("keeper governance: second-approver enabled with <2 eligible approvers",
				"workspace_id", wsID, "eligible", eligible)
		}
	}

	writeJSON(w, http.StatusOK, keeperGovernanceResponse{
		Configured:              true,
		Settings:                s,
		EffectiveSecondApprover: resolveEffectiveSecondApprover(s),
		Warning:                 warning,
	})
}
