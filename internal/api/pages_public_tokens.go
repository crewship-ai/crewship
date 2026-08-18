package api

// Public pages — publishing, listing and revoking the links (docs/prd/pages.md
// §7.3).
//
// This file is the AUTHENTICATED half of the public surface: the three verbs a
// human uses to mint a link, see which links exist, and take one back. The
// unauthenticated half — what somebody with no account actually receives — is
// pages_public.go, and the two are deliberately separate files for the same
// reason §7.3.1 gives for the separate URL space: "it is a distinct rendering
// path with its own middleware, and that separation is what makes it
// auditable".
//
// Four of §7.3.2's six rules are decided here rather than at view time, because
// a rule enforced at mint time cannot be forgotten on a code path somebody adds
// later:
//
//	rule 2  opt-in per PANEL. Publishing a page with no panel marked public is
//	        refused, so "publish" can never be a bulk action over panels the
//	        author has not looked at.
//	rule 3  only a HUMAN publishes. An agent-originated request is refused here
//	        and page_public_tokens.created_by_user_id is NOT NULL by migration,
//	        so the schema refuses the row even if this check were bypassed.
//	rule 4  every link EXPIRES. Default 30 days, maximum 1 year, and there is no
//	        spelling of the request that means "never".
//	§7.3.3  the password is hashed with the primitives the auth layer already
//	        uses (bcrypt at the same cost as every other password in this
//	        codebase) and is never reversible and never in a URL.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pages"
)

// ── The two constants §7.3.2 rule 4 asks for ───────────────────────────────

const (
	// PagePublicTokenDefaultTTL is what a publish that names no expiry gets.
	// §7.3.2 rule 4: "A required expiry, default 30 days, maximum 1 year."
	//
	// "Required" is the load-bearing word and it is why there is a DEFAULT at
	// all rather than a mandatory flag: a link with no expiry is the one that
	// is still live when nobody remembers it exists, and the way to make sure
	// every link has one is to make the absent case a date rather than an
	// error somebody works around by typing a large number.
	PagePublicTokenDefaultTTL = 30 * 24 * time.Hour

	// PagePublicTokenMaxTTL is the ceiling. A year is long enough for the
	// accountant who looks once a quarter and short enough that a forgotten
	// link dies within one audit cycle.
	PagePublicTokenMaxTTL = 365 * 24 * time.Hour

	// pagePublicTokenBytes is the entropy behind one link. 32 bytes = 256
	// bits: holding the token IS the authorisation (§7.3.1), so the token has
	// to be unguessable in the same sense a session id is, and the URL is the
	// only credential the reader will ever have.
	pagePublicTokenBytes = 32

	// pagePublicPasswordMaxBytes bounds a submitted password. bcrypt silently
	// truncates past 72 bytes, so a longer one would hash to the same value as
	// its prefix — accepting it would be storing a password the holder does not
	// have. users_me.go refuses for the same reason.
	pagePublicPasswordMaxBytes = 72

	// pagePublicPasswordMinBytes is the floor for a password that is meant to
	// be a control rather than a decoration. Nothing else in the product is
	// protected by a string this short, and the whole point of §7.3.3 is a
	// second factor on a link that is already a credential.
	pagePublicPasswordMinBytes = 8
)

// ── Wire ───────────────────────────────────────────────────────────────────

// pagePublishRequest is the publish body.
//
// There is deliberately no `token` field: a caller does not get to choose the
// secret. There is no `never_expires`, no `expires_at`, and no negative
// `expires_in_days` — the only thing a publisher may express about expiry is
// how many days, bounded at both ends.
type pagePublishRequest struct {
	ExpiresInDays  *int    `json:"expires_in_days"`
	Password       *string `json:"password"`
	ShowProvenance *bool   `json:"show_provenance"`
}

// pagePublicTokenWire is one link as the owner sees it.
//
// Token is populated on exactly one response — the 201 from publish. Every
// later read returns the row without it, because the column holds a hash and
// there is nothing to return: a link the server could show you again is a link
// the server is storing in the clear.
type pagePublicTokenWire struct {
	ID             string `json:"id"`
	Token          string `json:"token,omitempty"`
	URL            string `json:"url,omitempty"`
	ExpiresAt      string `json:"expires_at"`
	ShowProvenance bool   `json:"show_provenance"`
	HasPassword    bool   `json:"has_password"`
	CreatedBy      string `json:"created_by"`
	CreatedAt      string `json:"created_at"`
	RevokedAt      string `json:"revoked_at,omitempty"`
	LastSeenAt     string `json:"last_seen_at,omitempty"`
	// Live is the verdict the owner actually wants: not revoked and not yet
	// expired. Two columns and a clock is a calculation every reader would
	// otherwise have to repeat, and one of them would get it wrong.
	Live bool `json:"live"`
	// Panels are the panel ids this link exposes — the human-attested set
	// (§7.3.2 rules 2 and 3), so "what does this link show" is answerable
	// without reading the spec.
	Panels []string `json:"panels"`
}

// pagePublicTokensWire is the list envelope.
type pagePublicTokensWire struct {
	Page   string                `json:"page"`
	Tokens []pagePublicTokenWire `json:"tokens"`
}

// ── Token minting ──────────────────────────────────────────────────────────

// mintPagePublicToken returns a fresh token and the hash to store.
//
// base64url without padding, so the token is a single URL path segment that
// survives copy-paste out of an email client. The stored form is SHA-256 and
// never the token: the shape is copied from pipeline_webhooks and cli_tokens,
// which have hashed tokens at rest since #1888. bcrypt is deliberately NOT
// used here — it is the right primitive for a low-entropy human password and
// the wrong one for a 256-bit random string, where it would only make every
// public view pay ~250 ms for no gain in guessing resistance.
func mintPagePublicToken() (token, hash string, err error) {
	buf := make([]byte, pagePublicTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("generate public page token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(buf)
	return token, hashPagePublicToken(token), nil
}

// hashPagePublicToken is the lookup key for /p/{token}.
func hashPagePublicToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ── 1. Publish — POST /api/v1/pages/{slug}/public ──────────────────────────

// Publish mints a public link for a page.
//
// The order of the checks is the order of §7.3.2: is the caller human, may they
// publish this page at all, is anything actually marked public, and is the
// expiry inside its bounds. Each refusal names its rule, because the person
// reading it is deciding whether to argue with the product or fix their spec.
func (h *PageHandler) Publish(w http.ResponseWriter, r *http.Request) {
	setPublicPageHeaders(w)

	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())
	slug := r.PathValue("slug")

	// §7.3.2 rule 3 — only a human publishes. The same positive test for a
	// human credential that guards a grant (§7.1b rule 1), reused rather than
	// re-derived: two spellings of "is this an agent" is one spelling too many,
	// and the one that drifts is the one nobody is reading. "Public" is the
	// widest reach there is, so if an agent may not widen a page by one grant
	// it certainly may not widen it to everybody.
	if isAgent, _ := pageGrantCallerIsAgent(r); isAgent {
		replyError(w, http.StatusForbidden,
			"only a human may publish a page (§7.3.2 rule 3); an agent may build the page and may not make it public — "+
				"ask a human to run `crewship page publish`")
		return
	}

	rec, err := h.loadPage(r.Context(), wsID, slug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, fmt.Sprintf("page %q not found", slug))
			return
		}
		replyInternalError(w, h.logger, "load page for publish", err)
		return
	}
	// Publishing is at least as consequential as deleting, so it takes the
	// delete gate and not the edit gate: owner or workspace ADMIN/OWNER. A
	// `write` grantee rearranges the page — §7.1b rule 2 makes that authority
	// over arrangement, never over reach — and a write grant may be held by an
	// agent, which rule 3 has just refused.
	if !h.isPageOwner(r.Context(), wsID, user.ID, rec) && !canRole(RoleFromContext(r.Context()), "manage") {
		replyError(w, http.StatusForbidden, "only the page owner or a workspace admin may publish this page")
		return
	}

	req, ok := decodePagePublishRequest(w, r)
	if !ok {
		return
	}

	expiresAt, ok := pagePublicExpiry(w, req.ExpiresInDays, h.evaluator().Now())
	if !ok {
		return
	}

	// §7.3.2 rule 2 — opt-in per panel, and default deny. A page with nothing
	// marked public produces a link to an empty page, which reads to the
	// recipient as a broken product and to the publisher as a job done. Refuse
	// it and say which knob is missing.
	public, err := h.publicPanelIDs(r.Context(), rec.ID)
	if err != nil {
		replyInternalError(w, h.logger, "resolve public panels", err)
		return
	}
	if len(public) == 0 {
		replyError(w, http.StatusBadRequest,
			"no panel on this page is marked `public: true`, so publishing it would expose nothing (§7.3.2 rule 2: "+
				"publishing is opt-in per panel and never a bulk action over panels nobody looked at)")
		return
	}

	passwordHash := ""
	if req.Password != nil && strings.TrimSpace(*req.Password) != "" {
		hash, ok := hashPagePublicPassword(w, *req.Password)
		if !ok {
			return
		}
		passwordHash = hash
	}

	showProvenance := false
	if req.ShowProvenance != nil {
		showProvenance = *req.ShowProvenance
	}

	token, tokenHash, err := mintPagePublicToken()
	if err != nil {
		replyInternalError(w, h.logger, "mint public page token", err)
		return
	}

	tokenID := generateCUID()
	now := h.evaluator().Now().UTC().Format(time.RFC3339)
	if _, err := h.db.ExecContext(r.Context(), `
		INSERT INTO page_public_tokens
			(id, page_id, token_hash, password_hash, expires_at, show_provenance, created_by_user_id, created_at)
		VALUES (?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?)`,
		tokenID, rec.ID, tokenHash, passwordHash, expiresAt.UTC().Format(time.RFC3339),
		boolToInt(showProvenance), user.ID, now); err != nil {
		replyInternalError(w, h.logger, "insert public page token", err)
		return
	}

	h.journalPublicLink(r.Context(), journalPagePublished, wsID, rec, user.ID, map[string]any{
		"token_id":        tokenID,
		"expires_at":      expiresAt.UTC().Format(time.RFC3339),
		"has_password":    passwordHash != "",
		"show_provenance": showProvenance,
		"panels":          public,
	}, fmt.Sprintf("%s published page %s to %d panel(s) until %s",
		user.Email, rec.Slug, len(public), expiresAt.UTC().Format(time.RFC3339)))

	// The token is returned HERE and nowhere else, ever again.
	writeJSON(w, http.StatusCreated, pagePublicTokenWire{
		ID:             tokenID,
		Token:          token,
		URL:            PagePublicPath(token),
		ExpiresAt:      expiresAt.UTC().Format(time.RFC3339),
		ShowProvenance: showProvenance,
		HasPassword:    passwordHash != "",
		CreatedBy:      user.Email,
		CreatedAt:      now,
		Live:           true,
		Panels:         public,
	})
}

// PagePublicPath is the one place the public URL shape is written down.
// §7.3.1: a separate URL space that shares no session, no cookie and no
// workspace context with the app.
func PagePublicPath(token string) string { return "/p/" + token }

// decodePagePublishRequest reads the publish body under a small cap. There is
// nothing large in it — three scalars — and an unbounded decode on a route that
// mints a credential is not a shape worth having.
func decodePagePublishRequest(w http.ResponseWriter, r *http.Request) (*pagePublishRequest, bool) {
	body, ok := readCapped(w, r, 8<<10, "publish request")
	if !ok {
		return nil, false
	}
	req := &pagePublishRequest{}
	if len(strings.TrimSpace(string(body))) == 0 {
		return req, true
	}
	if err := json.Unmarshal(body, req); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return nil, false
	}
	return req, true
}

// pagePublicExpiry turns the requested day count into an instant, or refuses.
//
// §7.3.2 rule 4 in one function: absent means the default, zero and negative
// are refused rather than read as "never", and anything past a year is refused
// with the number that would have been accepted.
func pagePublicExpiry(w http.ResponseWriter, days *int, now time.Time) (time.Time, bool) {
	ttl := PagePublicTokenDefaultTTL
	if days != nil {
		if *days <= 0 {
			replyError(w, http.StatusBadRequest,
				"expires_in_days must be at least 1: every public link expires (§7.3.2 rule 4), and there is no value here that means 'never'")
			return time.Time{}, false
		}
		// Bound the DAYS before converting. time.Duration is int64 nanoseconds,
		// so *days * 24h overflows past ~106 751 days and wraps NEGATIVE — the
		// ceiling check below then passes, and now.Add(negative) mints a 201
		// for a link that expired before it was created.
		if maxDays := int(PagePublicTokenMaxTTL / (24 * time.Hour)); *days > maxDays {
			replyError(w, http.StatusBadRequest, fmt.Sprintf(
				"expires_in_days is %d; the maximum is %d (one year, §7.3.2 rule 4)", *days, maxDays))
			return time.Time{}, false
		}
		ttl = time.Duration(*days) * 24 * time.Hour
		if ttl > PagePublicTokenMaxTTL {
			replyError(w, http.StatusBadRequest, fmt.Sprintf(
				"expires_in_days is %d; the maximum is %d (one year, §7.3.2 rule 4)",
				*days, int(PagePublicTokenMaxTTL/(24*time.Hour))))
			return time.Time{}, false
		}
	}
	return now.Add(ttl), true
}

// hashPagePublicPassword applies §7.3.3: "stored hashed with the same
// primitives the auth layer already uses". That is bcrypt at cost 12 — the
// value signup, bootstrap, recovery and the profile password change all use —
// so a public page's password is exactly as expensive to guess as an account's.
func hashPagePublicPassword(w http.ResponseWriter, password string) (string, bool) {
	if len(password) < pagePublicPasswordMinBytes {
		replyError(w, http.StatusBadRequest, fmt.Sprintf(
			"the password must be at least %d characters; a shorter one is decoration on a link that is already a credential",
			pagePublicPasswordMinBytes))
		return "", false
	}
	if len(password) > pagePublicPasswordMaxBytes {
		replyError(w, http.StatusBadRequest, fmt.Sprintf(
			"the password must be at most %d bytes — bcrypt truncates past that, so a longer one would not be the password you set",
			pagePublicPasswordMaxBytes))
		return "", false
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), profileBcryptCost)
	if err != nil {
		replyError(w, http.StatusBadRequest, "that password could not be stored")
		return "", false
	}
	return string(hash), true
}

// ── 2. List — GET /api/v1/pages/{slug}/public ──────────────────────────────

// ListPublicLinks shows the owner which links exist and which are still live.
//
// §7.3.2 rule 4 gives a page several tokens on purpose — "revoking the
// accountant's link does not break the client's" — which is only usable if the
// owner can tell them apart. None of them carries a token value: the column is
// a hash and there is nothing to show.
func (h *PageHandler) ListPublicLinks(w http.ResponseWriter, r *http.Request) {
	setPublicPageHeaders(w)

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
		replyInternalError(w, h.logger, "load page for public links", err)
		return
	}
	if !h.isPageOwner(r.Context(), wsID, user.ID, rec) && !canRole(RoleFromContext(r.Context()), "manage") {
		replyError(w, http.StatusForbidden, "only the page owner or a workspace admin may see this page's public links")
		return
	}

	public, err := h.publicPanelIDs(r.Context(), rec.ID)
	if err != nil {
		replyInternalError(w, h.logger, "resolve public panels", err)
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT t.id, t.expires_at, t.show_provenance, t.password_hash IS NOT NULL,
		       COALESCE(u.email, ''), t.created_at,
		       COALESCE(t.revoked_at, ''), COALESCE(t.last_seen_at, '')
		FROM page_public_tokens t
		LEFT JOIN users u ON u.id = t.created_by_user_id
		WHERE t.page_id = ?
		ORDER BY t.created_at DESC, t.id ASC`, rec.ID)
	if err != nil {
		replyInternalError(w, h.logger, "list public page tokens", err)
		return
	}
	defer rows.Close()

	now := h.evaluator().Now()
	out := pagePublicTokensWire{Page: rec.Slug, Tokens: []pagePublicTokenWire{}}
	for rows.Next() {
		var t pagePublicTokenWire
		var showProvenance, hasPassword int
		if err := rows.Scan(&t.ID, &t.ExpiresAt, &showProvenance, &hasPassword,
			&t.CreatedBy, &t.CreatedAt, &t.RevokedAt, &t.LastSeenAt); err != nil {
			replyInternalError(w, h.logger, "scan public page token", err)
			return
		}
		t.ShowProvenance = showProvenance == 1
		t.HasPassword = hasPassword == 1
		t.Live = t.RevokedAt == "" && parsePageTime(t.ExpiresAt).After(now)
		// A live link shows what it currently exposes; a dead one shows
		// nothing, because it exposes nothing.
		if t.Live {
			t.Panels = public
		} else {
			t.Panels = []string{}
		}
		out.Tokens = append(out.Tokens, t)
	}
	if err := rows.Err(); err != nil {
		replyInternalError(w, h.logger, "rows iteration (public page tokens)", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ── 3. Revoke — DELETE /api/v1/pages/{slug}/public/{tokenId} ───────────────

// RevokePublicLink takes one link back.
//
// Individually revocable is half of §7.3.2 rule 4, and the reason the id is in
// the path rather than a body: revoking is the emergency verb, and an emergency
// verb whose target is buried in a JSON body is one somebody gets wrong.
//
// The row is marked revoked rather than deleted. `revoked_at` plus
// `last_seen_at` is the answer to "was it used after we pulled it", and a
// deleted row cannot answer that.
func (h *PageHandler) RevokePublicLink(w http.ResponseWriter, r *http.Request) {
	setPublicPageHeaders(w)

	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())
	slug := r.PathValue("slug")
	tokenID := r.PathValue("tokenId")

	rec, err := h.loadPage(r.Context(), wsID, slug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, fmt.Sprintf("page %q not found", slug))
			return
		}
		replyInternalError(w, h.logger, "load page for revoke", err)
		return
	}
	// Revoking is NOT gated on being human. An agent that notices a leaked link
	// should be able to close it: rule 3 forbids WIDENING reach, and narrowing
	// it is the opposite move. The owner gate still applies.
	if !h.isPageOwner(r.Context(), wsID, user.ID, rec) && !canRole(RoleFromContext(r.Context()), "manage") {
		replyError(w, http.StatusForbidden, "only the page owner or a workspace admin may revoke this page's public links")
		return
	}

	now := h.evaluator().Now().UTC().Format(time.RFC3339)
	res, err := h.db.ExecContext(r.Context(), `
		UPDATE page_public_tokens
		   SET revoked_at = ?
		 WHERE id = ? AND page_id = ? AND revoked_at IS NULL`, now, tokenID, rec.ID)
	if err != nil {
		replyInternalError(w, h.logger, "revoke public page token", err)
		return
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		// Already revoked, or never belonged to this page. Both are "there is
		// nothing live here with that id", and the caller's next action is the
		// same either way.
		var exists int
		_ = h.db.QueryRowContext(r.Context(),
			`SELECT 1 FROM page_public_tokens WHERE id = ? AND page_id = ?`, tokenID, rec.ID).Scan(&exists)
		if exists == 1 {
			writeJSON(w, http.StatusOK, map[string]any{"id": tokenID, "revoked": true, "already": true})
			return
		}
		replyError(w, http.StatusNotFound, fmt.Sprintf("page %q has no public link with id %q", slug, tokenID))
		return
	}

	h.journalPublicLink(r.Context(), journalPageLinkRevoked, wsID, rec, user.ID,
		map[string]any{"token_id": tokenID},
		fmt.Sprintf("%s revoked a public link on page %s", user.Email, rec.Slug))

	writeJSON(w, http.StatusOK, map[string]any{"id": tokenID, "revoked": true})
}

// ── The human-attested public panel set ────────────────────────────────────

// publicPanelIDs is §7.3.2 rules 2 and 3 as one query pair, and it is the only
// function in the codebase that decides what a public link exposes.
//
// A panel is public when BOTH are true:
//
//  1. the CURRENT spec marks it `public: true` — rule 2, opt-in per panel,
//     default deny;
//  2. the newest HUMAN-authored version of the spec marks it public too.
//
// The second condition is rule 3's second half — "[an agent] cannot add a panel
// to an already-public page without that panel being separately marked by a
// human" — and it is enforced at read time rather than at write time on
// purpose. A write-time check would have to be remembered by every future code
// path that can save a spec (the editor, import, rollback, an agent through the
// sidecar); a read-time check is enforced by the one function that answers
// "what does this link show", which is a place nobody can route around. If an
// agent rewrites the page and marks a new panel public, the panel appears on
// the INTERNAL page immediately and on the public one not at all, until a human
// saves the spec — at which point they have, by definition, marked it
// themselves.
//
// A page with no human-authored version exposes nothing, which is the same rule
// read from the other end.
func (h *PageHandler) publicPanelIDs(ctx context.Context, pageID string) ([]string, error) {
	// The live spec, read once — it supplies both the marked set AND the ORDER,
	// so the public grid reads the way the author arranged it rather than the
	// way a map iterated.
	current, err := h.specPanelPublicSet(ctx, `SELECT spec_json FROM pages WHERE id = ?`, pageID)
	if err != nil {
		return nil, err
	}
	if len(current.public) == 0 {
		return nil, nil
	}
	attested, err := h.specPanelPublicSet(ctx, `
		SELECT spec_json FROM page_versions
		 WHERE page_id = ? AND author_user_id IS NOT NULL
		 ORDER BY seq DESC LIMIT 1`, pageID)
	if err != nil {
		return nil, err
	}

	// Who attested, and what they were allowed to look at.
	//
	// The intersection above answers "did a human mark this public". It does
	// not answer "was that human allowed to SEE it", and those are different
	// questions because mayEditSpec is page-level: a MEMBER holding `write` may
	// edit a page carrying a panel that is sealed to them. Marking that panel
	// public served its payload to anyone holding the link, with no auth —
	// the seal defeated by the one path that leads outside the workspace.
	//
	// A panel nobody who could see it has published is not published.
	visible, err := h.attesterVisiblePanels(ctx, pageID)
	if err != nil {
		return nil, err
	}

	out := make([]string, 0, len(current.public))
	for _, id := range current.order {
		if current.public[id] && attested.public[id] && visible[id] {
			out = append(out, id)
		}
	}
	return out, nil
}

// attesterVisiblePanels answers, per panel, whether the human who PUBLISHED it
// could see it.
//
// The first version of this asked a simpler question — could the author of the
// page's newest human version see the panel — and that was wrong in a way that
// only shows up later. `pagePatchBody` drops `panels`, so renaming a page
// leaves every panel's `public: true` in place while making the renamer the
// newest author. A MEMBER with a `write` grant renaming a page would therefore
// have silently unpublished a panel an admin had published weeks earlier, with
// nothing said to anyone and a live external link going blank.
//
// So the attester for a panel is the author of the version in which that panel
// BECAME public — found by walking human-authored versions newest-first while
// the flag holds, and taking the author of the oldest one in that unbroken run.
// Publishing is a per-panel human act (§7.3.2 rule 2); who may vouch for it is
// therefore also per-panel.
//
// Membership is read as it stands NOW rather than as it stood at the save: an
// author who has since left the crew stops publishing its panel, which fails
// closed and matches §7.1b's use-time narrowing of grants.
func (h *PageHandler) attesterVisiblePanels(ctx context.Context, pageID string) (map[string]bool, error) {
	var wsID string
	if err := h.db.QueryRowContext(ctx,
		`SELECT workspace_id FROM pages WHERE id = ?`, pageID).Scan(&wsID); err != nil {
		return nil, err
	}

	rows, err := h.db.QueryContext(ctx, `
		SELECT v.author_user_id, v.spec_json
		  FROM page_versions v
		 WHERE v.page_id = ? AND v.author_user_id IS NOT NULL
		 ORDER BY v.seq DESC`, pageID)
	if err != nil {
		return nil, err
	}
	type humanVersion struct {
		author string
		public map[string]bool
	}
	var versions []humanVersion
	for rows.Next() {
		var author, specJSON string
		if err := rows.Scan(&author, &specJSON); err != nil {
			_ = rows.Close()
			return nil, err
		}
		versions = append(versions, humanVersion{author: author, public: publicSetFromSpecJSON(specJSON)})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()
	if len(versions) == 0 {
		return map[string]bool{}, nil
	}

	panels, err := h.loadPanels(ctx, wsID, pageID)
	if err != nil {
		return nil, err
	}

	viewers := map[string]*pageViewer{}
	out := make(map[string]bool, len(panels))
	for _, p := range panels {
		// Walk back while this panel is public; the last one still marking it
		// is where it was published.
		publisher := ""
		for _, v := range versions {
			if !v.public[p.PanelID] {
				break
			}
			publisher = v.author
		}
		if publisher == "" {
			continue
		}
		viewer, ok := viewers[publisher]
		if !ok {
			viewer, err = h.publishingViewer(ctx, wsID, publisher)
			if err != nil {
				return nil, err
			}
			viewers[publisher] = viewer
		}
		// nil viewer = the publisher has left the workspace. Fail closed.
		out[p.PanelID] = viewer != nil && h.canSeePanel(viewer, p)
	}
	return out, nil
}

// publishingViewer builds the viewer for a publisher, with the role read from
// the workspace rather than from the ambient request context.
//
// loadViewer fills Role from RoleFromContext — whoever is ASKING — which here
// would apply the current caller's role to a different person, and is wrong in
// both directions: an admin opening a public link would publish panels its
// author could never see, and an anonymous reader would unpublish panels the
// author legitimately marked. Returns nil when they are no longer a member.
func (h *PageHandler) publishingViewer(ctx context.Context, wsID, userID string) (*pageViewer, error) {
	var role string
	err := h.db.QueryRowContext(ctx,
		`SELECT role FROM workspace_members WHERE workspace_id = ? AND user_id = ?`,
		wsID, userID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v := &pageViewer{UserID: userID, Role: role, Crews: map[string]bool{}}
	rows, err := h.db.QueryContext(ctx, `
		SELECT cm.crew_id FROM crew_members cm
		  JOIN crews c ON c.id = cm.crew_id
		 WHERE cm.user_id = ? AND c.workspace_id = ? AND c.deleted_at IS NULL`, userID, wsID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		v.Crews[id] = true
	}
	return v, rows.Err()
}

// publicSetFromSpecJSON reads which panels one stored spec marks public.
func publicSetFromSpecJSON(specJSON string) map[string]bool {
	out := map[string]bool{}
	var doc struct {
		Spec struct {
			Panels []struct {
				ID     string `json:"id"`
				Public bool   `json:"public"`
			} `json:"panels"`
		} `json:"spec"`
	}
	if err := json.Unmarshal([]byte(specJSON), &doc); err != nil {
		return out
	}
	for _, p := range doc.Spec.Panels {
		if p.Public {
			out[p.ID] = true
		}
	}
	return out
}

// specPublicPanels is one spec's answer to "which panels are marked public",
// plus the order they are declared in.
type specPublicPanels struct {
	public map[string]bool
	order  []string
}

// specPanelPublicSet runs a query returning one spec_json and reads it.
//
// A query returning no row yields an empty set, never an error: a page with no
// human-authored version has attested nothing, which is a decision (rule 3
// again — nothing is public until a human saves a spec saying so) and not a
// failure.
func (h *PageHandler) specPanelPublicSet(ctx context.Context, query, pageID string) (specPublicPanels, error) {
	out := specPublicPanels{public: map[string]bool{}}
	var specJSON string
	err := h.db.QueryRowContext(ctx, query, pageID).Scan(&specJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	var doc pages.Document
	if err := json.Unmarshal([]byte(specJSON), &doc); err != nil {
		return out, err
	}
	for _, p := range doc.Spec.Panels {
		out.order = append(out.order, p.ID)
		if p.Public {
			out.public[p.ID] = true
		}
	}
	return out, nil
}

// ── Journal ────────────────────────────────────────────────────────────────

// The three entry types this surface writes. Declared here rather than in
// internal/journal because unknown types are forwarded by design
// (journal.IsFeedRelevant denylists high-volume telemetry and passes everything
// else), so a new human-facing type reaches the activity feed with no change
// there.
const (
	journalPagePublished   = journal.EntryType("page.published")
	journalPageLinkRevoked = journal.EntryType("page.link_revoked")
	// journalPagePublicView is §7.3.2 rule 6's "a journal entry for each
	// token's first view per day", written in pages_public.go.
	journalPagePublicView = journal.EntryType("page.public_view")
)

func (h *PageHandler) journalPublicLink(ctx context.Context, entryType journal.EntryType,
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
		// notice, not info: publishing a page outside the workspace is the
		// widest reach the product has, and it should be visible in a feed
		// somebody skims.
		Severity:  journal.SeverityNotice,
		ActorType: journal.ActorUser,
		ActorID:   actorID,
		Summary:   summary,
		Payload:   payload,
	}); err != nil && h.logger != nil {
		h.logger.Warn("pages: public link change was not journalled",
			"page", rec.Slug, "type", string(entryType), "error", err)
	}
}
