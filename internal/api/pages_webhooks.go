package api

// Inbound panel webhooks — the HUMAN half (docs/prd/pages.md §10b.5c).
//
// "A panel should be writable by anything, not only by something that can
// execute the `crewship` binary — a cron on someone else's box, a Zapier step,
// a PLC gateway, a GitHub Action."
//
// This file is the three verbs a human uses to mint a token, see which tokens
// exist, and take one back. The unauthenticated half — what a cron on somebody
// else's box actually POSTs to — is pages_webhooks_inbound.go, and the two are
// separate files for the same reason §7.3.1 gives for splitting the public
// page: one of them is reachable with a session and the other with nothing but
// a 256-bit string, and keeping that boundary visible in the file layout is
// what makes it auditable.
//
// §10b.5c in one sentence: "The webhook is a `produce` grant in a different
// coat, and it obeys every rule that grant does: issued only by a human, rate
// limited per panel, revocable, and every write journalled with the token id as
// the actor."
//
// Taken literally, that sentence decides four things here and one thing on the
// fire path:
//
//	issued only by a human   pageGrantCallerIsAgent, the same POSITIVE test for
//	                         a human credential that guards a grant (§7.1b
//	                         rule 1) and a publish (§7.3.2 rule 3). Absence
//	                         denies. Three callers, one test — a second spelling
//	                         of "is this an agent" is the one that drifts.
//	bound to one panel       the token names the panel; the wire carries no
//	                         panel field at fire time, so there is nothing to
//	                         redirect (§10b.5c: "a leaked token can write one
//	                         panel and nothing else").
//	no more than its issuer  a human may not mint a token that pushes a panel
//	                         they could not push themselves. Checked here with
//	                         mayProduce — the SAME reader the CLI path uses —
//	                         and checked AGAIN on every fire, because §7.1b
//	                         evaluates a delegated authority at USE time and
//	                         never at issue time.
//	revocable                a mark, not a delete: "was it used after we pulled
//	                         it" is what an incident asks.
//
// WHY A WEBHOOK DOES NOT EXPIRE, when every public link must (§7.3.2 rule 4).
// A public link is handed to a person who will stop needing it, and the failure
// it guards against is the link nobody remembers. A webhook is wired into a
// machine that is supposed to keep running: an expiry would present as a
// producer that went quiet at 03:00 on a date nobody wrote down, which is the
// exact failure §4's freshness contract exists to make visible and which the
// operator would then have to diagnose as a credential problem. What replaces
// it is stricter than a date, and automatic: the token carries no authority of
// its own, only its issuer's, re-derived on every fire. A token outlives its
// issuer's rights by exactly zero requests.

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

// pageWebhookTokenBytes is the entropy behind one token. 32 bytes = 256 bits:
// holding the token IS the authorisation, so it has to be unguessable in the
// same sense a session id is, and the URL is the only credential the sender
// will ever have. Identical to pipeline_webhooks (generateWebhookToken) and to
// a public page link.
const pageWebhookTokenBytes = 32

// pageWebhookTokenPrefix makes a token recognisable in a log, a CI secret store
// or a paste, and distinguishes it from `wh_` (a pipeline webhook, a completely
// different endpoint). It is part of the hashed value, so it costs nothing.
const pageWebhookTokenPrefix = "pgw_"

// ── Wire ───────────────────────────────────────────────────────────────────

// pageWebhookCreateRequest is the create body.
//
// There is deliberately no `token` field — a caller does not choose the secret —
// and no `rate_limit` field: §10b.3's per-panel rate is the panel's, and a
// per-token number would be a quieter way to set one higher (see the migration).
type pageWebhookCreateRequest struct {
	Panel string `json:"panel"`
	Name  string `json:"name"`
}

// pageWebhookWire is one token as its issuer sees it.
//
// Token and URL are populated on exactly one response — the 201 from create.
// Every later read returns the row without them, because the column holds a
// digest and there is nothing to return: a token the server could show you
// again is a token the server is storing in the clear.
type pageWebhookWire struct {
	ID          string `json:"id"`
	Panel       string `json:"panel"`
	Name        string `json:"name,omitempty"`
	Token       string `json:"token,omitempty"`
	URL         string `json:"url,omitempty"`
	CreatedBy   string `json:"created_by"`
	CreatedAt   string `json:"created_at"`
	RevokedAt   string `json:"revoked_at,omitempty"`
	LastFiredAt string `json:"last_fired_at,omitempty"`
	FireCount   int64  `json:"fire_count"`
	// Live is the verdict the issuer actually wants. It is one column today,
	// but it is computed rather than inferred by each reader for the same
	// reason the public link's is: the day a second condition joins it, every
	// client that reasoned about `revoked_at` itself is wrong.
	Live bool `json:"live"`
}

// pageWebhooksWire is the list envelope.
type pageWebhooksWire struct {
	Page     string            `json:"page"`
	Webhooks []pageWebhookWire `json:"webhooks"`
}

// ── Token minting ──────────────────────────────────────────────────────────

// mintPageWebhookToken returns a fresh token and the digest to store.
//
// The digest goes through internal/pipeline.HashCapabilityToken rather than
// through a second local SHA-256, and that is the point of the import: #1888
// wrote down ONE at-rest scheme for capability tokens ("sh1:" + hex SHA-256),
// argued at length why it is unkeyed (a 256-bit random input has no offline
// search to protect against, and an HMAC key would add a rotation that could
// brick every stored digest), and gave it the two guards this path needs —
// CapabilityLookupDigest refuses a value that is ALREADY a digest, so replaying
// something read out of the database resolves to nothing.
func mintPageWebhookToken() (token, digest string, err error) {
	buf := make([]byte, pageWebhookTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("generate page webhook token: %w", err)
	}
	token = pageWebhookTokenPrefix + hex.EncodeToString(buf)
	return token, pipeline.HashCapabilityToken(token), nil
}

// PageWebhookPath is the one place the inbound URL shape is written down.
//
// §10b.5c names `POST /api/v1/pages/webhooks/{token}`, and that exact pattern
// cannot be registered: Go's ServeMux refuses it as a conflict against
// `POST /api/v1/pages/{slug}/public` and `POST /api/v1/pages/{slug}/rollback`
// (both match "/api/v1/pages/webhooks/public", and neither pattern is more
// specific than the other — the panic is at registration, i.e. at boot). See
// router_pages_webhooks.go for the full argument and for why the fix is a
// separate URL space rather than a pile of disambiguating literals.
func PageWebhookPath(token string) string { return "/api/v1/page-webhooks/" + token }

// ── 1. Create — POST /api/v1/pages/{slug}/webhooks ─────────────────────────

// CreateWebhook mints one token for one panel.
func (h *PageHandler) CreateWebhook(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())
	slug := r.PathValue("slug")

	// §10b.5c — "issued only by a human". The refusal is first, before the page
	// is even loaded: an agent minting a credential that writes a panel forever
	// is the escalation §7.1b rule 1 exists to close, and it does not become
	// less of one because the agent happens to hold `write`.
	if isAgent, _ := pageGrantCallerIsAgent(r); isAgent {
		replyError(w, http.StatusForbidden,
			"only a human may issue a page webhook (§10b.5c — the webhook is a `produce` grant in a different coat, "+
				"and §7.1b rule 1 gives that verb to a human); an agent may push through the sidecar path it already holds — "+
				"ask a human to run `crewship page webhook create`")
		return
	}

	rec, err := h.loadPage(r.Context(), wsID, slug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, fmt.Sprintf("page %q not found", slug))
			return
		}
		replyInternalError(w, h.logger, "load page for webhook create", err)
		return
	}
	// The grant-issuing gate (§7.1 rule 3): the page owner or a workspace
	// ADMIN/OWNER. A `write` grantee rearranges the page and never widens who
	// reaches it, and minting a token that can be pasted into anybody's cron is
	// as wide as widening gets.
	if !h.isPageOwner(r.Context(), wsID, user.ID, rec) && !canRole(RoleFromContext(r.Context()), "manage") {
		replyError(w, http.StatusForbidden, "only the page owner or a workspace admin may issue a webhook on this page")
		return
	}

	body, ok := readCapped(w, r, 8<<10, "webhook request")
	if !ok {
		return
	}
	var req pageWebhookCreateRequest
	if len(strings.TrimSpace(string(body))) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			replyError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
	}
	req.Panel = strings.TrimSpace(req.Panel)
	req.Name = strings.TrimSpace(req.Name)
	if req.Panel == "" {
		replyError(w, http.StatusBadRequest,
			"panel is required: a webhook token is bound to exactly one panel (§10b.5c), so there is no page-wide token to mint")
		return
	}
	if len(req.Name) > pageWebhookNameMaxBytes {
		replyError(w, http.StatusBadRequest, fmt.Sprintf(
			"name is %d bytes; the maximum is %d — it is a label for the list, not a description",
			len(req.Name), pageWebhookNameMaxBytes))
		return
	}

	panel, ok := h.findPanel(r.Context(), w, wsID, rec, req.Panel, slug)
	if !ok {
		return
	}

	// A webhook may not be a way to hold more than its issuer holds. This is
	// the SAME check the issuer's own `crewship page set` would run
	// (pages_authz.go mayProduce), asked in their name — so a page owner may
	// mint a token for a `script`- or `webhook`-produced panel, and nobody may
	// mint one for a panel a routine declares as its producer without an
	// explicit produce grant (§7.1 rule 4).
	//
	// The check is repeated on every fire. Doing it here as well is not
	// redundant: a token that would be refused at every fire is a credential
	// somebody pastes into a cron and then debugs at 03:00, and refusing to
	// mint it says so once, in the terminal of the person who can fix it.
	if allowed, reason := h.mayProduce(r.Context(), wsID, user.ID, RoleFromContext(r.Context()), rec, panel); !allowed {
		replyError(w, http.StatusForbidden,
			"a webhook cannot hold more than the human who issued it: "+reason)
		return
	}

	token, digest, err := mintPageWebhookToken()
	if err != nil {
		replyInternalError(w, h.logger, "mint page webhook token", err)
		return
	}
	id := "pgwh_" + generateCUID()
	now := h.evaluator().Now().UTC().Format(time.RFC3339)
	if _, err := h.db.ExecContext(r.Context(), `
		INSERT INTO page_webhooks (id, panel_id, token_hash, name, created_by_user_id, created_at)
		VALUES (?, ?, ?, NULLIF(?, ''), ?, ?)`,
		id, panel.RowID, digest, req.Name, user.ID, now); err != nil {
		replyInternalError(w, h.logger, "insert page webhook", err)
		return
	}

	h.journalWebhook(r.Context(), journalPageWebhookIssued, wsID, rec, user.ID, map[string]any{
		"webhook_id": id,
		"panel":      panel.PanelID,
		"producer":   panel.producerRef(),
		"name":       req.Name,
	}, fmt.Sprintf("%s issued a webhook on %s/%s", user.Email, rec.Slug, panel.PanelID))

	// The token is returned HERE and nowhere else, ever again.
	writeJSON(w, http.StatusCreated, pageWebhookWire{
		ID:        id,
		Panel:     panel.PanelID,
		Name:      req.Name,
		Token:     token,
		URL:       PageWebhookPath(token),
		CreatedBy: user.Email,
		CreatedAt: now,
		Live:      true,
	})
}

// pageWebhookNameMaxBytes bounds the optional label. It is a column in a
// tabwriter listing, not a place to store a runbook.
const pageWebhookNameMaxBytes = 200

// findPanel resolves a panel id on a loaded page, replying 404 when the page
// does not declare it. The 404 names the page and the panel, because the
// commonest way to get here is a typo in a producer script.
func (h *PageHandler) findPanel(ctx context.Context, w http.ResponseWriter, wsID string, rec *pageRecord, panelID, slug string) (*panelRecord, bool) {
	panels, err := h.loadPanels(ctx, wsID, rec.ID)
	if err != nil {
		replyInternalError(w, h.logger, "load panels for webhook", err)
		return nil, false
	}
	for _, p := range panels {
		if p.PanelID == panelID {
			return p, true
		}
	}
	replyError(w, http.StatusNotFound, fmt.Sprintf("page %q has no panel %q", slug, panelID))
	return nil, false
}

// ── 2. List — GET /api/v1/pages/{slug}/webhooks ────────────────────────────

// ListWebhooks shows the issuer which tokens exist and which still work.
//
// No token value appears, here or anywhere else: the column holds a digest and
// there is nothing to show. What is here is what somebody needs in order to
// decide which one to revoke.
func (h *PageHandler) ListWebhooks(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())
	slug := r.PathValue("slug")

	rec, err := h.loadPage(r.Context(), wsID, slug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, fmt.Sprintf("page %q not found", slug))
			return
		}
		replyInternalError(w, h.logger, "load page for webhook list", err)
		return
	}
	if !h.isPageOwner(r.Context(), wsID, user.ID, rec) && !canRole(RoleFromContext(r.Context()), "manage") {
		replyError(w, http.StatusForbidden, "only the page owner or a workspace admin may see this page's webhooks")
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT wh.id, pp.panel_id, COALESCE(wh.name, ''), COALESCE(u.email, ''),
		       wh.created_at, COALESCE(wh.revoked_at, ''), COALESCE(wh.last_fired_at, ''), wh.fire_count
		FROM page_webhooks wh
		JOIN page_panels pp ON pp.id = wh.panel_id
		LEFT JOIN users u ON u.id = wh.created_by_user_id
		WHERE pp.page_id = ?
		ORDER BY wh.created_at DESC, wh.id ASC`, rec.ID)
	if err != nil {
		replyInternalError(w, h.logger, "list page webhooks", err)
		return
	}
	defer rows.Close()

	out := pageWebhooksWire{Page: rec.Slug, Webhooks: []pageWebhookWire{}}
	for rows.Next() {
		var v pageWebhookWire
		if err := rows.Scan(&v.ID, &v.Panel, &v.Name, &v.CreatedBy,
			&v.CreatedAt, &v.RevokedAt, &v.LastFiredAt, &v.FireCount); err != nil {
			replyInternalError(w, h.logger, "scan page webhook", err)
			return
		}
		v.Live = v.RevokedAt == ""
		out.Webhooks = append(out.Webhooks, v)
	}
	if err := rows.Err(); err != nil {
		replyInternalError(w, h.logger, "rows iteration (page webhooks)", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ── 3. Revoke — DELETE /api/v1/pages/{slug}/webhooks/{webhookId} ───────────

// RevokeWebhook takes one token back, immediately.
//
// The id is in the path rather than in a body for the reason RevokePublicLink
// gives: revoking is the emergency verb, and an emergency verb whose target is
// buried in a JSON body is one somebody gets wrong.
//
// Revocation is NOT gated on being human, and that asymmetry with create is
// deliberate — the same one page publishing makes. An agent that notices a
// leaked token should be able to close it: §7.1b rule 1 forbids WIDENING reach,
// and narrowing it is the opposite move.
func (h *PageHandler) RevokeWebhook(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())
	slug := r.PathValue("slug")
	webhookID := r.PathValue("webhookId")

	rec, err := h.loadPage(r.Context(), wsID, slug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, fmt.Sprintf("page %q not found", slug))
			return
		}
		replyInternalError(w, h.logger, "load page for webhook revoke", err)
		return
	}
	if !h.isPageOwner(r.Context(), wsID, user.ID, rec) && !canRole(RoleFromContext(r.Context()), "manage") {
		replyError(w, http.StatusForbidden, "only the page owner or a workspace admin may revoke this page's webhooks")
		return
	}

	now := h.evaluator().Now().UTC().Format(time.RFC3339)
	// The page is part of the predicate, not just of the lookup: a token id
	// from another page must not be revocable through this page's URL, and
	// making that a property of the UPDATE means it cannot be forgotten.
	res, err := h.db.ExecContext(r.Context(), `
		UPDATE page_webhooks
		   SET revoked_at = ?
		 WHERE id = ?
		   AND revoked_at IS NULL
		   AND panel_id IN (SELECT id FROM page_panels WHERE page_id = ?)`, now, webhookID, rec.ID)
	if err != nil {
		replyInternalError(w, h.logger, "revoke page webhook", err)
		return
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		var exists int
		_ = h.db.QueryRowContext(r.Context(), `
			SELECT 1 FROM page_webhooks wh
			 WHERE wh.id = ? AND wh.panel_id IN (SELECT id FROM page_panels WHERE page_id = ?)`,
			webhookID, rec.ID).Scan(&exists)
		if exists == 1 {
			// Already revoked. The caller's next action is the same either way,
			// and reporting it as a failure would make a retry look like a bug.
			writeJSON(w, http.StatusOK, map[string]any{"id": webhookID, "revoked": true, "already": true})
			return
		}
		replyError(w, http.StatusNotFound, fmt.Sprintf("page %q has no webhook with id %q", slug, webhookID))
		return
	}

	h.journalWebhook(r.Context(), journalPageWebhookRevoked, wsID, rec, user.ID,
		map[string]any{"webhook_id": webhookID},
		fmt.Sprintf("%s revoked a webhook on page %s", user.Email, rec.Slug))

	writeJSON(w, http.StatusOK, map[string]any{"id": webhookID, "revoked": true})
}

// ── Journal ────────────────────────────────────────────────────────────────

// The two entry types this surface writes. Declared here rather than in
// internal/journal because unknown types are forwarded by design
// (journal.IsFeedRelevant denylists high-volume telemetry and passes everything
// else), so a new human-facing type reaches the activity feed with no change
// there — the same argument pages_public_tokens.go makes for its three.
//
// Issuing is SeverityNotice for the reason publishing is: a credential that
// writes a panel from outside the product is a widening of reach, and it should
// be visible in a feed somebody skims rather than only in a table somebody
// queries.
const (
	journalPageWebhookIssued  = journal.EntryType("page.webhook_issued")
	journalPageWebhookRevoked = journal.EntryType("page.webhook_revoked")
)

func (h *PageHandler) journalWebhook(ctx context.Context, entryType journal.EntryType,
	wsID string, rec *pageRecord, actorID string, payload map[string]any, summary string) {
	if h.journal == nil {
		return
	}
	payload["page"] = rec.Slug
	payload["page_id"] = rec.ID
	payload["actor_user_id"] = actorID
	if _, err := h.journal.Emit(ctx, journal.Entry{
		WorkspaceID: wsID,
		Type:        entryType,
		Severity:    journal.SeverityNotice,
		ActorType:   journal.ActorUser,
		ActorID:     actorID,
		Summary:     summary,
		Payload:     payload,
	}); err != nil && h.logger != nil {
		h.logger.Warn("pages: webhook change was not journalled",
			"page", rec.Slug, "type", string(entryType), "error", err)
	}
}
