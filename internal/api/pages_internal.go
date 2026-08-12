package api

// Pages — the ROUTINE write path (docs/prd/pages.md §0, §4, §7.1 rule 4,
// §7.1b, §11; issue #1945).
//
//	PUT /api/v1/internal/pages/{page}/data
//
// This is the far side of the `page.write` crewship verb
// (internal/pipeline/crewship_step.go). §0 names it as the feature's payoff: a
// cheap script pushes a number, a threshold wakes an agent, and the agent
// writes its analysis back onto the same page. Without this route the last step
// has nowhere to land.
//
// It is the same write as PUT /api/v1/pages/{slug}/panels/{panelId}/data
// (pages_data.go) with the caller swapped, and everything that differs follows
// from that one swap:
//
//  1. THE BODY IS AN ENVELOPE, NOT THE PAYLOAD. The public route can insist the
//     body IS the payload because a JWT carries the identity. The dispatcher
//     writes identity into the body — workspace_id, crew_id, agent_id,
//     author_run_id, injected from the RUN and never from the routine author
//     (crewship_actions.go crewshipInjected) — so the payload has to be a field
//     inside it. `data` is that field, and it is the only part of this body the
//     author controls that reaches storage.
//
//  2. THE CAP IS ENFORCED HERE, EXPLICITLY. /api/v1/internal/* bypasses the
//     global BodyCap middleware (the warning repeated at crew_messaging.go:60),
//     so a route on this prefix that does not bound its own body has no bound at
//     all. §11b.6 requires the SAME 422 to arrive on this path as on the public
//     one, which pages.ValidatePayload gives for free: it is the single entry
//     point every write path uses and it owns the 64 KiB judgement.
//
//  3. AUTHORITY IS THE RUN, NOT A ROLE. There is no human here, so "is this
//     caller the page owner" has no answer. What there is instead is stronger:
//     the run resolves to the routine that is executing, and §7.1 rule 4 says
//     only the declared producer may write a panel. See mayProduceUnattended.
//
// Provenance stays server-attached (§4 rule 5) and the clock stays the server's
// (§4 rule 2): produced_at is this machine's time and producer_run_id is the
// injected run, so a routine cannot claim to have produced something earlier,
// later, or on somebody else's behalf.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pages"
)

// registerInternalPageRoutes mounts the routine write path.
//
// Called from registerInternalRoutes so every /api/v1/internal/* route is
// wired in one pass and inherits internalAuth (the X-Internal-Token fence)
// without a second opinion about what guards this prefix.
//
// A second PageHandler instance rather than the one router_pages.go built: the
// handler carries no registry, no cache and no pending map, so the "one
// instance or they drift" rule that binds AssignmentHandler and
// PortExposeHandler does not bind this one. Threading it through the internal
// registration signature would couple two route groups for no property either
// of them has.
//
// The one piece of state it does hold is §10b.3's push-rate buckets, and they
// are per-instance on purpose: they are per-PROCESS anyway (config/
// rate-limits.yml's own header), so sharing them between two route groups
// would buy a guarantee that a second replica takes straight back. What holds
// across both doors, and across replicas, is the floor in the push
// transaction — see pages_data.go.
func (r *Router) registerInternalPageRoutes(internalAuth func(http.Handler) http.Handler) {
	p := NewPageHandler(r.db, r.hub, r.logger).SetJournal(r.Journal())
	r.mux.Handle("PUT /api/v1/internal/pages/{page}/data",
		internalAuth(http.HandlerFunc(p.PushDataInternal)))
}

// internalPagePushEnvelopeSlack is the headroom the ENVELOPE gets on top of the
// payload cap.
//
// The number the caller is owed is 64 KiB of payload (pages.MaxPayloadBytes),
// and that judgement is made by pages.ValidatePayload on `data` alone, so the
// 422 it produces is byte-identical to the public route's. This constant bounds
// something different: the identity fields, the panel id and whatever else the
// routine author put in `args`, none of which is payload. It exists because the
// read itself has to be bounded — BodyCap does not run on this prefix — and
// capping the read at exactly MaxPayloadBytes would refuse a LEGAL 64 KiB
// payload for the crime of travelling with its own workspace id.
//
// 8 KiB is roughly forty of the longest ids this system mints and is not a
// budget anybody should be authoring against; a body that needs more than that
// around its payload is refused with the envelope limit named.
const internalPagePushEnvelopeSlack = 8 << 10

// internalPagePush is the dispatcher's envelope. Unknown fields are TOLERATED
// on purpose: crewshipBody merges the routine author's whole args map with six
// injected identity fields, so a body arriving here legitimately carries names
// this struct has never heard of. Rejecting them would refuse a valid push
// because the author left a comment-shaped arg lying around.
type internalPagePush struct {
	// Injected by the dispatcher from the run — never author-supplied.
	WorkspaceID string `json:"workspace_id"`
	CrewID      string `json:"crew_id"`
	AgentID     string `json:"agent_id"`
	AuthorRunID string `json:"author_run_id"`

	// Authored.
	Panel string `json:"panel"`
	// Data is held raw. Decoding and re-encoding it would rewrite the bytes the
	// schema is supposed to judge, and `{"value": 1.0}` re-encoding as
	// `{"value":1}` is exactly the kind of quiet difference a payload contract
	// exists to notice.
	Data json.RawMessage `json:"data"`
	// State is the producer's own verdict, "ok" or "failed" (§4 rule 2). Absent
	// means ok: a producer that ran and said nothing about itself worked.
	State string `json:"state"`
}

// PushDataInternal stores one payload for one panel on behalf of a routine.
//
// PUT /api/v1/internal/pages/{page}/data
func (h *PageHandler) PushDataInternal(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSpace(r.PathValue("page"))
	if slug == "" {
		replyError(w, http.StatusBadRequest, "the page slug is required")
		return
	}

	// The cap FIRST, before anything is parsed. /api/v1/internal/* bypasses
	// BodyCap, so this is the only thing standing between an unbounded body and
	// this process's memory.
	//
	// The public route reads the body AFTER its permission check, so a caller who
	// may not write the panel never gets to spend the server's memory on 64 KiB.
	// That ordering is not available here and the reason is structural rather
	// than an oversight: on this path the workspace, the crew, the run and the
	// panel are all IN the body, so there is nothing to check until it has been
	// read. The bound is what makes that safe — the most an unauthorised caller
	// can spend is one envelope.
	raw, ok := readCapped(w, r, pages.MaxPayloadBytes+internalPagePushEnvelopeSlack, "page push envelope")
	if !ok {
		return
	}
	var req internalPagePush
	if err := json.Unmarshal(raw, &req); err != nil {
		replyError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.WorkspaceID = strings.TrimSpace(req.WorkspaceID)
	req.Panel = strings.TrimSpace(req.Panel)
	if req.WorkspaceID == "" {
		replyError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}
	if req.Panel == "" {
		replyError(w, http.StatusBadRequest, "panel is required — it names which panel of the page to write")
		return
	}
	if len(strings.TrimSpace(string(req.Data))) == 0 || string(req.Data) == "null" {
		replyError(w, http.StatusBadRequest,
			"data is required and carries the panel's payload as JSON")
		return
	}

	// The two tenancy fences every internal write runs: the body's workspace
	// against the token's binding, and the crew against the workspace it claims
	// (missions_internal.go, issues_internal.go). Both are documented no-ops for
	// a master-token caller, which is why crewshipActions.fenceTenancy proves the
	// same facts on its own side before dispatching — this is the half that
	// holds for a crew-bound token.
	if !assertInternalTokenWorkspace(w, r, req.WorkspaceID) {
		return
	}
	if !assertBoundCrewWorkspaceDB(w, r, h.db, h.logger, &req.CrewID) {
		return
	}

	rec, err := h.loadPage(r.Context(), req.WorkspaceID, slug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, fmt.Sprintf("page %q not found", slug))
			return
		}
		replyInternalError(w, h.logger, "load page for routine push", err)
		return
	}
	panels, err := h.loadPanels(r.Context(), req.WorkspaceID, rec.ID)
	if err != nil {
		replyInternalError(w, h.logger, "load panels for routine push", err)
		return
	}
	var panel *panelRecord
	for _, p := range panels {
		if p.PanelID == req.Panel {
			panel = p
			break
		}
	}
	if panel == nil {
		replyError(w, http.StatusNotFound,
			fmt.Sprintf("page %q has no panel %q", slug, req.Panel))
		return
	}

	// §7.1 rule 4 / §7.1b rule 3.
	allowed, reason, err := h.mayProduceUnattended(r.Context(), req, rec, panel)
	if err != nil {
		// A permission check that could not read its own state has not
		// established anything. Fail closed, and say so as a server error rather
		// than as a 403 the operator would read as "the grant is wrong".
		replyInternalError(w, h.logger, "resolve routine produce authority", err)
		return
	}
	if !allowed {
		h.reportUnauthorisedRoutinePush(r, req, rec, panel, reason)
		replyError(w, http.StatusForbidden, reason)
		return
	}

	// §11b.6 — the same 422, from the same function, as the public path.
	if _, err := pages.ValidatePayload(pages.PanelSchema(panel.Schema), req.Data); err != nil {
		var ve *pages.ValidationError
		if errors.As(err, &ve) {
			if ve.Code == pages.CodeTooLarge {
				writeRejection(w, pageRejection{
					Kind:    "cap",
					Message: ve.Detail,
					Detail:  map[string]any{"bytes_attempted": len(req.Data), "bytes_limit": pages.MaxPayloadBytes},
				})
				return
			}
			replyError(w, http.StatusBadRequest,
				fmt.Sprintf("payload does not satisfy %s: %s", panel.Schema, ve.Detail))
			return
		}
		replyError(w, http.StatusBadRequest, err.Error())
		return
	}

	push := string(pages.PushOK)
	if s := strings.TrimSpace(req.State); s != "" {
		if s != string(pages.PushOK) && s != string(pages.PushFailed) {
			replyError(w, http.StatusBadRequest,
				`state must be "ok" or "failed"; fresh and stale are the server's arithmetic, not a producer's claim`)
			return
		}
		push = s
	}

	// Provenance: the server's clock and the RUN the dispatcher injected (§4
	// rules 2 and 5). runRef is empty when the run row cannot be seen from this
	// workspace, and the column is nullable for exactly that case — a script in
	// a crew container and an inbound webhook have no run either.
	runRef := h.resolveProducerRun(r.Context(), req.WorkspaceID, req.AuthorRunID)
	now := h.evaluator().Now().UTC()
	producedAt := now.Format(time.RFC3339)

	// §10b.3, the same two layers as the public path — and this is the path
	// that matters most for them: the 2 880 writes/second in the PRD's
	// arithmetic come from routines and crew scripts on a 5-second loop, not
	// from a human with a CLI.
	//
	// This handler is a SECOND instance (registerInternalPageRoutes), so its
	// token buckets are its own: a producer alternating between the two doors
	// sees two buckets, not one. That is the same per-process caveat
	// config/rate-limits.yml records for every limit in it, and it is exactly
	// why the floor is enforced by the database rather than only here — the
	// floor is shared because the ROW is shared.
	if ok, scope, wait := h.pushLimits.Allow(now, req.WorkspaceID, panel.RowID); !ok {
		writePushLimited(w, scope, wait, rec.Slug, panel.PanelID)
		return
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "begin routine panel push", err)
		return
	}
	defer func() { _ = tx.Rollback() }()

	var seq int64
	if err := tx.QueryRowContext(r.Context(),
		`SELECT COALESCE(MAX(seq), 0) + 1 FROM page_panel_data WHERE panel_id = ?`, panel.RowID).Scan(&seq); err != nil {
		replyInternalError(w, h.logger, "next payload seq", err)
		return
	}
	var runCol any
	if runRef != "" {
		runCol = runRef
	}
	// The floor, in the same statement as the INSERT — pages_data.go explains
	// why it cannot be a read followed by a write.
	limits := h.pushLimits.Limits()
	res, err := tx.ExecContext(r.Context(), `
		INSERT INTO page_panel_data (panel_id, seq, payload_json, produced_at, producer_run_id, state)
		SELECT ?, ?, ?, ?, ?, ?
		WHERE NOT EXISTS (
			SELECT 1 FROM page_panel_data WHERE panel_id = ? AND produced_at > ?
		)`,
		panel.RowID, seq, string(req.Data), producedAt, runCol, push,
		panel.RowID, limits.FloorCutoff(now).Format(time.RFC3339))
	if err != nil {
		replyInternalError(w, h.logger, "insert routine panel payload", err)
		return
	}
	stored, err := res.RowsAffected()
	if err != nil {
		replyInternalError(w, h.logger, "insert routine panel payload", err)
		return
	}
	if stored == 0 {
		writePushLimited(w, pages.ScopePanel, h.floorWait(r.Context(), tx, panel.RowID, limits, now), rec.Slug, panel.PanelID)
		return
	}
	// The same bound as the public push, through the same helper: the ring is
	// bounded by the write that grows it, and the age cut is the workspace's
	// page_retention_days. A second copy of that arithmetic here would be a
	// panel whose retention depends on who wrote it.
	if err := h.evictPanelRingForWorkspace(r.Context(), tx, req.WorkspaceID, panel.RowID, now); err != nil {
		replyInternalError(w, h.logger, "evict panel ring", err)
		return
	}
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit routine panel push", err)
		return
	}

	// §10b.5b: the broadcast carries no payload, so the client re-reads through
	// the authorised path and there is only ever one copy of the per-panel
	// permission filter. Same two events the public push emits — a page does not
	// go live only when a human writes it.
	broadcastChannelEvent(h.hub, "page", rec.ID, "page.panel.updated",
		map[string]any{"page_id": rec.ID, "slug": rec.Slug, "panel_id": panel.PanelID})
	broadcastWorkspaceEvent(h.hub, req.WorkspaceID, "page.panel.updated",
		map[string]any{"page_id": rec.ID, "slug": rec.Slug, "panel_id": panel.PanelID})

	panel.HasData = true
	panel.Seq = seq
	panel.Payload = string(req.Data)
	panel.ProducedAt = now
	panel.PushState = push
	panel.RunID = runRef
	verdict := h.verdict(panel)

	writeJSON(w, http.StatusOK, map[string]any{
		"accepted": true,
		"page":     rec.Slug,
		"panel":    panel.PanelID,
		"seq":      seq,
		"state":    string(verdict.State),
		"provenance": pageProvenance{
			Producer:   panel.producerRef(),
			RunID:      pushReference(panel),
			ProducedAt: producedAt,
		},
	})
}

// resolveProducerRun returns author_run_id if it names a run in this workspace,
// and "" otherwise.
//
// The check is not ceremony: producer_run_id is a foreign key onto
// pipeline_runs, so an id that does not resolve would fail the INSERT and turn
// a legitimate push into a 500. Scoping it to the workspace additionally means
// the provenance a page displays can only ever point at a run the page's own
// tenant can open.
func (h *PageHandler) resolveProducerRun(ctx context.Context, wsID, runID string) string {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return ""
	}
	var got string
	if err := h.db.QueryRowContext(ctx,
		`SELECT id FROM pipeline_runs WHERE id = ? AND workspace_id = ?`, runID, wsID).Scan(&got); err != nil {
		return ""
	}
	return got
}

// mayProduceUnattended answers §7.1 rule 4 — "Only the declared producer may
// write a panel's payload" — for a caller that is a RUN rather than a human.
//
// The human path (pages_authz.go mayProduce) ends with a fallback: for a
// `script` or `webhook` producer there is no principal to check against, so the
// page's owner or a workspace ADMIN may push. NOTHING here inherits that
// fallback, and the absence is the point — "the routine that happens to be
// running" is not an owner, and a routine that could write any script-produced
// panel on any page in its workspace would make the producer field decorative.
//
// Two ways in, both established by somebody other than the caller:
//
//  1. THE RUN IS THE DECLARED PRODUCER. producer_kind='routine' and
//     producer_ref is the slug of the routine this run is executing, read from
//     pipeline_runs — a server-side row, keyed by the run id the dispatcher
//     injected and the author cannot set. This is the strongest identity on the
//     path and it needs no grant: the panel already names its producer, and
//     that producer is what is calling. The `agent` producer kind is the same
//     statement about the acting agent.
//
//  2. A HUMAN ISSUED A `produce` GRANT covering this panel, to the acting agent
//     or to the routine's author crew (§7.1b). Grants are the mechanism the PRD
//     defines for exactly this, only a human may issue one (rule 1), and every
//     issue is journalled.
//
// THE USE-TIME NARROWING, and what is and is not enforced. §7.1b's invariant is
// that an agent's authority is a subset of the authorising human's, evaluated
// at USE time — "if that human loses access to a crew, every agent grant they
// issued narrows with them". The half enforced here is the one that can be
// asked of the database in the same query: the granting human must still be
// able to SEE the panel, which is membership of its owning crew or a workspace
// role that outranks it (pages_authz.go canSeePanel is the same test). A grant
// issued by somebody who has since left the crew stops working, which is the
// case the rule was written for. What is NOT reconstructed is whether that
// human could push this exact panel themselves today, because for a
// routine/agent-produced panel that answer is itself a grant lookup and the
// chain has no natural end. Delegating write authority over data you cannot
// read is the escalation that mattered, and it is closed.
//
// Returns the reason on refusal so the 403, the journal entry and the owner's
// notification all say the same thing.
func (h *PageHandler) mayProduceUnattended(ctx context.Context, req internalPagePush, rec *pageRecord, panel *panelRecord) (bool, string, error) {
	// 1. The declared producer, calling.
	switch panel.ProducerKind {
	case "routine":
		slug, err := h.runRoutineSlug(ctx, req.WorkspaceID, req.AuthorRunID)
		if err != nil {
			return false, "", err
		}
		if slug != "" && slug == panel.ProducerRef {
			return true, "", nil
		}
	case "agent":
		slug, err := h.agentSlug(ctx, req.WorkspaceID, req.AgentID)
		if err != nil {
			return false, "", err
		}
		if slug != "" && slug == panel.ProducerRef {
			return true, "", nil
		}
	}

	// 2. A produce grant a human issued, still backed by that human's own reach.
	//
	// This goes through livePageGrants — the SAME reader the human surface
	// uses — and that is the whole point. §7.1b's use-time narrowing ("if that
	// human loses access to a crew, every agent grant they issued narrows with
	// them") is the load-bearing security rule in this feature, and it briefly
	// had two implementations here with two different definitions of liveness:
	// this path checked the issuer against the panel's crew, the human path
	// against the page. Two readings of one invariant do not stay equal; they
	// drift, and the drift is silent because each side has its own passing
	// tests. There is one reader now.
	if agentID := strings.TrimSpace(req.AgentID); agentID != "" &&
		h.agentMayProduce(ctx, req.WorkspaceID, rec, agentID, panel.PanelID) {
		return true, "", nil
	}
	if h.crewMayProduce(ctx, req.WorkspaceID, rec, req.CrewID, panel.PanelID) {
		return true, "", nil
	}

	return false, "panel " + panel.PanelID + " is produced by " + panel.producerRef() +
		"; this routine is not that producer and holds no produce grant covering the panel", nil
}

// runRoutineSlug resolves an injected run id to the slug of the routine it is
// executing, inside the workspace that owns the page. pipeline_slug is
// denormalised onto the run (migrate_consts_v83), so this is one read and it
// survives the routine being renamed after the run started — which is the
// honest answer, since the panel named the producer that actually ran.
func (h *PageHandler) runRoutineSlug(ctx context.Context, wsID, runID string) (string, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return "", nil
	}
	var slug string
	err := h.db.QueryRowContext(ctx,
		`SELECT pipeline_slug FROM pipeline_runs WHERE id = ? AND workspace_id = ?`, runID, wsID).Scan(&slug)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return slug, nil
}

// agentSlug resolves the acting agent id to its slug inside the workspace.
func (h *PageHandler) agentSlug(ctx context.Context, wsID, agentID string) (string, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return "", nil
	}
	var slug string
	err := h.db.QueryRowContext(ctx,
		`SELECT slug FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		agentID, wsID).Scan(&slug)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return slug, nil
}

// reportUnauthorisedRoutinePush is §7.1b rule 3 on the unattended path: the
// journal entry and the notification to the page owner, alongside the 403.
//
// The same entry type as the human refusal (EntryPageProduceDenied) with an
// AGENT actor, because an operator asking "who has been pushing at panels they
// do not hold" wants one answer, not two lists. The payload names the run as
// well as the agent — on this path the run is often the only identity there is,
// and "which routine did this" is the first question.
//
// Best-effort with respect to the response: the caller is refused whether or
// not the audit trail could be written, but a failure to write either is logged
// loudly, because an ACL nobody can audit is not a security control.
func (h *PageHandler) reportUnauthorisedRoutinePush(r *http.Request, req internalPagePush, rec *pageRecord, panel *panelRecord, reason string) {
	// The actor as an operator would name it: the acting agent when there is
	// one, otherwise the run.
	actor := req.AgentID
	actorType := journal.ActorAgent
	if strings.TrimSpace(actor) == "" {
		actor = req.AuthorRunID
		actorType = journal.ActorSystem
	}

	if h.journal != nil {
		if _, err := h.journal.Emit(r.Context(), journal.Entry{
			WorkspaceID: req.WorkspaceID,
			Type:        EntryPageProduceDenied,
			Severity:    journal.SeverityWarn,
			ActorType:   actorType,
			ActorID:     actor,
			CrewID:      req.CrewID,
			Summary: fmt.Sprintf("refused a routine payload push to %s/%s",
				rec.Slug, panel.PanelID),
			Payload: map[string]any{
				"page":           rec.Slug,
				"page_id":        rec.ID,
				"panel":          panel.PanelID,
				"producer":       panel.producerRef(),
				"actor_agent_id": req.AgentID,
				"actor_run_id":   req.AuthorRunID,
				"actor_crew_id":  req.CrewID,
				"reason":         reason,
			},
		}); err != nil && h.logger != nil {
			h.logger.Warn("pages: journal entry for a refused routine push was not written",
				"page", rec.Slug, "panel", panel.PanelID, "error", err)
		}
	}

	// One item per (panel, actor): the first occurrence asks for a human, the
	// hundredth does not need to ask again — Insert's (kind, source_id) dedup is
	// exactly that contract. A routine retrying every minute must not be able to
	// bury the inbox with the news that it is still refused.
	item := inbox.Item{
		WorkspaceID:  req.WorkspaceID,
		Kind:         inbox.KindMessage,
		SourceID:     fmt.Sprintf("page-produce-denied:%s:%s:%s", rec.ID, panel.PanelID, actor),
		TargetUserID: pageOwnerUserID(rec),
		Title:        fmt.Sprintf("Refused a routine push to %s/%s", rec.Slug, panel.PanelID),
		BodyMD: fmt.Sprintf("A `page.write` step tried to push data into panel **%s** of page **%s**, "+
			"which is produced by `%s`.\n\n%s\n\n"+
			"This is either a misconfigured routine or a step writing a panel it does not hold. "+
			"Grant it explicitly with `crewship page grant %s --agent <agent-slug> --level produce --panels %s`, "+
			"or leave it refused.",
			panel.PanelID, rec.Slug, panel.producerRef(), reason, rec.Slug, panel.PanelID),
		SenderType: "system",
		SenderName: "Pages",
		Priority:   "high",
		Payload: map[string]any{
			"page":           rec.Slug,
			"panel":          panel.PanelID,
			"producer":       panel.producerRef(),
			"actor_agent_id": req.AgentID,
			"actor_run_id":   req.AuthorRunID,
			"reason":         reason,
		},
	}
	if err := inbox.Insert(r.Context(), h.db, h.logger, item); err != nil && h.logger != nil {
		h.logger.Warn("pages: owner notification for a refused routine push was not written",
			"page", rec.Slug, "panel", panel.PanelID, "error", err)
	}
}
