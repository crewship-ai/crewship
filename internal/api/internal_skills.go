package api

// Internal sidecar route for LLM-driven skill generation
// (PRD-SLASH-CAPABILITIES-2026 §6.4).
//
// Mirrors internal_routines.go in shape. The internal entry takes
// workspace_id from the query (per the sidecar proxyToAPI convention)
// and puts it in the request CONTEXT, which is where
// SkillGenerateHandler.Generate reads it from.
//
// It used to read r.PathValue("workspaceId") instead, and this adapter
// stamped the value onto the path to suit it. That read was the
// path/query divergence hole: on the public route the middleware
// validates membership against ?workspace_id= while the path can name
// a different tenant (see the comment on Generate). The context value
// is now the only one that carries the workspace here — the
// SetPathValue below is kept for any middleware that logs the path
// variable, and is NOT what makes this route work.
//
// Same MANAGER role injection as internal_hire.go / internal_routines.go:
// the public Generate handler runs requireRole("create"); injecting
// MANAGER clears that pre-existing gate without making the sidecar
// claim more than it needs. The CAPABILITY gate added in commit 6 is
// the slash-action security boundary.

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/crewship-ai/crewship/internal/llm"
	"net/http"
	"os"
	"strings"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/policy"
	"github.com/crewship-ai/crewship/internal/skills"
)

// SkillInternalAdapter wraps SkillGenerateHandler.Generate so the
// internal /api/v1/internal/skills/generate route can dispatch into
// it with workspace + role context injected from query params.
//
// It also holds the SkillProposedHandler, because the #1768 held arm has to
// land the generated document in the crew's .proposed review queue rather
// than in the live registry — see Generate.
type SkillInternalAdapter struct {
	gen    *SkillGenerateHandler
	prop   *SkillProposedHandler
	policy *policy.Resolver
}

// NewSkillInternalAdapter constructs the adapter at router-wiring
// time so it reuses the public SkillGenerateHandler instance
// (shared *sql.DB, shared logger, no duplicate state).
func NewSkillInternalAdapter(gen *SkillGenerateHandler) *SkillInternalAdapter {
	return &SkillInternalAdapter{gen: gen}
}

// SetAutonomyGate wires the per-crew autonomy resolver and the proposed-skill
// handler the held arm stages through (#1768). A nil proposed handler leaves
// the adapter unable to stage; see the fail-closed branch in Generate.
func (h *SkillInternalAdapter) SetAutonomyGate(r *policy.Resolver, prop *SkillProposedHandler) {
	h.policy = r
	h.prop = prop
}

// Generate reads workspace_id from the query, sets it as the
// {workspaceId} path value the public handler expects, injects
// MANAGER role into the context, then calls the public Generate
// path.
//
// Dual-path: when X-Caller-User-Id is present (user-initiated
// slash command from chat-bridge / CLI repl), gates on
// skill.create capability before the LLM bill fires.
//
// #1768 — the autonomous-agent path used to "fall through" here on the claim
// that "the autonomy gate runs upstream before this surface is hit". Nothing
// ran it, so an agent could write into the workspace's live skills registry
// at any autonomy level. The gate is now here, on
// policy.ActionSkillCreate:
//
//	strict/guided/trusted → the document is generated but STAGED under the
//	                        crew's .proposed directory with the same blocking
//	                        review item `skills/author` uses. Nothing reaches
//	                        the registry, and nothing reaches an agent's
//	                        prompt (injection reads agent_skills, which only
//	                        an operator-driven assignment writes) until
//	                        `skills proposed approve` promotes it.
//	full                  → today's behaviour: the row lands in the registry,
//	                        with a non-blocking inbox notice.
//
// Generating and REGISTERING are deliberately separated: the LLM spend
// produces a proposal, which is the same bargain `skills/author` already
// makes (the agent spends its own tokens writing a SKILL.md that a human then
// approves). What the gate holds is registration.
func (h *SkillInternalAdapter) Generate(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.gen == nil {
		replyError(w, http.StatusInternalServerError, "skill adapter not configured")
		return
	}
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		replyError(w, http.StatusBadRequest, "workspace_id required")
		return
	}

	callerID := CallerUserIDFromRequest(r)
	if callerID != "" {
		if !requireCapabilityOrForbid(w, r, h.gen.logger, h.gen.db,
			wsID, callerID,
			CapabilitySkillCreate, "skill.create", "skill:new") {
			return
		}
	}

	// The sidecar's /skills/generate does not forward a crew_id (unlike
	// /skills/author), so the crew comes from the token binding — which is
	// the authoritative source anyway.
	crewID := autonomySubjectCrew(r, r.URL.Query().Get("crew_id"))
	gate, ok := gateInternalAction(w, r, h.policy, h.gen.logger, crewID,
		policy.ActionSkillCreate, "Skill generation")
	if !ok {
		return
	}

	// Cosmetic: the internal route has no {workspaceId} pattern, so
	// anything that reads the path variable (request logging) sees the
	// same tenant the context carries. Generate does NOT read this —
	// deleting this line changes nothing, deleting the ctxWorkspaceID
	// value below breaks the route.
	r.SetPathValue("workspaceId", wsID)

	// This is the load-bearing line: Generate resolves the workspace
	// from the context. Pinned by
	// TestSkillAdapter_Internal_CarriesWorkspaceInContext.
	ctx := context.WithValue(r.Context(), ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxRole, "MANAGER")
	if callerID != "" {
		ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: callerID, Email: "x-internal"})
	}
	r = r.WithContext(ctx)

	if !gate.held() {
		if !gate.wantsInbox() {
			h.gen.Generate(w, r)
			return
		}
		// Capture so the notice can key on the created skill's id. The inbox
		// writer dedups on (kind, source_id), so a fixed key would silently
		// swallow the notice for every generation after the first.
		rec := newCapturedResponse()
		h.gen.Generate(rec, r)
		if rec.status == http.StatusCreated {
			var created struct {
				SkillID string `json:"skill_id"`
				Slug    string `json:"slug"`
			}
			if err := json.Unmarshal(rec.body.Bytes(), &created); err == nil && created.SkillID != "" {
				writeAutonomyNotice(r.Context(), h.gen.db, h.gen.logger, gate, wsID,
					inbox.KindMessage, created.SkillID,
					"Skill generated by agent: "+created.Slug,
					"An agent generated the skill `"+created.Slug+"` into the workspace registry at "+
						"`autonomy_level="+string(gate.Level)+"`.")
			}
		}
		rec.flush(w)
		return
	}
	h.generateStaged(w, r, crewID, gate)
}

// generateStaged is the held arm: generate the SKILL.md, then stage it for
// review instead of registering it.
func (h *SkillInternalAdapter) generateStaged(w http.ResponseWriter, r *http.Request, crewID string, gate autonomyDecision) {
	// Read the workspace from the CONTEXT, not the query, for the same
	// reason SkillGenerateHandler.Generate does: the context value is the
	// one RequireWorkspace validates membership against, and it is the
	// adapter's ctxWorkspaceID injection above that puts it there. Reading
	// the query here would quietly make that injection non-load-bearing on
	// this arm (pinned by TestSkillAdapter_Internal_CarriesWorkspaceInContext).
	wsID := WorkspaceIDFromContext(r.Context())
	if wsID == "" {
		writeProblem(w, r, http.StatusBadRequest, "workspace_id is required")
		return
	}

	// Request-shape validation first, matching Generate's order, so a
	// malformed body is a 400 on both arms rather than a policy 403 on one
	// of them.
	var body skillGenerateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid JSON body")
		return
	}
	body.Slug = strings.TrimSpace(body.Slug)
	body.Prompt = strings.TrimSpace(body.Prompt)
	if body.Slug == "" || body.Prompt == "" {
		writeProblem(w, r, http.StatusBadRequest, "slug and prompt are required")
		return
	}
	body.Slug = skills.Slugify(body.Slug)
	model := body.Model
	if model == "" {
		model = llm.DefaultModel("anthropic")
	}

	if h.prop == nil || crewID == "" {
		// No staging destination — either the router did not wire the
		// proposed handler, or the caller holds a workspace-bound (crew-less)
		// token so there is no .proposed directory to stage into. Refuse
		// rather than fall back to a registry write, which is precisely the
		// ungated behaviour this gate exists to remove.
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":          "Skill generation rejected by policy",
			"reason":         "autonomy_level=" + string(gate.Level) + " requires the generated skill to be staged for review, and this caller has no crew to stage into",
			"autonomy_level": string(gate.Level),
			"remedy":         "call /skills/author from a crew-bound sidecar, or raise the crew's autonomy_level",
		})
		return
	}

	raw, ok := h.gen.produceSkillMD(w, r, wsID, body.Slug, body.Prompt, model)
	if !ok {
		return
	}

	fileName, slug, scanStatus, scanReason, err := h.prop.stageProposedSkill(
		r.Context(), wsID, crewID, raw, "generated")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, errStorageNotConfigured) {
			h.prop.mapDirError(w, err)
			return
		}
		writeProblem(w, r, http.StatusBadGateway, "generated SKILL.md could not be staged: "+err.Error())
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"file_name":      fileName,
		"slug":           slug,
		"scan_status":    scanStatus,
		"scan_reason":    scanReason,
		"decision":       string(gate.Decision),
		"autonomy_level": string(gate.Level),
		"pending_review": true,
	})
}
