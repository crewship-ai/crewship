package api

// Pages — the write path (docs/prd/pages.md §11, §4, §7.1b, §10, §10b.3).
//
//	PUT /api/v1/pages/{slug}/panels/{id}/data
//
// "`crewship page set <page>/<panel> --data -` reading JSON on stdin is the
// single write path, and it is what appears in every producer script.
// Provenance is attached server-side." (§11)
//
// Three properties of this file are the feature's security model, and each of
// them is a rule that fails silently when it is wrong:
//
//  1. THE BODY IS THE PAYLOAD, AND NOTHING ELSE. There is no field here through
//     which a producer could supply an identity, a run id or a timestamp. §4
//     rule 5 makes provenance server-attached and §7.1b makes agent identity a
//     property of the token, never of the request body — the sidecar already
//     overwrites caller-supplied identity fields (internal/sidecar/identity.go)
//     and a page write takes the same shape: the fields simply do not exist.
//     `state=failed` rides on the QUERY STRING for the same reason — a `state`
//     key in the body would sit next to the payload's own keys and read as
//     part of it.
//
//  2. THE CAP IS ENFORCED HERE. §10: MaxBytesReader → decode → an explicit
//     refusal, never a DB CHECK, because a CHECK cannot produce the rejection
//     envelope the API owes the caller and cannot be raised without a
//     migration. Over the cap is 422 with the richer envelope from
//     internal/sidecar/memory_write.go — not a bare 400, and never a 500.
//
//  3. AN UNAUTHORISED PUSH IS A SIGNAL, NOT NOISE (§7.1b rule 3). It returns
//     403, writes a journal entry, and notifies the page owner. It is equally
//     likely to be a misconfiguration or an injection, and both deserve a
//     human's attention on the first occurrence rather than the hundredth.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pages"
)

// EntryPageProduceDenied records a push to a panel the caller does not hold.
//
// Declared here rather than in internal/journal/types.go only because that
// file belongs to another slice; the string is the stable wire identifier and
// moving the constant later changes nothing. Payload: page, panel, producer,
// actor_user_id, reason.
const EntryPageProduceDenied journal.EntryType = "page.produce_denied"

// pageRejection is the 422 envelope, copied in shape from
// internal/sidecar/memory_write.go MemoryWriteRejection.
//
// A bare 400 would tell a producer script "you did something wrong" and leave
// it to guess what; this says which limit, what was attempted, and what the
// ceiling is, in fields a script can branch on and a message a human can read.
type pageRejection struct {
	Rejected bool           `json:"rejected"`
	Kind     string         `json:"kind"`
	Detail   map[string]any `json:"detail,omitempty"`
	Message  string         `json:"message,omitempty"`
}

// writeRejection sends the rejection as 422 Unprocessable Entity. 422 and not
// 400: the request was well-formed and the server understood it, it just
// refuses to store it — and the CLI maps 422 onto ExitValidation, which is
// what a producer script branches on (internal/cli/errors.go).
func writeRejection(w http.ResponseWriter, rej pageRejection) {
	rej.Rejected = true
	writeJSON(w, http.StatusUnprocessableEntity, rej)
}

// readCapped reads a request body bounded by limit.
//
// The reader is given limit+1 so the handler can tell "exactly at the cap"
// from "over it" without trusting Content-Length, which a client controls. A
// body past that trips MaxBytesReader, and the reported size falls back to the
// declared Content-Length — over-cap by an unknown amount is still over-cap,
// and the message says the limit either way.
func readCapped(w http.ResponseWriter, r *http.Request, limit int, what string) ([]byte, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, int64(limit)+1)
	data, err := io.ReadAll(r.Body)
	attempted := len(data)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if !errors.As(err, &tooLarge) {
			replyError(w, http.StatusBadRequest, "could not read the request body")
			return nil, false
		}
		if r.ContentLength > int64(attempted) {
			attempted = int(r.ContentLength)
		}
	}
	if err == nil && attempted <= limit {
		return data, true
	}
	writeRejection(w, pageRejection{
		Kind:    "cap",
		Message: fmt.Sprintf("%s is %d bytes; the limit is %d (%d KiB)", what, attempted, limit, limit/1024),
		Detail: map[string]any{
			"bytes_attempted": attempted,
			"bytes_limit":     limit,
		},
	})
	return nil, false
}

// PushData stores one payload for one panel.
//
// PUT /api/v1/pages/{slug}/panels/{id}/data
func (h *PageHandler) PushData(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())
	slug := r.PathValue("slug")
	panelID := r.PathValue("panelId")

	rec, err := h.loadPage(r.Context(), wsID, slug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, fmt.Sprintf("page %q not found", slug))
			return
		}
		replyInternalError(w, h.logger, "load page for push", err)
		return
	}
	panels, err := h.loadPanels(r.Context(), wsID, rec.ID)
	if err != nil {
		replyInternalError(w, h.logger, "load panels for push", err)
		return
	}
	var panel *panelRecord
	for _, p := range panels {
		if p.PanelID == panelID {
			panel = p
			break
		}
	}
	if panel == nil {
		replyError(w, http.StatusNotFound,
			fmt.Sprintf("page %q has no panel %q", slug, panelID))
		return
	}

	// §7.1 rule 4 / §7.1b rule 3. The refusal happens BEFORE the body is read:
	// a caller who may not write this panel does not get to spend the server's
	// memory on a 64 KiB payload first.
	if ok, reason := h.mayProduce(r.Context(), wsID, user.ID, RoleFromContext(r.Context()), rec, panel); !ok {
		h.reportUnauthorisedPush(r, wsID, user, rec, panel, reason)
		replyError(w, http.StatusForbidden, reason)
		return
	}

	// The server's clock, always (§4 rule 2, and the migration's "there is
	// deliberately no column in which a producer could supply its own
	// timestamp"). Read once: the rate decision, the stored produced_at and
	// the ring's age cut all have to be the same instant, or a push can be
	// admitted against one clock and evicted against another.
	now := h.evaluator().Now().UTC()

	// §10b.3 layer 1, and like the ACL check it happens before the body is
	// read. A producer in a hot loop is the case this exists for, and making
	// it upload 64 KiB per refusal would be paying for the flood twice.
	if ok, scope, wait := h.pushLimits.Allow(now, wsID, panel.RowID); !ok {
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
	// The decoded payload is kept, not discarded: the wake gates read it
	// (§5), and decoding it a second time to answer "is this critical" would
	// be paying for the schema twice on the write path.
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

	// §4 rule 2: the producer's own verdict is the ONLY part of the state it
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
		replyInternalError(w, h.logger, "begin panel push", err)
		return
	}
	defer func() { _ = tx.Rollback() }()

	var seq int64
	if err := tx.QueryRowContext(r.Context(),
		`SELECT COALESCE(MAX(seq), 0) + 1 FROM page_panel_data WHERE panel_id = ?`, panel.RowID).Scan(&seq); err != nil {
		replyInternalError(w, h.logger, "next payload seq", err)
		return
	}
	// §10b.3 layer 2 — the floor, expressed as part of the INSERT itself.
	//
	// The WHERE NOT EXISTS is the whole point: read-then-write would be two
	// statements with a window between them, and the failure this defends
	// against is precisely two processes deciding at the same instant. As one
	// statement the database arbitrates, so the floor holds with any number of
	// replicas — which the per-process buckets above cannot claim.
	//
	// The comparison is textual, which is exact here because produced_at is
	// written in exactly one place (the line above) in exactly one format:
	// RFC3339, UTC, second resolution — lexicographically ordered.
	limits := h.pushLimits.Limits()
	floorCutoff := limits.FloorCutoff(now).Format(time.RFC3339)
	res, err := tx.ExecContext(r.Context(), `
		INSERT INTO page_panel_data (panel_id, seq, payload_json, produced_at, producer_run_id, state)
		SELECT ?, ?, ?, ?, NULL, ?
		WHERE NOT EXISTS (
			SELECT 1 FROM page_panel_data WHERE panel_id = ? AND produced_at > ?
		)`,
		panel.RowID, seq, string(body), producedAt, push, panel.RowID, floorCutoff)
	if err != nil {
		replyInternalError(w, h.logger, "insert panel payload", err)
		return
	}
	stored, err := res.RowsAffected()
	if err != nil {
		replyInternalError(w, h.logger, "insert panel payload", err)
		return
	}
	if stored == 0 {
		// Nothing was written, and the transaction is rolled back by the
		// deferred call. Retry-After is computed from the row that blocked it
		// rather than quoted as the whole interval, so a producer that waits
		// the stated time is admitted rather than refused a second time.
		writePushLimited(w, pages.ScopePanel, h.floorWait(r.Context(), tx, panel.RowID, limits, now), rec.Slug, panel.PanelID)
		return
	}
	if err := h.evictPanelRingForWorkspace(r.Context(), tx, wsID, panel.RowID, now); err != nil {
		replyInternalError(w, h.logger, "evict panel ring", err)
		return
	}
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit panel push", err)
		return
	}

	// §10b.5b: every page is live, and the broadcast carries no payload — the
	// client re-reads through the authorised path, so there is only ever one
	// copy of the per-panel permission filter.
	broadcastChannelEvent(h.hub, "page", rec.ID, "page.panel.updated",
		map[string]any{"page_id": rec.ID, "slug": rec.Slug, "panel_id": panel.PanelID})
	broadcastWorkspaceEvent(h.hub, wsID, "page.panel.updated",
		map[string]any{"page_id": rec.ID, "slug": rec.Slug, "panel_id": panel.PanelID})

	h.recordPanelPush(r.Context(), wsID, rec, panel, seq, push, journal.ActorUser, user.ID,
		fmt.Sprintf("%s pushed %s/%s", user.Email, rec.Slug, panel.PanelID), payload)

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

// writePushLimited is §10b.3's "over the limit: 429 + Retry-After", in the
// shape pipelines_exec.go:211-218 already uses for a concurrency rejection:
// the header, then a JSON body carrying an error and the reason it happened.
// A rate rejection is a normal outcome, not an internal error.
//
// The scope is named because "you are pushing this panel too fast" and "this
// workspace is pushing too fast" ask the producer for different fixes, and a
// bare 429 leaves it to guess which one it is.
func writePushLimited(w http.ResponseWriter, scope pages.LimitScope, wait time.Duration, slug, panelID string) {
	retry := pages.RetryAfterSeconds(wait)
	w.Header().Set("Retry-After", strconv.Itoa(retry))
	reason := fmt.Sprintf("panel %s/%s is being pushed faster than its rate limit allows", slug, panelID)
	if scope == pages.ScopeWorkspace {
		reason = "this workspace is pushing panel data faster than its rate limit allows; " +
			"per-panel limits do not compose, so the workspace cap is the backstop"
	}
	writeJSON(w, http.StatusTooManyRequests, map[string]any{
		"error":            "push rate limit exceeded",
		"reason":           reason,
		"scope":            string(scope),
		"retry_after_secs": retry,
		"page":             slug,
		"panel":            panelID,
	})
}

// floorWait computes how long the caller must wait for the panel's minimum
// interval to open, from the payload that blocked the push. Best effort: if the
// row cannot be read, the full interval is the honest fallback — it is never
// shorter than the true wait, so a producer obeying it is never refused twice
// for the same reason.
func (h *PageHandler) floorWait(ctx context.Context, tx *sql.Tx, panelRowID string, limits pages.PushLimits, now time.Time) time.Duration {
	var newest sql.NullString
	if err := tx.QueryRowContext(ctx,
		`SELECT MAX(produced_at) FROM page_panel_data WHERE panel_id = ?`, panelRowID).Scan(&newest); err != nil || !newest.Valid {
		return limits.MinInterval()
	}
	last := parsePageTime(newest.String)
	if last.IsZero() {
		return limits.MinInterval()
	}
	ok, wait := limits.AdmitPush(last, true, now)
	if ok {
		// The row moved between the INSERT and this read (another push landed
		// and was itself evicted, say). Ask for one interval rather than zero:
		// "retry immediately" is what the floor exists to refuse.
		return limits.MinInterval()
	}
	return wait
}

// evictPanelRingForWorkspace applies §10b.3's bound — newest 200 payloads, age
// cut at the workspace's page_retention_days — in the SAME transaction as the
// push, so a panel's storage is bounded by the write that grows it rather than
// by a sweep that might not run. A panel pushed at the floor rate would
// otherwise produce ~300 000 rows a week, per panel.
//
// The age cut is read per push rather than hard-coded: NULL (every existing
// row) means pages.DefaultPageRetentionDays, following the run_retention_days
// convention. It is one indexed lookup by primary key, in a transaction that is
// already writing.
//
// The eviction rule itself lives in internal/pages: it is two rules with an
// ordering between them (count first, age second, and the NEWEST payload
// survives both, so a producer dead for eight days keeps saying when it died),
// and it has to be tested against a clock rather than by waiting.
func (h *PageHandler) evictPanelRingForWorkspace(ctx context.Context, tx *sql.Tx, wsID, panelRowID string, now time.Time) error {
	var days sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT page_retention_days FROM workspaces WHERE id = ?`, wsID).Scan(&days); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return evictPanelRing(ctx, tx, panelRowID, now, pages.RetentionAge(int(days.Int64)))
}

// evictPanelRing is the effect half: read the ring, ask internal/pages what to
// drop, delete exactly that.
func evictPanelRing(ctx context.Context, tx *sql.Tx, panelRowID string, now time.Time, maxAge time.Duration) error {
	rows, err := tx.QueryContext(ctx, `SELECT seq, produced_at FROM page_panel_data WHERE panel_id = ?`, panelRowID)
	if err != nil {
		return err
	}
	var entries []pages.RingEntry
	for rows.Next() {
		var seq int64
		var producedAt string
		if err := rows.Scan(&seq, &producedAt); err != nil {
			rows.Close()
			return err
		}
		entries = append(entries, pages.RingEntry{Seq: seq, ProducedAt: parsePageTime(producedAt)})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, e := range pages.EvictRingWithin(entries, now, maxAge) {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM page_panel_data WHERE panel_id = ? AND seq = ?`, panelRowID, e.Seq); err != nil {
			return err
		}
	}
	return nil
}

// reportUnauthorisedPush is §7.1b rule 3's second and third halves: the
// journal entry and the notification to the page owner.
//
// Both are best-effort with respect to the HTTP response — the caller is
// recordPanelPush writes the journal entry for an accepted push.
//
// §5 is the section that answers §2.1 — the documented reason the push-to-panel
// genre lost to query-based dashboards — and its answer for history is
// "internal/journal: every push emits an entry; the journal is the queryable
// record". Without this the ring is the only history Pages has, which is the
// thing §5 explicitly says is NOT the answer, and §5's alerting row has no
// event for an automation matcher to fire on.
//
// It is deliberately not fatal. The payload is already committed; refusing the
// response now would tell a producer its push failed when the number is stored
// and rendering. A failure to write the entry is logged instead, the same
// bargain reportUnauthorisedPush makes in the other direction.
func (h *PageHandler) recordPanelPush(ctx context.Context, wsID string, rec *pageRecord,
	panel *panelRecord, seq int64, push string, actorType journal.ActorType, actorID, summary string,
	payload pages.Payload) {
	if h.journal == nil {
		return
	}
	severity := journal.SeverityInfo
	if push == "failed" {
		severity = journal.SeverityWarn
	}
	entryPayload := map[string]any{
		"page":     rec.Slug,
		"page_id":  rec.ID,
		"panel":    panel.PanelID,
		"schema":   panel.Schema,
		"producer": panel.producerRef(),
		"seq":      seq,
		// The producer's own verdict, not the freshness state: fresh and
		// stale are functions of the clock and are never stored (§4), so
		// an entry claiming one would be wrong the moment it was read.
		"state": push,
	}
	// §5: a panel whose wake gates this push ARMS carries one extra boolean
	// per armed gate, and that boolean is what the compiled `automations` rule
	// matches on. A panel with no gates adds nothing — the predicate, the hold
	// window and the ring read all live behind a length check (pages_wake.go).
	for k, v := range h.wakeSignals(ctx, wsID, panel, payload, h.evaluator().Now().UTC()) {
		entryPayload[k] = v
	}
	if _, err := h.journal.Emit(ctx, journal.Entry{
		WorkspaceID: wsID,
		CrewID:      panel.OwnerCrewID,
		Type:        journal.EntryPagePanelUpdated,
		Severity:    severity,
		ActorType:   actorType,
		ActorID:     actorID,
		Summary:     summary,
		Payload:     entryPayload,
	}); err != nil && h.logger != nil {
		h.logger.Warn("pages: journal entry for an accepted push was not written",
			"page", rec.Slug, "panel", panel.PanelID, "error", err)
	}
}

// refused whether or not the audit trail could be written — but a failure to
// write either is logged loudly, because an ACL nobody can audit is not a
// security control.
func (h *PageHandler) reportUnauthorisedPush(r *http.Request, wsID string, user *AuthUser, rec *pageRecord, panel *panelRecord, reason string) {
	if h.journal != nil {
		if _, err := h.journal.Emit(r.Context(), journal.Entry{
			WorkspaceID: wsID,
			Type:        EntryPageProduceDenied,
			Severity:    journal.SeverityWarn,
			ActorType:   journal.ActorUser,
			ActorID:     user.ID,
			Summary: fmt.Sprintf("refused a payload push to %s/%s from %s",
				rec.Slug, panel.PanelID, user.Email),
			Payload: map[string]any{
				"page":          rec.Slug,
				"page_id":       rec.ID,
				"panel":         panel.PanelID,
				"producer":      panel.producerRef(),
				"actor_user_id": user.ID,
				"reason":        reason,
			},
		}); err != nil && h.logger != nil {
			h.logger.Warn("pages: journal entry for a refused push was not written",
				"page", rec.Slug, "panel", panel.PanelID, "error", err)
		}
	}

	// One item per (panel, actor): the first occurrence asks for a human, the
	// hundredth does not need to ask again — Insert's (kind, source_id) dedup
	// is exactly that contract.
	item := inbox.Item{
		WorkspaceID:  wsID,
		Kind:         inbox.KindMessage,
		SourceID:     fmt.Sprintf("page-produce-denied:%s:%s:%s", rec.ID, panel.PanelID, user.ID),
		TargetUserID: pageOwnerUserID(rec),
		Title:        fmt.Sprintf("Refused a push to %s/%s", rec.Slug, panel.PanelID),
		BodyMD: fmt.Sprintf("`%s` tried to push data into panel **%s** of page **%s**, which is produced by `%s`.\n\n%s\n\n"+
			"This is either a misconfigured producer or an attempt to write a panel it does not hold. "+
			"Grant it explicitly with `crewship page grant %s --user %s --level produce --panels %s`, or leave it refused.",
			user.Email, panel.PanelID, rec.Slug, panel.producerRef(), reason, rec.Slug, user.Email, panel.PanelID),
		SenderType: "system",
		SenderName: "Pages",
		Priority:   "high",
		Payload: map[string]any{
			"page":          rec.Slug,
			"panel":         panel.PanelID,
			"producer":      panel.producerRef(),
			"actor_user_id": user.ID,
			"reason":        reason,
		},
	}
	if err := inbox.Insert(r.Context(), h.db, h.logger, item); err != nil && h.logger != nil {
		h.logger.Warn("pages: owner notification for a refused push was not written",
			"page", rec.Slug, "panel", panel.PanelID, "error", err)
	}
}
