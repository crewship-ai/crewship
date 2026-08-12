package api

// Public pages — what somebody with no account receives (docs/prd/pages.md
// §7.3).
//
// §7.3.1: "It is a different product, not a permission level." A public page is
// served from a separate URL space (/p/{token}) that shares no session, no
// cookie and no workspace context with the app. Nothing here reads
// UserFromContext, WorkspaceIDFromContext or RoleFromContext, and nothing here
// can: the only input is a token in the path and, optionally, a password in a
// POST body.
//
// THE SHAPE OF THIS FILE IS THE SECURITY ARGUMENT. Every other read path in
// Pages starts from the internal wire type and removes what the viewer may not
// see. This one starts from an EMPTY struct and adds, field by field, what an
// outsider may. The difference matters the day somebody adds a field to
// pagePanelWire: the subtractive path leaks it by default, the additive path
// cannot leak it at all, and pages_public_test.go pins the additive type's
// field set so the compiler and a test both have to agree before anything new
// crosses this boundary.
//
// The six rules of §7.3.2, and where each of them lives:
//
//	1  read-only          no action field exists on the public wire, and the
//	                      payload is stripped of any before serialisation.
//	2  opt-in per panel   publicPanelIDs (pages_public_tokens.go).
//	3  only a human       Publish refuses an agent; the exposed panel set is the
//	                      human-attested one.
//	4  every link expires  resolved here on every view, from the stored column.
//	5  provenance stripped  the default is off and the strip is server-side.
//	6  noindex, limited, logged   setPublicPageHeaders, pagePublicLimiter, and
//	                      the once-a-day journal entry below.
//
// §7.3.2b — "show the age, never the reason" — is the ProducedAt field being
// present on every panel and the Reason field not existing.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/time/rate"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pages"
	"github.com/crewship-ai/crewship/internal/ratelimitcfg"
)

// ── Handler ────────────────────────────────────────────────────────────────

// PagePublicHandler serves the unauthenticated view path.
//
// It embeds *PageHandler for the panel loading and the freshness verdict —
// those are the same reads, and a second copy of them would be a second place
// for "is this panel stale" to be answered differently. What it does NOT
// inherit is any notion of a viewer: the embedded handler's authorisation
// helpers all take a *pageViewer, and there is no viewer to pass.
type PagePublicHandler struct {
	*PageHandler

	// views is §7.3.2 rule 6's per-token request cap: 600/h by default, tuned
	// in Settings through internal/ratelimitcfg. It bounds what a leaked link
	// costs before anybody notices it leaked.
	views *pagePublicLimiter
	// passwords is §7.3.3's separate, much tighter bucket. A view is something
	// a legitimate reader does repeatedly; a password attempt is not, and
	// sharing one bucket between them would mean either a useless view limit or
	// a brute-force allowance of 600 guesses an hour.
	passwords *pagePublicLimiter
}

// NewPagePublicHandler builds the public view handler over an existing page
// handler.
func NewPagePublicHandler(base *PageHandler) *PagePublicHandler {
	return &PagePublicHandler{
		PageHandler: base,
		views:       newPagePublicLimiter(configuredPublicViewsPerHour, 0),
		passwords:   newPagePublicLimiter(func() int { return pagePublicPasswordAttemptsPerHour }, pagePublicPasswordBurst),
	}
}

// ── The public wire ────────────────────────────────────────────────────────

// pagePublicPanelWire is one panel as an outsider receives it.
//
// Read the ABSENT fields, they are the specification:
//
//	owner       a crew slug is our org chart (rule 5).
//	producer    a routine slug, an agent slug, a script name (rule 5).
//	reason      failure text is container names and OOM traces (§7.3.2b).
//	sla_seconds  how often we expect our own producers to run is ours.
//	actions     §7.3.2 rule 1: "A public page renders no buttons. A button
//	            behind a public link is remote code execution with a URL for a
//	            credential." There is no field, so there is nothing to strip in
//	            the renderer and nothing to forget to strip.
//
// ProducedAt is present on every panel that has ever produced, INCLUDING a
// failed one — that is the whole of §7.3.2b: an outsider acting on a stale
// number is the worst version of the failure this PRD exists to prevent,
// because internally somebody would have caught it and externally they will
// invoice on it.
type pagePublicPanelWire struct {
	ID     string `json:"id"`
	Schema string `json:"schema"`
	Title  string `json:"title,omitempty"`
	Span   int    `json:"span"`
	State  string `json:"state"`
	// ProducedAt is the server-written timestamp of the newest payload. Show
	// the age.
	ProducedAt string `json:"produced_at,omitempty"`
	// Data is omitted entirely on a failed panel. The payload of a failed push
	// is producer-supplied text and is exactly where "OOM in crew-container-3"
	// arrives; §7.3.2b says a failed public panel reads "Data are not current —
	// last value 12:40" and NOTHING more.
	Data json.RawMessage `json:"data,omitempty"`
	// Provenance appears only when the publisher opted it back in per token
	// (rule 5). The default is off, so forgetting to decide cannot be the
	// disclosure.
	Provenance *pageProvenance `json:"provenance,omitempty"`
}

// pagePublicWire is the whole public document. No id, no owner, no grants, no
// workspace, no spec timestamps — a reader outside the company is told what the
// page is called and what the panels say, and nothing about who we are.
type pagePublicWire struct {
	Slug        string                `json:"slug"`
	Name        string                `json:"name"`
	Description string                `json:"description,omitempty"`
	Panels      []pagePublicPanelWire `json:"panels"`
	// GeneratedAt is when the SERVER rendered this response. Distinct from any
	// panel's produced_at, and the reason a reader can tell "the page is old"
	// from "the data are old".
	GeneratedAt string `json:"generated_at"`
	// ShowProvenance echoes the token's setting so the renderer does not have
	// to infer it from whether a field happened to be populated.
	ShowProvenance bool `json:"show_provenance"`
	// ExpiresAt is the link's own expiry. A reader who bookmarks it deserves to
	// know it will stop working, and telling them costs nothing: they are
	// holding the token already.
	ExpiresAt string `json:"expires_at"`
}

// ── Token resolution ───────────────────────────────────────────────────────

// publicToken is one resolved row.
type publicToken struct {
	ID             string
	PageID         string
	WorkspaceID    string
	PasswordHash   string
	ExpiresAt      time.Time
	ShowProvenance bool
	LastSeenAt     string
}

// pagePublicUnavailable is the ONE sentence every refusal on this surface
// carries. Unknown token, expired token, revoked token, deleted page — the
// caller is told the same thing, because the differences between them are
// facts about our workspace and the caller is outside it.
const pagePublicUnavailable = "this link is not available"

// pagePublicPasswordRefusal is §7.3.3's central requirement: "the failure must
// not distinguish 'wrong password' from 'unknown token'". One string, one
// status, one response body, for both.
const pagePublicPasswordRefusal = "that link and password do not match"

// resolvePublicToken looks a token up by its hash and applies rule 4.
//
// Returns sql.ErrNoRows for every unavailable case — unknown, revoked, expired,
// page gone — so the caller has no branch on which to build a different answer
// for each.
func (h *PagePublicHandler) resolvePublicToken(ctx context.Context, token string, now time.Time) (*publicToken, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, sql.ErrNoRows
	}
	var t publicToken
	var passwordHash, expiresAt, lastSeen sql.NullString
	var showProvenance int
	err := h.db.QueryRowContext(ctx, `
		SELECT t.id, t.page_id, p.workspace_id, t.password_hash, t.expires_at,
		       t.show_provenance, t.last_seen_at
		FROM page_public_tokens t
		JOIN pages p ON p.id = t.page_id
		WHERE t.token_hash = ? AND t.revoked_at IS NULL`,
		hashPagePublicToken(token)).Scan(&t.ID, &t.PageID, &t.WorkspaceID,
		&passwordHash, &expiresAt, &showProvenance, &lastSeen)
	if err != nil {
		return nil, err
	}
	t.PasswordHash = passwordHash.String
	t.ShowProvenance = showProvenance == 1
	t.LastSeenAt = lastSeen.String
	t.ExpiresAt = parsePageTime(expiresAt.String)

	// §7.3.2 rule 4. An unparseable expiry is treated as expired rather than as
	// absent: the column is NOT NULL by migration, so a value we cannot read is
	// a corrupted row, and the safe reading of a corrupted expiry is "over".
	if t.ExpiresAt.IsZero() || !t.ExpiresAt.After(now) {
		return nil, sql.ErrNoRows
	}
	return &t, nil
}

// ── 1. View — GET /api/v1/public/pages/{token} ─────────────────────────────

// View serves the public document, or asks for the password.
func (h *PagePublicHandler) View(w http.ResponseWriter, r *http.Request) {
	setPublicPageHeaders(w)
	token := r.PathValue("token")
	now := h.evaluator().Now()

	tok, err := h.resolvePublicToken(r.Context(), token, now)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, pagePublicUnavailable)
			return
		}
		replyInternalError(w, h.logger, "resolve public page token", err)
		return
	}

	// §7.3.2 rule 6 — the per-token cap. Keyed on the token id, never on the
	// token itself: a limiter map holding live credentials in process memory is
	// a credential store nobody meant to build.
	if !h.admitPublicRequest(w, h.views, tok.ID, now) {
		return
	}

	if tok.PasswordHash != "" {
		// A password-protected link says so and nothing else. It does not say
		// which page, who owns it, or when it expires — those are on the far
		// side of the password.
		//
		// This DOES tell a caller holding a valid token that the token is
		// valid, where an unknown one gets a 404. That asymmetry is deliberate
		// and is not the one §7.3.3 forbids: §7.3.3 is about the FAILURE of a
		// password attempt, and Unlock answers a wrong password and an unknown
		// token identically. Collapsing this branch into the 404 as well would
		// mean a reader whose link expired is shown a password box they can
		// never satisfy, and the disclosure it buys back is "a 256-bit random
		// string you already possess exists" — which is not a disclosure.
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"error":             "this link is protected by a password",
			"password_required": true,
		})
		return
	}

	h.servePublicPage(w, r, tok, now)
}

// ── 2. Unlock — POST /api/v1/public/pages/{token}/unlock ───────────────────

// Unlock verifies a password and serves the document (§7.3.3).
//
// The password arrives in a POST body and never in the URL, because a URL is
// written to every proxy log, every browser history and every Referer header
// between here and the reader. There is no cookie set on success either: §7.3.1
// says this surface shares no session with the app, and the honest reading of
// that is that it does not invent one of its own. The response IS the document;
// a reader who reloads is asked again.
func (h *PagePublicHandler) Unlock(w http.ResponseWriter, r *http.Request) {
	setPublicPageHeaders(w)
	token := r.PathValue("token")
	now := h.evaluator().Now()

	body, ok := readCapped(w, r, 4<<10, "unlock request")
	if !ok {
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if len(strings.TrimSpace(string(body))) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			replyError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
	}

	tok, resolveErr := h.resolvePublicToken(r.Context(), token, now)

	// The rate limit is applied BEFORE the password is checked and, for an
	// unknown token, against a key derived from the token hash rather than from
	// a row id — otherwise the one case an attacker is actually in (guessing)
	// would be the one case that is not limited. Both keys are namespaced so a
	// resolved token and an unresolved one can never collide.
	limitKey := "unknown:" + hashPagePublicToken(strings.TrimSpace(token))
	if tok != nil {
		limitKey = "token:" + tok.ID
	}
	if !h.admitPublicRequest(w, h.passwords, limitKey, now) {
		return
	}

	// §7.3.3: "the failure must not distinguish 'wrong password' from 'unknown
	// token'". That is two properties, not one — the same ANSWER and the same
	// COST. An unknown token still burns a bcrypt comparison against the
	// package's dummy hash, exactly as the sign-in path does for an unknown
	// email (lockout.go), so the two cases cannot be told apart by a clock
	// either.
	switch {
	case resolveErr != nil && !errors.Is(resolveErr, sql.ErrNoRows):
		replyInternalError(w, h.logger, "resolve public page token", resolveErr)
		return
	case tok == nil:
		_ = bcrypt.CompareHashAndPassword([]byte(dummyBcryptHash()), []byte(req.Password))
		h.replyPublicPasswordRefused(w)
		return
	case tok.PasswordHash == "":
		// No password on this link. Serving the page here would make "POST
		// unlock" a way to skip the GET's rate limit; refusing tells the caller
		// nothing it did not already know, since an unprotected link answers
		// the GET anyway.
		_ = bcrypt.CompareHashAndPassword([]byte(dummyBcryptHash()), []byte(req.Password))
		h.replyPublicPasswordRefused(w)
		return
	case bcrypt.CompareHashAndPassword([]byte(tok.PasswordHash), []byte(req.Password)) != nil:
		h.replyPublicPasswordRefused(w)
		return
	}

	// A correct password still spends a view from the per-token cap: the page
	// is about to be served, and it is the SERVING that rule 6 bounds.
	if !h.admitPublicRequest(w, h.views, tok.ID, now) {
		return
	}
	h.servePublicPage(w, r, tok, now)
}

// replyPublicPasswordRefused is the single exit for every password failure, so
// the two cases §7.3.3 requires to be indistinguishable are literally the same
// line of code.
func (h *PagePublicHandler) replyPublicPasswordRefused(w http.ResponseWriter) {
	writeJSON(w, http.StatusUnauthorized, map[string]any{
		"error":             pagePublicPasswordRefusal,
		"password_required": true,
	})
}

// ── Serialising a public page ──────────────────────────────────────────────

// servePublicPage builds and writes the document.
func (h *PagePublicHandler) servePublicPage(w http.ResponseWriter, r *http.Request, tok *publicToken, now time.Time) {
	var rec pageRecord
	err := h.db.QueryRowContext(r.Context(), `
		SELECT id, slug, name, COALESCE(description, '')
		FROM pages WHERE id = ?`, tok.PageID).Scan(&rec.ID, &rec.Slug, &rec.Name, &rec.Description)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, pagePublicUnavailable)
			return
		}
		replyInternalError(w, h.logger, "load public page", err)
		return
	}

	allowed, err := h.publicPanelIDs(r.Context(), rec.ID)
	if err != nil {
		replyInternalError(w, h.logger, "resolve public panels", err)
		return
	}
	panels, err := h.loadPanels(r.Context(), tok.WorkspaceID, rec.ID)
	if err != nil {
		replyInternalError(w, h.logger, "load public page panels", err)
		return
	}

	allowedSet := make(map[string]bool, len(allowed))
	for _, id := range allowed {
		allowedSet[id] = true
	}
	byID := make(map[string]*panelRecord, len(panels))
	for _, p := range panels {
		byID[p.PanelID] = p
	}

	out := pagePublicWire{
		Slug:           rec.Slug,
		Name:           rec.Name,
		Description:    rec.Description,
		Panels:         make([]pagePublicPanelWire, 0, len(allowed)),
		GeneratedAt:    now.UTC().Format(time.RFC3339),
		ShowProvenance: tok.ShowProvenance,
		ExpiresAt:      tok.ExpiresAt.UTC().Format(time.RFC3339),
	}
	// `allowed` is in spec order, so the public grid reads the way the author
	// arranged it. A panel that is marked public but has no row (a spec that
	// raced a reconcile) is simply not there — a public page does not carry
	// placeholders for panels it cannot show, because unlike the internal page
	// there is no second viewer whose layout it has to agree with.
	for _, id := range allowed {
		p, ok := byID[id]
		if !ok {
			continue
		}
		out.Panels = append(out.Panels, h.publicPanelWire(p, tok.ShowProvenance))
	}

	h.recordPublicView(r, tok, &rec, now)
	writeJSON(w, http.StatusOK, out)
}

// publicPanelWire is the additive serialiser. Every field is written here by
// hand; nothing is copied from pagePanelWire, and nothing is inherited.
func (h *PagePublicHandler) publicPanelWire(p *panelRecord, showProvenance bool) pagePublicPanelWire {
	v := h.verdict(p)
	out := pagePublicPanelWire{
		ID:     p.PanelID,
		Schema: p.Schema,
		Title:  p.Title,
		Span:   p.Span,
		State:  string(v.State),
	}
	if p.HasData {
		// The age, always — §7.3.2b, including on a failed panel.
		out.ProducedAt = p.ProducedAt.UTC().Format(time.RFC3339)
	}
	// The reason, never. v.Reason is deliberately read and discarded here so
	// the omission is visible at the one place somebody would otherwise "fix"
	// it by adding the field.
	_ = v.Reason

	if p.HasData && v.State != pages.StateFailed {
		out.Data = stripPublicPayloadActions(json.RawMessage(p.Payload))
	}
	if showProvenance && p.HasData {
		out.Provenance = &pageProvenance{
			Producer:   p.producerRef(),
			RunID:      pushReference(p),
			ProducedAt: p.ProducedAt.UTC().Format(time.RFC3339),
		}
	}
	return out
}

// stripPublicPayloadActions removes any action the payload carries, server-side
// and before serialisation (§7.3.2 rule 1).
//
// Two layers make rule 1 hold, and this is the second one. The first is that
// pagePublicPanelWire has no action field, so a PageAction declared in the page
// SPEC cannot cross this boundary at all. This layer covers the other door: a
// payload is producer-supplied JSON, and the day a schema admits richer content
// (narrative.v1, embed.v1 — reserved in the enum since the first migration) is
// the day a producer could push an action inside one. §8b.1 puts actions on the
// panel and never inside a table row, so the strip is at the payload's top
// level: a table CELL called "actions" is somebody's data and stays.
//
// A payload that is not a JSON object is returned untouched — there is nowhere
// in it for a key to hide.
func stripPublicPayloadActions(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return raw
	}
	stripped := false
	for k := range obj {
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "action", "actions":
			delete(obj, k)
			stripped = true
		}
	}
	if !stripped {
		return raw
	}
	out, err := json.Marshal(obj)
	if err != nil {
		// Unreachable: it decoded a moment ago. Returning an empty payload
		// rather than the original is the safe half of the branch — a panel
		// that renders nothing beats a panel that renders a button.
		return json.RawMessage(`{}`)
	}
	return out
}

// ── Headers (§7.3.2 rule 6, §7.3.4) ────────────────────────────────────────

// setPublicPageHeaders stamps every response this surface produces, success and
// refusal alike. A 404 for a mistyped token is as indexable as a 200 if nobody
// says otherwise.
//
//	X-Robots-Tag: noindex     rule 6, verbatim. nofollow and noarchive come
//	                          with it: a cached copy of a public page outlives
//	                          the expiry that rule 4 exists to enforce.
//	Referrer-Policy: no-referrer   rule 6, verbatim. The token IS the
//	                          credential and it is in the URL, so a Referer
//	                          header on any outbound click hands it to a third
//	                          party. This is the one header on this surface
//	                          that is not defence in depth.
//	Cache-Control: no-store   a shared cache holding a revoked page's last
//	                          render would answer after the revocation.
//	X-Frame-Options: DENY     §7.3.4 keeps third-party embedding out of 1.0.
//	                          The global middleware sets this already; it is
//	                          restated because "out of scope" and "prevented"
//	                          are different states and this surface needs the
//	                          second.
func setPublicPageHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
	h.Set("Pragma", "no-cache")
	h.Set("X-Frame-Options", "DENY")
}

// ── The once-a-day journal entry (§7.3.2 rule 6) ───────────────────────────

// recordPublicView writes at most one journal entry per token per day.
//
// "A journal entry for each token's first view per day so the owner can see the
// link is being used and by roughly whom." The bound is the point: a public
// page refreshed every thirty seconds would otherwise write 2 880 audit rows a
// day per reader, and an audit trail nobody can read is not one.
//
// The de-duplication is a CONDITIONAL UPDATE, not a read-then-write. SQLite has
// one writer, so `WHERE last_seen_at IS NULL OR last_seen_at < <start of today>`
// admits exactly one of any number of concurrent views, and the row it admits
// is the one that emits. A read-then-write would emit N times for N concurrent
// first views, which is the failure this bound exists to prevent.
func (h *PagePublicHandler) recordPublicView(r *http.Request, tok *publicToken, rec *pageRecord, now time.Time) {
	startOfDay := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)
	res, err := h.db.ExecContext(r.Context(), `
		UPDATE page_public_tokens
		   SET last_seen_at = ?
		 WHERE id = ? AND (last_seen_at IS NULL OR last_seen_at < ?)`,
		now.UTC().Format(time.RFC3339), tok.ID, startOfDay.Format(time.RFC3339))
	if err != nil {
		if h.logger != nil {
			h.logger.Warn("pages: public view was not recorded", "page", rec.Slug, "error", err)
		}
		return
	}
	affected, err := res.RowsAffected()
	if err != nil || affected == 0 {
		return // already seen today; the entry was written by the first view
	}
	if h.journal == nil {
		return
	}
	if _, err := h.journal.Emit(r.Context(), journal.Entry{
		WorkspaceID: tok.WorkspaceID,
		Type:        journalPagePublicView,
		Severity:    journal.SeverityInfo,
		// There is no actor. A public reader has no account, no agent identity
		// and no session, and inventing one would be the first lie in an audit
		// trail whose whole value is that it does not tell any.
		ActorType: journal.ActorSystem,
		Summary: fmt.Sprintf("public link on page %s was viewed (first view today, from %s)",
			rec.Slug, publicViewerHint(r)),
		Payload: map[string]any{
			"page":        rec.Slug,
			"page_id":     rec.ID,
			"token_id":    tok.ID,
			"viewer_hint": publicViewerHint(r),
		},
	}); err != nil && h.logger != nil {
		h.logger.Warn("pages: public view was not journalled", "page", rec.Slug, "error", err)
	}
}

// publicViewerHint is "roughly whom", and roughly is the whole design.
//
// The network prefix — /24 for IPv4, /48 for IPv6 — tells an owner "this is
// being read from the accountant's office" and "this is being read from three
// different countries", which are the two questions rule 6 is for. The full
// address would identify a person who never agreed to be identified by us, and
// the User-Agent would fingerprint them; neither answers a question the owner
// asked.
func publicViewerHint(r *http.Request) string {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		host = h
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	if ip == nil {
		return "an unrecorded address"
	}
	if v4 := ip.To4(); v4 != nil {
		return fmt.Sprintf("%d.%d.%d.0/24", v4[0], v4[1], v4[2])
	}
	masked := ip.Mask(net.CIDRMask(48, 128))
	return masked.String() + "/48"
}

// ── Rate limiting (§7.3.2 rule 6, §10b.3, §7.3.3) ──────────────────────────

const (
	// pagePublicPasswordAttemptsPerHour is §7.3.3's "rate limited per token".
	// It is a Go constant rather than a Settings knob on purpose: the view rate
	// is operational and gets tuned by whoever runs the instance, but the
	// number of password guesses a link tolerates is a security property, and a
	// brute-force allowance that varies per deployment is one that is raised
	// once during an incident and never lowered.
	pagePublicPasswordAttemptsPerHour = 20
	// pagePublicPasswordBurst lets a reader fat-finger a password a few times
	// in a row without waiting three minutes between attempts.
	pagePublicPasswordBurst = 5

	// pagePublicBucketTTL drops a bucket untouched for this long. A bucket
	// reconstructed after it is full anyway, so dropping it changes no
	// decision — and without a sweep, a caller walking random tokens would grow
	// the map for as long as the process lives.
	pagePublicBucketTTL = time.Hour
)

// configuredPublicViewsPerHour reads §7.3.2 rule 6's number from the registry,
// which is what config/rate-limits.yml's PAGE_PUBLIC_VIEW entry and the admin
// "Rate Limiters" console both describe. The default is 600/h.
func configuredPublicViewsPerHour() int {
	n := ratelimitcfg.Int(ratelimitcfg.KeyPagesPublicViewPerHr)
	if n < 1 {
		n = 1
	}
	return n
}

// pagePublicLimiter is one token bucket per key, refilling per HOUR.
//
// Per-process by construction, like every other limiter in this codebase
// (config/rate-limits.yml: "MVP: per-process, neskaluje přes více instancí").
// With N replicas the effective cap is N×. That is acceptable here in a way it
// is not for the panel push path — a view is a read, it takes no write lock,
// and the cap exists to bound the value of a leaked link rather than to protect
// the database.
type pagePublicLimiter struct {
	mu      sync.Mutex
	perHour func() int
	burst   int
	buckets map[string]*pagePublicBucket
	swept   time.Time
}

type pagePublicBucket struct {
	lim      *rate.Limiter
	lastSeen time.Time
	perHour  int
}

// newPagePublicLimiter builds a limiter. burst 0 means "burst equals the hourly
// rate", which is what a view limit wants: 600 views in the first minute and
// then a wait is the same budget as 10 a minute for an hour, and the first
// shape is what a person clicking around actually produces.
func newPagePublicLimiter(perHour func() int, burst int) *pagePublicLimiter {
	return &pagePublicLimiter{
		perHour: perHour,
		burst:   burst,
		buckets: map[string]*pagePublicBucket{},
	}
}

// Allow reports whether one request may proceed, and how long to wait if not.
func (l *pagePublicLimiter) Allow(now time.Time, key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked(now)

	perHour := l.perHour()
	if perHour < 1 {
		perHour = 1
	}
	burst := l.burst
	if burst < 1 {
		burst = perHour
	}

	b, ok := l.buckets[key]
	if !ok {
		b = &pagePublicBucket{lim: rate.NewLimiter(rate.Limit(float64(perHour)/3600.0), burst), perHour: perHour}
		l.buckets[key] = b
	} else if b.perHour != perHour {
		// The registry moved under us — an operator retuned the limit in
		// Settings. Retune the live bucket rather than waiting for it to be
		// swept, or the change reaches only links nobody is reading.
		b.perHour = perHour
		b.lim.SetLimit(rate.Limit(float64(perHour) / 3600.0))
		b.lim.SetBurst(burst)
	}
	b.lastSeen = now

	res := b.lim.ReserveN(now, 1)
	if !res.OK() {
		return false, time.Hour
	}
	if d := res.DelayFrom(now); d > 0 {
		res.CancelAt(now)
		return false, d
	}
	return true, 0
}

func (l *pagePublicLimiter) sweepLocked(now time.Time) {
	if now.Sub(l.swept) < pagePublicBucketTTL {
		return
	}
	l.swept = now
	cutoff := now.Add(-pagePublicBucketTTL)
	for k, b := range l.buckets {
		if b.lastSeen.Before(cutoff) {
			delete(l.buckets, k)
		}
	}
}

// admitPublicRequest applies a limiter and writes the 429 if it refuses.
//
// 429 + Retry-After is the pattern the rest of the product uses
// (pipelines_exec.go:211-218, and the Pages push path), so a client that
// already knows how to back off does not need to learn a second convention.
func (h *PagePublicHandler) admitPublicRequest(w http.ResponseWriter, l *pagePublicLimiter, key string, now time.Time) bool {
	ok, wait := l.Allow(now, key)
	if ok {
		return true
	}
	w.Header().Set("Retry-After", fmt.Sprintf("%d", pages.RetryAfterSeconds(wait)))
	writeJSON(w, http.StatusTooManyRequests, map[string]any{
		"error": "this link has been opened too many times recently; try again shortly",
	})
	return false
}
