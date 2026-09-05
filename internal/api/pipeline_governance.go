package api

// Routine governance: the maker-checker gate over routine authoring plus the
// admin disable/enable airbag. Mirrors the SKILLS proposed-review flow
// (skills_proposed_handler.go): risky agent/user-authored routines land as
// status='proposed' with a MANAGER+ inbox item; approve flips them live and
// resolves the item; an OWNER/ADMIN can disable a live routine (cancelling
// any in-flight runs) and re-enable it later.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

// routineProposalInboxSource is the (kind, source_id) dedup key tying a
// proposed routine to its inbox review item. The save path inserts it;
// approve/reject resolve it. Keyed on workspace + slug so re-proposing the
// same routine is idempotent (INSERT OR IGNORE on this key) and approve/reject
// can resolve it without re-reading the row.
func routineProposalInboxSource(workspaceID, slug string) string {
	return "routineprop:" + workspaceID + ":" + slug
}

// routineThreadKey is the §12 thread_key shared by EVERY inbox item that
// asks a human to decide something about routine <slug> — the governance
// risk review (this file) and the B8 trigger-activation review
// (pipeline_trigger.go) both use it, so inbox.WriteThreaded collapses them
// into one card when a single `routine save --draft` raises both (B10,
// #2364 — the live-observed duplicate: "Routine proposed for review" +
// "Routine trigger ready" for the same save). Scoped to workspace+slug, not
// version: a routine's identity across versions is its slug, and the
// version that changed is exactly the fact routine_version in the merged
// payload answers.
func routineThreadKey(workspaceID, slug string) string {
	return "routine:" + workspaceID + ":" + slug
}

// classifyRoutineRisk decides whether a routine save must land as 'proposed'
// (human review) or may go live as 'active'. Risky when the DSL declares any
// http/egress step, any code-runtime step, any credentials_required, OR an
// integrations_required the author crew can't currently satisfy. Returns the
// risk reasons for the audit / inbox payload.
//
// The integration factor reuses the W0 resolver (resolveCrewIntegrations) and
// is FAIL-OPEN exactly like the run gate: if there's no author crew, no DB, or
// the resolver errors, we don't treat the integration as a risk factor (a
// resolver hiccup must not wedge every save into the review queue). The static
// factors still apply.
func (h *PipelineHandler) classifyRoutineRisk(ctx context.Context, workspaceID, crewID string, dsl *pipeline.DSL) (bool, []string) {
	reasons := dsl.StaticRiskReasons()
	reasons = append(reasons, h.unmetIntegrationReasons(ctx, workspaceID, crewID, dsl)...)
	return len(reasons) > 0, reasons
}

// unmetIntegrationReasons returns a RiskUnmetIntegration:<slug> reason for each
// declared integration the author crew hasn't connected. Fail-open on missing
// crew/db/resolver-error and under the default-connector wildcard.
func (h *PipelineHandler) unmetIntegrationReasons(ctx context.Context, workspaceID, crewID string, dsl *pipeline.DSL) []string {
	required := dsl.NormalizedIntegrationsRequired()
	if len(required) == 0 || h.db == nil || crewID == "" {
		return nil
	}
	available, err := resolveCrewIntegrations(ctx, h.db, workspaceID, crewID)
	if err != nil {
		h.logger.Warn("routine risk: integration resolve failed, treating as satisfiable (fail-open)",
			"workspace_id", workspaceID, "crew_id", crewID, "error", err)
		return nil
	}
	if available[crewIntegrationsWildcard] {
		return nil
	}
	var out []string
	for _, want := range required {
		if !available[want] {
			out = append(out, pipeline.RiskUnmetIntegration+":"+want)
		}
	}
	return out
}

// statusForRisk maps the risk verdict onto a persisted status.
func statusForRisk(risky bool) string {
	if risky {
		return "proposed"
	}
	return "active"
}

// proposeRoutineInbox raises the MANAGER+ inbox review item for a routine that
// landed as 'proposed', then nudges the workspace to refresh its inbox. Mirrors
// the skills author flow: KindEscalation, blocking, high priority. Best-effort
// — a projection failure must not fail the save (the proposed row is
// authoritative).
func (h *PipelineHandler) proposeRoutineInbox(ctx context.Context, workspaceID string, saved *pipeline.Pipeline, reasons []string, senderName string) {
	// Which two versions a reviewer should compare.
	//
	// The routine already keeps immutable versions and the API already
	// serves a unified diff of any two; the payload just never carried
	// the numbers, so the inbox could show a slug and a reason and
	// nothing about what actually changed.
	//
	// from is omitted for v1: there is no predecessor, and emitting 0
	// would have the inbox request a diff against a version that never
	// existed.
	payload := map[string]interface{}{
		"kind":           "routine_proposal",
		"slug":           saved.Slug,
		"pipeline_id":    saved.ID,
		"author_crew_id": saved.AuthorCrewID,
		"risk_reasons":   reasons,
	}
	if head, err := h.store.HeadVersion(ctx, saved.ID); err == nil && head > 0 {
		payload["to_version"] = head
		if head > 1 {
			payload["from_version"] = head - 1
		}
	}
	// WHAT it is asking for, not just that it asks.
	//
	// "requires credentials" is a category. The question a reviewer
	// actually has is which ones, and the routine declares them — so
	// leaving them off the item meant the only way to answer it was to
	// leave the inbox, find the routine and read its DSL. Most people
	// pressed Approve instead.
	//
	// Best-effort: the proposal is the authoritative record that a human
	// must rule on this, and failing to decorate it must not stop it
	// being raised.
	for k, v := range routineAsks(saved.DefinitionJSON) {
		payload[k] = v
	}

	// WriteThreaded, not Upsert: the source id is the slug — deliberately
	// stable, so a retried save does not pile up siblings — and Insert's
	// INSERT OR IGNORE turned that stability into silence: once a routine
	// had been through review the row existed forever, so every LATER
	// proposal was dropped. The routine sat at 'proposed', unable to run,
	// with nothing in anyone's inbox to approve it. Refreshing the row
	// instead asks the question again, about the change actually on the
	// table.
	//
	// ThreadKey is routineThreadKey, shared with proposeTriggerActivationInbox
	// (pipeline_trigger.go): when ONE save is both risky (this card) and
	// carries a draft trigger (that card), WriteThreaded merges them into a
	// single card rather than raising two (B10, #2364).
	_ = inbox.WriteThreaded(ctx, h.db, h.logger, inbox.Item{
		WorkspaceID: workspaceID,
		Kind:        inbox.KindEscalation,
		SourceID:    routineProposalInboxSource(workspaceID, saved.Slug),
		TargetRole:  "MANAGER",
		Title:       "Routine proposed for review: " + saved.Slug,
		BodyMD: "A routine was authored that needs approval before it can run (reasons: " +
			strings.Join(reasons, ", ") + "). Approve it to activate the routine, or reject it.",
		SenderType:        "pipeline",
		SenderName:        senderName,
		Priority:          "high",
		Blocking:          true,
		Payload:           payload,
		ThreadKey:         routineThreadKey(workspaceID, saved.Slug),
		AccumulateOnMerge: true,
		AttentionClass:    inbox.AttentionDecision,
		Actions: []inbox.Action{
			{ID: "approve_routine", Label: "Approve", Effect: "Activates the routine", Irreversible: false},
			{ID: "reject_routine", Label: "Reject", Effect: "Discards the proposed routine", Irreversible: true},
		},
	})
	h.broadcastInboxUpdated(workspaceID, "routine_proposed")
}

// riskReasonsForRoutine reads back WHY a routine was sent for review.
//
// The classifier produces these at save time and proposeRoutineInbox
// writes them into the inbox item's payload. Nothing read them back, so
// a reviewer got a banner saying "awaiting approval" and no indication
// of what they were being asked to judge.
//
// Read from the inbox rather than stored a second time on the routine:
// a reason shown on the routine and a reason shown in the inbox could
// then disagree, and the inbox item is the thing a MANAGER actually
// acts on. Best-effort — this decorates a response, and failing to
// explain a proposal must not fail loading the routine.
func (h *PipelineHandler) riskReasonsForRoutine(ctx context.Context, workspaceID, slug string) []string {
	if h.db == nil {
		return nil
	}
	var payload sql.NullString
	err := h.db.QueryRowContext(ctx,
		`SELECT payload_json FROM inbox_items
         WHERE workspace_id = ? AND source_id = ? AND state != 'resolved'
         ORDER BY created_at DESC LIMIT 1`,
		workspaceID, routineProposalInboxSource(workspaceID, slug),
	).Scan(&payload)
	if err != nil || !payload.Valid {
		return nil
	}
	var decoded struct {
		RiskReasons []string `json:"risk_reasons"`
	}
	if err := json.Unmarshal([]byte(payload.String), &decoded); err != nil {
		return nil
	}
	return decoded.RiskReasons
}

// inboxItemForRoutine returns the id of the OPEN review row for a
// routine, or "" when there is none.
//
// The banner on a proposed routine offers to open "the review item in
// Inbox" and used to land on the inbox root, leaving the reader to find
// the row. Deep-linking needs the row id, and that id is built inside
// the inbox writer from the (kind, source_id) pair — a client that
// reconstructed it would be a second copy of that rule, silently wrong
// the day the first one changes.
//
// Resolved rows are excluded on purpose: pointing at a decided review
// invites a second decision on something already ruled on.
func (h *PipelineHandler) inboxItemForRoutine(ctx context.Context, workspaceID, slug string) string {
	if h.db == nil {
		return ""
	}
	var id string
	err := h.db.QueryRowContext(ctx,
		`SELECT id FROM inbox_items
         WHERE workspace_id = ? AND source_id = ? AND state != 'resolved'
         ORDER BY created_at DESC LIMIT 1`,
		workspaceID, routineProposalInboxSource(workspaceID, slug),
	).Scan(&id)
	if err != nil {
		return ""
	}
	return id
}

// broadcastInboxUpdated pushes the same inbox.updated event the inbox handler
// uses so any subscribed client repaints its inbox. No-op when the WS
// broadcaster isn't wired (tests / headless boot).
func (h *PipelineHandler) broadcastInboxUpdated(workspaceID, reason string) {
	if h.ws == nil {
		return
	}
	h.ws.BroadcastWorkspace(workspaceID, "inbox.updated", map[string]string{"reason": reason})
}

// broadcastRoutinesChanged tells every client in the workspace that the
// routine catalog moved: something was saved, approved, rejected,
// disabled, enabled or deleted.
//
// The overview had a Refresh button, which is a dashboard admitting it
// is not live. Run events already pushed themselves; the catalog did
// not, so a routine authored by an agent — or approved in another tab —
// sat invisible until someone pressed it. A page that shows a queue has
// to be told when the queue changes.
func (h *PipelineHandler) broadcastRoutinesChanged(workspaceID, reason string) {
	if h.ws == nil {
		return
	}
	h.ws.BroadcastWorkspace(workspaceID, "pipeline.saved", map[string]string{"reason": reason})
}

// gateRoutineStatus blocks a run whose routine isn't 'active'. Returns true
// (having written the response) when the run must be refused. Placed alongside
// the W0 integration gate in Run. dry_run is intentionally NOT gated (preview
// is always allowed); test_run executes an unsaved draft so has no status.
func (h *PipelineHandler) gateRoutineStatus(w http.ResponseWriter, p *pipeline.Pipeline) bool {
	switch p.Status {
	case "", "active":
		return false
	case "proposed":
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "routine is awaiting approval",
			"status": "proposed",
			"hint":   "a MANAGER must approve this routine before it can run",
		})
		return true
	case "disabled":
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "routine is disabled",
			"status": "disabled",
			"hint":   "an OWNER or ADMIN must re-enable this routine before it can run",
		})
		return true
	default:
		// Unknown status — fail closed so a future state can't silently run.
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "routine is not active",
			"status": p.Status,
		})
		return true
	}
}

// Approve flips a proposed routine to active and resolves its inbox review
// item. MANAGER+ (canRole "create" — same threshold as save/import).
//
// POST /api/v1/workspaces/{ws}/pipelines/{slug}/approve
func (h *PipelineHandler) Approve(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if !canRole(RoleFromContext(r.Context()), "create") {
		replyError(w, http.StatusForbidden, "MANAGER+ role required to approve routines")
		return
	}
	slug := r.PathValue("slug")
	p, err := h.store.GetBySlug(r.Context(), workspaceID, slug)
	if errors.Is(err, pipeline.ErrNotFound) {
		replyError(w, http.StatusNotFound, "routine not found")
		return
	}
	if err != nil {
		h.logger.Error("routine approve: load", "error", err, "slug", slug)
		replyError(w, http.StatusInternalServerError, "load routine")
		return
	}
	// Approve only acts on the maker-checker queue. Without this, a MANAGER+
	// could approve a 'disabled' routine straight back to 'active', bypassing
	// the OWNER/ADMIN-only enable gate (the disable airbag).
	if p.Status != "proposed" {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "routine is not awaiting approval",
			"status": p.Status,
		})
		return
	}
	if err := h.store.SetStatus(r.Context(), p.ID, "active"); err != nil {
		if errors.Is(err, pipeline.ErrNotFound) {
			replyError(w, http.StatusNotFound, "routine not found")
			return
		}
		h.logger.Error("routine approve: set status", "error", err, "slug", slug)
		replyError(w, http.StatusInternalServerError, "approve routine")
		return
	}
	actorID := ""
	if user := UserFromContext(r.Context()); user != nil {
		actorID = user.ID
	}
	inbox.ResolveByThreadOrSource(r.Context(), h.db, h.logger, workspaceID,
		inbox.KindEscalation, routineProposalInboxSource(workspaceID, slug), routineThreadKey(workspaceID, slug), "approved", actorID)
	h.broadcastInboxUpdated(workspaceID, "routine_approved")
	h.broadcastRoutinesChanged(workspaceID, "approved")
	writeJSON(w, http.StatusOK, map[string]string{"slug": slug, "status": "active"})
}

// Reject removes a proposed routine (soft-delete, mirroring the skills reject
// which deletes the staged file) and resolves its inbox item. MANAGER+.
//
// POST /api/v1/workspaces/{ws}/pipelines/{slug}/reject
func (h *PipelineHandler) Reject(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if !canRole(RoleFromContext(r.Context()), "create") {
		replyError(w, http.StatusForbidden, "MANAGER+ role required to reject routines")
		return
	}
	slug := r.PathValue("slug")
	p, err := h.store.GetBySlug(r.Context(), workspaceID, slug)
	if errors.Is(err, pipeline.ErrNotFound) {
		replyError(w, http.StatusNotFound, "routine not found")
		return
	}
	if err != nil {
		h.logger.Error("routine reject: load", "error", err, "slug", slug)
		replyError(w, http.StatusInternalServerError, "load routine")
		return
	}
	// Reject is the maker-checker "no" — it only discards a proposal. An
	// active/disabled routine must be removed via Delete, not silently
	// soft-deleted through the review path.
	if p.Status != "proposed" {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "routine is not awaiting approval",
			"status": p.Status,
		})
		return
	}
	if err := h.store.SoftDelete(r.Context(), p.ID); err != nil && !errors.Is(err, pipeline.ErrNotFound) {
		h.logger.Error("routine reject: soft delete", "error", err, "slug", slug)
		replyError(w, http.StatusInternalServerError, "reject routine")
		return
	}
	actorID := ""
	if user := UserFromContext(r.Context()); user != nil {
		actorID = user.ID
	}
	inbox.ResolveByThreadOrSource(r.Context(), h.db, h.logger, workspaceID,
		inbox.KindEscalation, routineProposalInboxSource(workspaceID, slug), routineThreadKey(workspaceID, slug), "rejected", actorID)
	h.broadcastInboxUpdated(workspaceID, "routine_rejected")
	h.broadcastRoutinesChanged(workspaceID, "rejected")
	writeJSON(w, http.StatusOK, map[string]string{"slug": slug, "status": "rejected"})
}

// Disable is the admin airbag: flip a routine to 'disabled' and cancel any
// in-flight runs of it. OWNER/ADMIN only (canRole "manage" — same threshold as
// cancel/rollback).
//
// POST /api/v1/workspaces/{ws}/pipelines/{slug}/disable
func (h *PipelineHandler) Disable(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if !canRole(RoleFromContext(r.Context()), "manage") {
		replyError(w, http.StatusForbidden, "OWNER/ADMIN role required to disable routines")
		return
	}
	slug := r.PathValue("slug")
	p, err := h.store.GetBySlug(r.Context(), workspaceID, slug)
	if errors.Is(err, pipeline.ErrNotFound) {
		replyError(w, http.StatusNotFound, "routine not found")
		return
	}
	if err != nil {
		h.logger.Error("routine disable: load", "error", err, "slug", slug)
		replyError(w, http.StatusInternalServerError, "load routine")
		return
	}
	if err := h.store.SetStatus(r.Context(), p.ID, "disabled"); err != nil {
		if errors.Is(err, pipeline.ErrNotFound) {
			replyError(w, http.StatusNotFound, "routine not found")
			return
		}
		h.logger.Error("routine disable: set status", "error", err, "slug", slug)
		replyError(w, http.StatusInternalServerError, "disable routine")
		return
	}
	// Cancel in-flight runs of this routine so disable takes effect
	// immediately, not just for future triggers. Best-effort — a run that
	// finishes between Active() and Cancel() simply isn't found.
	cancelled := 0
	if h.runs != nil {
		for _, info := range h.runs.Active(workspaceID) {
			if info.PipelineID == p.ID || info.PipelineSlug == slug {
				if cerr := h.runs.Cancel(info.RunID); cerr == nil {
					cancelled++
				}
			}
		}
	}
	h.broadcastInboxUpdated(workspaceID, "routine_disabled")
	h.broadcastRoutinesChanged(workspaceID, "disabled")
	writeJSON(w, http.StatusOK, map[string]any{
		"slug":           slug,
		"status":         "disabled",
		"cancelled_runs": cancelled,
	})
}

// Enable lifts a disable, returning the routine to 'active'. OWNER/ADMIN only.
//
// POST /api/v1/workspaces/{ws}/pipelines/{slug}/enable
func (h *PipelineHandler) Enable(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if !canRole(RoleFromContext(r.Context()), "manage") {
		replyError(w, http.StatusForbidden, "OWNER/ADMIN role required to enable routines")
		return
	}
	slug := r.PathValue("slug")
	p, err := h.store.GetBySlug(r.Context(), workspaceID, slug)
	if errors.Is(err, pipeline.ErrNotFound) {
		replyError(w, http.StatusNotFound, "routine not found")
		return
	}
	if err != nil {
		h.logger.Error("routine enable: load", "error", err, "slug", slug)
		replyError(w, http.StatusInternalServerError, "load routine")
		return
	}
	if err := h.store.SetStatus(r.Context(), p.ID, "active"); err != nil {
		if errors.Is(err, pipeline.ErrNotFound) {
			replyError(w, http.StatusNotFound, "routine not found")
			return
		}
		h.logger.Error("routine enable: set status", "error", err, "slug", slug)
		replyError(w, http.StatusInternalServerError, "enable routine")
		return
	}
	h.broadcastRoutinesChanged(workspaceID, "enabled")
	writeJSON(w, http.StatusOK, map[string]string{"slug": slug, "status": "active"})
}

// gateInput is everything the maker-checker decision depends on.
type gateInput struct {
	// currentStatus is the routine's persisted status, or "" for a new
	// one. A brand-new routine has nothing previously approved, so the
	// subset rule below has nothing to compare against.
	currentStatus string
	// priorReasons are the risk factors of the definition as STORED —
	// the ones already reviewed and accepted, if the routine is active.
	priorReasons []string
	// newReasons are the risk factors of the definition being saved.
	newReasons []string
}

type gateResult struct {
	status string
	// why records the reason for the audit log when review is bypassed.
	// Empty when the decision needed no justification.
	why string
}

// gateDecision decides whether a save lands `active` or `proposed`.
//
// The gate used to classify ABSOLUTE risk on every save, so a routine
// declaring credentials_required was risky forever: fixing a typo in
// its description demoted an already-approved routine back to review.
// The person doing it was usually the same person who then clicked
// Approve, which is not a control — it is a ritual, and rituals teach
// people to approve without reading. A gate that fires constantly is a
// gate nobody reads.
//
// Two rules, both asking whether there is anything NEW to review:
//
//  1. An already-active routine whose risk factors are a subset of the
//     ones already accepted stays active. Nothing new appeared, so
//     there is nothing to judge. Adding a factor still goes for review.
//
// One rule, not two. The obvious companion — "someone holding the
// approval right does not file a request with themselves" — was
// considered and rejected: Approve is gated on canRole "create", the
// SAME tier as save, so every user who can save can already approve.
// That rule would therefore switch the gate off entirely on the user
// path rather than making it proportionate, which is further than the
// complaint goes and further than a governance change should reach
// without being asked for by name.
//
// So on the user path the gate stays a deliberation prompt — "you just
// added a new risk, confirm you meant it" — which is worth something
// precisely because it now fires only when something new appeared.
// Separation of duties still comes from the agent path, where the
// author holds no role at all.
//
// What is unchanged, deliberately: a NEW routine is judged on its own
// merits, and a `disabled` routine is never reactivated by a save —
// that status is an admin airbag, not a review outcome.
func gateDecision(in gateInput) gateResult {
	if len(in.newReasons) == 0 {
		if in.currentStatus == "disabled" {
			return gateResult{status: "disabled"}
		}
		return gateResult{status: "active"}
	}
	if in.currentStatus == "disabled" {
		return gateResult{status: "disabled"}
	}

	if in.currentStatus == "active" && reasonsSubset(in.newReasons, in.priorReasons) {
		return gateResult{
			status: "active",
			why:    "risk unchanged from the already-approved definition",
		}
	}
	return gateResult{status: "proposed"}
}

// reasonsSubset reports whether every factor in `next` was already in
// `prior`. Set semantics: order and duplicates are noise here, the
// question is only whether something NEW appeared.
func reasonsSubset(next, prior []string) bool {
	if len(prior) == 0 {
		return len(next) == 0
	}
	have := make(map[string]struct{}, len(prior))
	for _, r := range prior {
		have[r] = struct{}{}
	}
	for _, r := range next {
		if _, ok := have[r]; !ok {
			return false
		}
	}
	return true
}

// currentRiskProfile returns a routine's persisted status and the risk
// factors of the definition as STORED.
//
// The stored definition is what a reviewer last accepted, so its risk
// factors are the baseline a save is judged against. Classifying it
// costs one parse; the alternative is a column that records what was
// approved and then drifts from the definition it describes.
//
// Returns ("", nil) for a routine that does not exist yet — a new
// routine has no accepted baseline and must be judged on its own.
func (h *PipelineHandler) currentRiskProfile(ctx context.Context, workspaceID, slug string) (string, []string) {
	if h.store == nil {
		return "", nil
	}
	p, err := h.store.GetBySlug(ctx, workspaceID, slug)
	if err != nil || p == nil {
		return "", nil
	}
	dsl, err := pipeline.Parse([]byte(p.DefinitionJSON))
	if err != nil {
		// An unparseable stored definition means no trustworthy
		// baseline. Returning no reasons makes any risk look new, which
		// sends the save for review — the safe direction.
		return p.Status, nil
	}
	_, reasons := h.classifyRoutineRisk(ctx, workspaceID, p.AuthorCrewID, dsl)
	return p.Status, reasons
}

// routineAsks pulls the concrete declarations out of a routine's DSL so
// the review item can name them: which credentials, which integrations,
// which egress hosts.
//
// Keys are omitted rather than emitted empty. A heading with nothing
// under it reads as "we could not find out", which is a different claim
// from "it asks for none" — and on a governance card the difference
// decides whether someone goes and checks.
//
// A credential is rendered "type:scope" when it carries a scope:
// github and github:repo are different asks, and collapsing them would
// understate one of them.
func routineAsks(definitionJSON string) map[string]interface{} {
	dsl, err := pipeline.Parse([]byte(definitionJSON))
	if err != nil || dsl == nil {
		return nil
	}
	out := map[string]interface{}{}
	if len(dsl.CredsRequired) > 0 {
		creds := make([]string, 0, len(dsl.CredsRequired))
		for _, c := range dsl.CredsRequired {
			if c.Scope != "" {
				creds = append(creds, c.Type+":"+c.Scope)
				continue
			}
			creds = append(creds, c.Type)
		}
		out["credentials_required"] = creds
	}
	if ints := dsl.NormalizedIntegrationsRequired(); len(ints) > 0 {
		out["integrations_required"] = ints
	}
	if len(dsl.EgressTargets) > 0 {
		out["egress_targets"] = dsl.EgressTargets
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
