package api

// Inbound panel webhooks — the UNAUTHENTICATED half (docs/prd/pages.md
// §10b.5c).
//
//	POST /api/v1/page-webhooks/{token}
//
// This is the door a cron on somebody else's box, a Zapier step, a PLC gateway
// or a GitHub Action pushes through. It authenticates nothing: holding the
// token IS the authorisation, exactly as it is for a pipeline webhook
// (POST /api/v1/webhooks/{token}) and for a published page (/p/{token}).
//
// It is the same write as PUT /api/v1/pages/{slug}/panels/{id}/data
// (pages_data.go) with the caller swapped, and — as with the routine path in
// pages_internal.go — everything that differs follows from that one swap:
//
//  1. THE BODY IS THE PAYLOAD, AND NOTHING ELSE. There is no envelope here, and
//     that is the security property, not a convenience: the token names the
//     workspace, the page and the panel, so a body carrying `panel`,
//     `producer`, `produced_at` or `workspace_id` is just payload with
//     unfortunate key names. §4 rule 5 keeps provenance server-attached and
//     there is no field through which a sender could claim any of it — produced_at
//     is this machine's clock and producer_run_id is NULL, because a cron on
//     somebody else's box has no run.
//
//     `state=failed` rides on the QUERY STRING for the reason pages_data.go
//     gives: a `state` key in the body would sit next to the payload's own keys
//     and read as part of it.
//
//  2. THE AUTHORITY IS THE ISSUER'S, RE-DERIVED ON EVERY FIRE. §10b.5c: "The
//     webhook is a `produce` grant in a different coat, and it obeys every rule
//     that grant does". The rule that matters most is §7.1b's: a delegated
//     authority is evaluated against the authorising human's own rights AT USE
//     TIME, never at issue time. So this path reads WHO minted the token and
//     asks mayProduce in their name — the same single reader the CLI push and
//     the grant surface use. A token whose issuer left the workspace, lost the
//     crew, or had their grant withdrawn stops working on the next request, not
//     on the next sweep. There is no stored copy of its authority to go stale.
//
//  3. THE LIMITS ARE THE PANEL'S. §10b.5c says "rate limited per panel", and
//     the panel already has a rate (§10b.3): the per-panel and per-workspace
//     token buckets, plus the minimum-interval floor enforced inside the push
//     transaction. Both apply here unchanged. A webhook is not a way around
//     them, which is also why the row carries no rate limit of its own.
//
//  4. THE 422 IS THE SAME 422. §11b.6 requires the same rejection envelope on
//     every write path, which pages.ValidatePayload gives for free: it is the
//     single entry point that owns the 64 KiB judgement, so a Zapier step gets
//     byte-for-byte what `crewship page set` gets.
//
// WHY EVERY REFUSAL AT THE DOOR IS A 404. An unknown token, a revoked token and
// a well-formed token that is not a token at all are indistinguishable in the
// response, and deliberately so: 403 would confirm that a token exists, which
// is the one bit an attacker holding a guess wants. pipeline_webhooks'
// GetByToken makes the same choice in as many words. A refusal AFTER the token
// resolved — the issuer lost their standing — is a 403, because at that point
// the holder is a legitimate sender whose integration has to be fixed, and
// telling them "not found" would send them hunting for a deleted panel.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pages"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

// pageWebhookBinding is what a token resolves to: one panel, on one page, in
// one workspace, minted by one human. Every field is read from the server's own
// tables — none of it can be influenced by the request.
type pageWebhookBinding struct {
	WebhookID   string
	PanelRowID  string
	PageID      string
	Slug        string
	WorkspaceID string
	IssuerID    string
	Name        string
}

// resolvePageWebhookToken maps a presented token to its binding, or
// sql.ErrNoRows.
//
// The digest is computed by internal/pipeline.CapabilityLookupDigest, which
// refuses a value that is already an at-rest digest or a redaction marker:
// replaying something read out of the database resolves to nothing rather than
// to an unfiltered query. A revoked row is not selected at all, so revocation
// is immediate and takes effect on the very next request — there is no cache,
// no window and nothing to invalidate.
func (h *PageHandler) resolvePageWebhookToken(ctx context.Context, token string) (*pageWebhookBinding, error) {
	digest := pipeline.CapabilityLookupDigest(token)
	if digest == "" {
		return nil, sql.ErrNoRows
	}
	var b pageWebhookBinding
	var stored string
	err := h.db.QueryRowContext(ctx, `
		SELECT wh.id, wh.token_hash, COALESCE(wh.name, ''), wh.created_by_user_id,
		       pp.id, pg.id, pg.slug, pg.workspace_id
		FROM page_webhooks wh
		JOIN page_panels pp ON pp.id = wh.panel_id
		JOIN pages pg ON pg.id = pp.page_id
		WHERE wh.token_hash = ? AND wh.revoked_at IS NULL`, digest).Scan(
		&b.WebhookID, &stored, &b.Name, &b.IssuerID,
		&b.PanelRowID, &b.PageID, &b.Slug, &b.WorkspaceID)
	if err != nil {
		return nil, err
	}
	// Constant-time confirmation that the row the unique index matched really
	// is the one this token digests to, so the decision never rests on a
	// short-circuiting byte comparison.
	if !pipeline.CapabilityDigestEqual(stored, digest) {
		return nil, sql.ErrNoRows
	}
	return &b, nil
}

// FireWebhook stores one payload for one panel on behalf of a token holder.
//
// POST /api/v1/page-webhooks/{token}
func (h *PageHandler) FireWebhook(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")

	// The token FIRST, before the body is touched. An unauthorised caller must
	// not get to spend the server's memory on 64 KiB — the same ordering
	// pages_data.go uses for its permission check, and the reason this path can
	// keep it (unlike the internal one) is that the token carries the identity
	// instead of the body.
	bind, err := h.resolvePageWebhookToken(r.Context(), token)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			pageWebhookNotFound(w)
			return
		}
		replyInternalError(w, h.logger, "resolve page webhook token", err)
		return
	}

	rec, err := h.loadPage(r.Context(), bind.WorkspaceID, bind.Slug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// The page vanished between the join and this read. Nothing to
			// write to, and nothing this sender can do about it.
			pageWebhookNotFound(w)
			return
		}
		replyInternalError(w, h.logger, "load page for webhook push", err)
		return
	}
	panels, err := h.loadPanels(r.Context(), bind.WorkspaceID, rec.ID)
	if err != nil {
		replyInternalError(w, h.logger, "load panels for webhook push", err)
		return
	}
	var panel *panelRecord
	for _, p := range panels {
		// Matched on the ROW id, which is what the token is bound to. The
		// author-chosen panel id is stable across edits but is not what the FK
		// points at, and matching on it would quietly re-point a token at a
		// panel somebody dropped and re-added.
		if p.RowID == bind.PanelRowID {
			panel = p
			break
		}
	}
	if panel == nil {
		pageWebhookNotFound(w)
		return
	}

	// §7.1b's use-time narrowing, and the whole of this endpoint's authority.
	if allowed, reason := h.webhookIssuerMayProduce(r.Context(), bind, rec, panel); !allowed {
		h.reportUnauthorisedWebhookPush(r, bind, rec, panel, reason)
		replyError(w, http.StatusForbidden, reason)
		return
	}

	// The server's clock, always (§4 rule 2). Read once: the rate decision, the
	// stored produced_at and the ring's age cut all have to be the same instant.
	now := h.evaluator().Now().UTC()

	// §10b.3 layer 1, before the body is read — a sender in a hot loop is
	// exactly the case this exists for, and making it upload 64 KiB per refusal
	// would be paying for the flood twice.
	if ok, scope, wait := h.pushLimits.Allow(now, bind.WorkspaceID, panel.RowID); !ok {
		writePushLimited(w, scope, wait, rec.Slug, panel.PanelID)
		return
	}

	body, ok := readCapped(w, r, pages.MaxPayloadBytes, "panel payload")
	if !ok {
		return
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		replyError(w, http.StatusBadRequest,
			"the request body is empty; a push carries the panel's payload as JSON")
		return
	}
	// §11b.6 — the same function, and therefore the same 422, as the CLI path.
	// The decoded payload is kept because the wake gates read it (§5): a
	// threshold crossed by a cron on somebody else's box wakes an agent exactly
	// as one crossed by `crewship page set` does.
	payload, err := pages.ValidatePayload(pages.PanelSchema(panel.Schema), body)
	if err != nil {
		var ve *pages.ValidationError
		if errors.As(err, &ve) {
			if ve.Code == pages.CodeTooLarge {
				writeRejection(w, pageRejection{
					Kind:    "cap",
					Message: ve.Detail,
					Detail:  map[string]any{"bytes_attempted": len(body), "bytes_limit": pages.MaxPayloadBytes},
				})
				return
			}
			replyError(w, http.StatusBadRequest, fmt.Sprintf("payload does not satisfy %s: %s", panel.Schema, ve.Detail))
			return
		}
		replyError(w, http.StatusBadRequest, err.Error())
		return
	}

	// §4 rule 2: the sender's own verdict is the ONLY part of the state it
	// influences, and it does not travel in the body (see the file header).
	push := string(pages.PushOK)
	if q := strings.TrimSpace(r.URL.Query().Get("state")); q != "" {
		if q != string(pages.PushOK) && q != string(pages.PushFailed) {
			replyError(w, http.StatusBadRequest,
				`state must be "ok" or "failed"; fresh and stale are the server's arithmetic, not a producer's claim`)
			return
		}
		push = q
	}

	producedAt := now.Format(time.RFC3339)

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "begin webhook panel push", err)
		return
	}
	defer func() { _ = tx.Rollback() }()

	var seq int64
	if err := tx.QueryRowContext(r.Context(),
		`SELECT COALESCE(MAX(seq), 0) + 1 FROM page_panel_data WHERE panel_id = ?`, panel.RowID).Scan(&seq); err != nil {
		replyInternalError(w, h.logger, "next payload seq", err)
		return
	}
	// §10b.3 layer 2 — the floor, in the same statement as the INSERT.
	// pages_data.go explains why it cannot be a read followed by a write; the
	// argument is stronger here, because this door is the one an external
	// system hammers.
	limits := h.pushLimits.Limits()
	res, err := tx.ExecContext(r.Context(), `
		INSERT INTO page_panel_data (panel_id, seq, payload_json, produced_at, producer_run_id, state)
		SELECT ?, ?, ?, ?, NULL, ?
		WHERE NOT EXISTS (
			SELECT 1 FROM page_panel_data WHERE panel_id = ? AND produced_at > ?
		)`,
		panel.RowID, seq, string(body), producedAt, push,
		panel.RowID, limits.FloorCutoff(now).Format(time.RFC3339))
	if err != nil {
		replyInternalError(w, h.logger, "insert webhook panel payload", err)
		return
	}
	stored, err := res.RowsAffected()
	if err != nil {
		replyInternalError(w, h.logger, "insert webhook panel payload", err)
		return
	}
	if stored == 0 {
		writePushLimited(w, pages.ScopePanel, h.floorWait(r.Context(), tx, panel.RowID, limits, now), rec.Slug, panel.PanelID)
		return
	}
	if err := h.evictPanelRingForWorkspace(r.Context(), tx, bind.WorkspaceID, panel.RowID, now); err != nil {
		replyInternalError(w, h.logger, "evict panel ring", err)
		return
	}
	// The fire counters ride the SAME transaction as the payload. "How many
	// times did this token write" and "how many rows did it write" must not be
	// able to disagree, and a best-effort UPDATE afterwards is exactly how they
	// would: a refused floor, a crash, or a rolled-back push would still have
	// bumped the counter.
	if _, err := tx.ExecContext(r.Context(),
		`UPDATE page_webhooks SET last_fired_at = ?, fire_count = fire_count + 1 WHERE id = ?`,
		producedAt, bind.WebhookID); err != nil {
		replyInternalError(w, h.logger, "record webhook fire", err)
		return
	}
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit webhook panel push", err)
		return
	}

	// §10b.5b: every page is live, and the broadcast carries no payload — the
	// client re-reads through the authorised path, so there is only ever one
	// copy of the per-panel permission filter. The same two events the other
	// two write paths emit: a page does not go live only when a human writes it.
	broadcastChannelEvent(h.hub, "page", rec.ID, "page.panel.updated",
		map[string]any{"page_id": rec.ID, "slug": rec.Slug, "panel_id": panel.PanelID})
	broadcastWorkspaceEvent(h.hub, bind.WorkspaceID, "page.panel.updated",
		map[string]any{"page_id": rec.ID, "slug": rec.Slug, "panel_id": panel.PanelID})

	// §10b.5c: "every write journalled with the token id as the actor". Not the
	// issuer — the issuer authorised the capability, the TOKEN performed the
	// write, and an operator asking "what has been writing this panel" needs to
	// tell one token from another so they can revoke the right one. The issuer
	// is in the payload, so the accountable human is one field away.
	h.recordPanelPush(r.Context(), bind.WorkspaceID, rec, panel, seq, push,
		journal.ActorSystem, bind.WebhookID,
		fmt.Sprintf("webhook %s pushed %s/%s", pageWebhookLabel(bind), rec.Slug, panel.PanelID), payload)

	panel.HasData = true
	panel.Seq = seq
	panel.Payload = string(body)
	panel.ProducedAt = now
	panel.PushState = push
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

// pageWebhookLabel is how a token names itself in a journal summary: its label
// when it has one, its id otherwise. The id is always in the entry's actor
// field, so the summary is free to be readable.
func pageWebhookLabel(b *pageWebhookBinding) string {
	if b.Name != "" {
		return b.Name
	}
	return b.WebhookID
}

// pageWebhookNotFound is the single refusal for everything that happens before
// a token resolves. One message, one status, no branch a caller could time.
func pageWebhookNotFound(w http.ResponseWriter) {
	replyError(w, http.StatusNotFound, "no such webhook")
}

// webhookIssuerMayProduce re-derives, on every fire, what the human who minted
// this token may do right now.
//
// Two questions, in order:
//
//  1. IS THE ISSUER STILL A MEMBER OF THIS WORKSPACE? On the CLI path this is
//     guaranteed by RequireWorkspace before any handler runs; here there is no
//     middleware and no session, so it has to be asked explicitly. Missing it
//     would mean a token minted by somebody who has since left the company
//     keeps writing — which is the single most likely way this feature could
//     become a back door.
//
//  2. COULD THEY PUSH THIS PANEL THEMSELVES? Asked through mayProduce, the same
//     function `crewship page set` goes through, with the issuer's CURRENT
//     workspace role. So the token narrows when a produce grant is withdrawn,
//     when the issuer leaves the page's owner crew, when they are demoted out
//     of ADMIN, or when the panel's declared producer changes to a routine —
//     none of which needs a sweep, a cache invalidation, or anybody remembering
//     that a token exists.
func (h *PageHandler) webhookIssuerMayProduce(ctx context.Context, bind *pageWebhookBinding,
	rec *pageRecord, panel *panelRecord) (bool, string) {
	var role string
	err := h.db.QueryRowContext(ctx,
		`SELECT role FROM workspace_members WHERE workspace_id = ? AND user_id = ?`,
		bind.WorkspaceID, bind.IssuerID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return false, "the human who issued this webhook is no longer a member of this workspace, " +
			"so the token holds nothing (§7.1b: a delegated authority is evaluated against its issuer's own rights at use time); " +
			"ask a current member to issue a new one"
	}
	if err != nil {
		// A permission check that could not read its own state has established
		// nothing. Fail closed, and say so as a refusal the sender can report
		// rather than as an accepted write.
		if h.logger != nil {
			h.logger.Warn("pages: could not read the webhook issuer's standing",
				"webhook_id", bind.WebhookID, "error", err)
		}
		return false, "the issuing human's standing could not be read, so this push is refused"
	}
	allowed, reason := h.mayProduce(ctx, bind.WorkspaceID, bind.IssuerID, role, rec, panel)
	if allowed {
		return true, ""
	}
	return false, "this webhook holds exactly what the human who issued it holds, and that is no longer enough: " + reason
}

// reportUnauthorisedWebhookPush is §7.1b rule 3 on the inbound path: the
// journal entry and the notification to the page owner, alongside the 403.
//
// The same entry type as the other two refusals (EntryPageProduceDenied), so an
// operator asking "who has been pushing at panels they do not hold" gets one
// answer rather than three lists. The actor is the TOKEN, and the payload names
// the issuer — on this path the token is the only thing present, and the issuer
// is the only thing actionable.
//
// Best-effort with respect to the response: the sender is refused whether or
// not the audit trail could be written, but a failure to write either is logged
// loudly, because an ACL nobody can audit is not a security control.
func (h *PageHandler) reportUnauthorisedWebhookPush(r *http.Request, bind *pageWebhookBinding,
	rec *pageRecord, panel *panelRecord, reason string) {
	if h.journal != nil {
		if _, err := h.journal.Emit(r.Context(), journal.Entry{
			WorkspaceID: bind.WorkspaceID,
			Type:        EntryPageProduceDenied,
			Severity:    journal.SeverityWarn,
			ActorType:   journal.ActorSystem,
			ActorID:     bind.WebhookID,
			CrewID:      panel.OwnerCrewID,
			Summary: fmt.Sprintf("refused a webhook payload push to %s/%s",
				rec.Slug, panel.PanelID),
			Payload: map[string]any{
				"page":             rec.Slug,
				"page_id":          rec.ID,
				"panel":            panel.PanelID,
				"producer":         panel.producerRef(),
				"webhook_id":       bind.WebhookID,
				"webhook_name":     bind.Name,
				"issuer_user_id":   bind.IssuerID,
				"reason":           reason,
				"actor_is_a_token": true,
			},
		}); err != nil && h.logger != nil {
			h.logger.Warn("pages: journal entry for a refused webhook push was not written",
				"page", rec.Slug, "panel", panel.PanelID, "error", err)
		}
	}

	// One item per (panel, token): a cron retrying every minute must not be
	// able to bury the inbox with the news that it is still refused, and
	// Insert's (kind, source_id) dedup is exactly that contract.
	item := inbox.Item{
		WorkspaceID:  bind.WorkspaceID,
		Kind:         inbox.KindMessage,
		SourceID:     fmt.Sprintf("page-webhook-denied:%s:%s:%s", rec.ID, panel.PanelID, bind.WebhookID),
		TargetUserID: pageOwnerUserID(rec),
		Title:        fmt.Sprintf("Refused a webhook push to %s/%s", rec.Slug, panel.PanelID),
		BodyMD: fmt.Sprintf("A webhook token (`%s`) tried to push data into panel **%s** of page **%s**, "+
			"which is produced by `%s`.\n\n%s\n\n"+
			"The sender is still holding a working URL, so this will repeat until somebody acts: either restore the "+
			"issuer's produce authority, or revoke the token with "+
			"`crewship page webhook revoke %s --id %s --yes`.",
			pageWebhookLabel(bind), panel.PanelID, rec.Slug, panel.producerRef(), reason, rec.Slug, bind.WebhookID),
		SenderType: "system",
		SenderName: "Pages",
		Priority:   "high",
		Payload: map[string]any{
			"page":           rec.Slug,
			"panel":          panel.PanelID,
			"producer":       panel.producerRef(),
			"webhook_id":     bind.WebhookID,
			"issuer_user_id": bind.IssuerID,
			"reason":         reason,
		},
	}
	if err := inbox.Insert(r.Context(), h.db, h.logger, item); err != nil && h.logger != nil {
		h.logger.Warn("pages: owner notification for a refused webhook push was not written",
			"page", rec.Slug, "panel", panel.PanelID, "error", err)
	}
}
