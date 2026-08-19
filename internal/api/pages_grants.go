package api

// Pages — the grants surface (docs/prd/pages.md §7.1 rule 3, §7.1b, §11).
//
//	GET    /api/v1/pages/{slug}/grants
//	PUT    /api/v1/pages/{slug}/grants
//	DELETE /api/v1/pages/{slug}/grants
//
// Until this file existed, page_grants was honoured on read and writable only
// by hand in SQL — which made the whole multi-agent permission model of §7.1b
// unreachable from the product. This is the "who may look at this page"
// control, and the three verbs of §7.1b (read / produce / write) are issued
// and withdrawn here or nowhere.
//
// Four properties, each of which is a rule that fails quietly when it is
// wrong:
//
//  1. ONLY A HUMAN ISSUES A GRANT (§7.1b rule 1). The refusal is the first
//     thing every mutation does, before the body is read, and it is a positive
//     test for a human credential rather than a blocklist of agent markers —
//     see pageGrantCallerIsAgent. `granted_by_user_id` is NOT NULL by
//     migration, so the schema refuses an agent-issued row even if this check
//     were ever removed; the handler refusing FIRST is what makes the refusal
//     legible instead of a constraint violation.
//
//  2. ISSUING IS NOT DELEGABLE. The gate is ownership of the page or
//     ADMIN/OWNER of the workspace (§7.1 rule 3) and it deliberately does NOT
//     consult page_grants: a `write` grantee may rearrange the page, not widen
//     who reaches it. Otherwise the first grant issued is the last one anybody
//     controls.
//
//  3. REVOKE IS SYMMETRIC WITH GRANT (§11b decision 13). Every subject kind
//     that can be granted can be revoked by the same reference, and a revoke
//     with no --level removes every level that subject holds. An asymmetric
//     revoke is how a grant becomes impossible to remove.
//
//  4. EVERY CHANGE IS JOURNALLED, actor and subject recorded (§7.1b) — an ACL
//     nobody can audit is not a security control. One entry per row changed,
//     so a revoke that removed three levels reads as three facts rather than
//     one summary a reviewer has to interpret.
//
// What this file does NOT do: it never widens what a grantee may SEE of a
// crew's data. §7.1 rule 3 is enforced where visibility is decided
// (canSeePanel, which does not read grants at all), and a grant issued here
// reaches the PAGE only.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pages"
)

// ── The wire ───────────────────────────────────────────────────────────────

// pageGrantWire is one grant as the API sends it.
//
// Subject is the REFERENCE a person types (an email, a crew slug, an agent
// slug) and SubjectID is the stored id. Both travel: the id is what the
// permission checks compare, the reference is what the CLI prints and what a
// human recognises in an audit.
//
// Live and InertReason are the use-time verdict from §7.1b — whether the human
// who issued this grant could still issue it today. A row whose issuer has
// lost their standing is NOT hidden from this listing: it is shown, inert,
// with the reason, because "the grant you added is doing nothing" is precisely
// what the page owner needs to be told.
type pageGrantWire struct {
	SubjectType     string   `json:"subject_type"`
	Subject         string   `json:"subject"`
	SubjectID       string   `json:"subject_id"`
	Level           string   `json:"level"`
	Panels          []string `json:"panels,omitempty"`
	GrantedBy       string   `json:"granted_by"`
	GrantedByUserID string   `json:"granted_by_user_id"`
	GrantedAt       string   `json:"granted_at"`
	Live            bool     `json:"live"`
	InertReason     string   `json:"inert_reason,omitempty"`
}

// pageGrantsWire is the envelope all three verbs answer with: the page, and
// its complete grant list AFTER the change. A mutation that returned only what
// it changed would leave the CLI guessing at the resulting state, and the
// resulting state is the only thing an operator is deciding about.
type pageGrantsWire struct {
	Page    string          `json:"page"`
	Grants  []pageGrantWire `json:"grants"`
	Changed int             `json:"changed,omitempty"`
}

// pageGrantWriteRequest is the PUT body.
//
// `subject` is a reference, not an id, in the same way `owner: crew/lookout`
// is in a page spec: the ids are CUIDs nobody types. `panels` is meaningful
// for produce alone — the database says so with a CHECK, and the handler says
// so with a 400 rather than storing a scope that would read to a reviewer as a
// scope while the code ignored it.
type pageGrantWriteRequest struct {
	SubjectType string   `json:"subject_type"`
	Subject     string   `json:"subject"`
	Level       string   `json:"level"`
	Panels      []string `json:"panels"`
}

// ── 1. List — GET /api/v1/pages/{slug}/grants ──────────────────────────────

// ListGrants returns the page's complete ACL, including the rows that are
// currently inert.
func (h *PageHandler) ListGrants(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())
	rec, ok := h.grantPage(w, r, wsID)
	if !ok {
		return
	}
	// Reading an ACL is not reading a page: it names people, their crews and
	// the agents somebody trusted, so it is the owner's and the admin's, not
	// every workspace member's.
	if !h.mayAdministerGrants(r.Context(), wsID, user.ID, RoleFromContext(r.Context()), rec) {
		replyError(w, http.StatusForbidden, pageGrantAdminRefusal)
		return
	}
	out, ok := h.grantsDocument(w, r, wsID, rec, 0)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ── 2. Grant — PUT /api/v1/pages/{slug}/grants ─────────────────────────────

// PutGrant issues (or re-issues) one grant. PUT rather than POST because the
// row is identified by (page, subject_type, subject, level) and re-running the
// same command must be the same state, not a second row.
//
// Re-issuing re-anchors `granted_by_user_id` to the human running it. That is
// deliberate and it is the §7.1b invariant working as intended: the authority
// behind a grant is whoever vouches for it NOW, so the person re-issuing takes
// it over from whoever issued it before.
func (h *PageHandler) PutGrant(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	// §7.1b rule 1, before anything else is read.
	if isAgent, reason := pageGrantCallerIsAgent(r); isAgent {
		replyError(w, http.StatusForbidden, reason)
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())
	rec, ok := h.grantPage(w, r, wsID)
	if !ok {
		return
	}
	if !h.mayAdministerGrants(r.Context(), wsID, user.ID, RoleFromContext(r.Context()), rec) {
		replyError(w, http.StatusForbidden, pageGrantAdminRefusal)
		return
	}

	body, ok := readCapped(w, r, pages.MaxSpecBytes, "grant request")
	if !ok {
		return
	}
	var req pageGrantWriteRequest
	if err := json.Unmarshal(body, &req); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.SubjectType = strings.ToLower(strings.TrimSpace(req.SubjectType))
	req.Level = strings.ToLower(strings.TrimSpace(req.Level))
	req.Subject = strings.TrimSpace(req.Subject)

	if !validPageSubjectType(req.SubjectType) {
		replyError(w, http.StatusBadRequest,
			`subject_type must be "user", "crew" or "agent" (§7.1b: three subject kinds, and an agent is named, never implied)`)
		return
	}
	if req.Subject == "" {
		replyError(w, http.StatusBadRequest, "subject is required: the user, crew or agent the grant is for")
		return
	}
	if !validPageGrantLevel(req.Level) {
		replyError(w, http.StatusBadRequest,
			`level must be "read", "produce" or "write" (§7.1b: three verbs — see the page, push into named panels, edit the spec)`)
		return
	}
	panels := trimPageGrantPanels(req.Panels)
	if len(panels) > 0 && req.Level != pageGrantProduce {
		replyError(w, http.StatusBadRequest, fmt.Sprintf(
			"--panels scopes a produce grant and nothing else; %q covers the whole page, "+
				"and storing a panel list against it would read as a scope the code ignores", req.Level))
		return
	}
	// A scope naming a panel this page does not have is a grant that silently
	// covers nothing. Refuse it here rather than let somebody believe an agent
	// was authorised.
	if len(panels) > 0 {
		known, err := h.pagePanelIDs(r.Context(), wsID, rec)
		if err != nil {
			replyInternalError(w, h.logger, "load page panels for grant", err)
			return
		}
		for _, id := range panels {
			if !known[id] {
				replyError(w, http.StatusBadRequest, fmt.Sprintf(
					"page %q has no panel %q; a produce grant scoped to a panel that does not exist authorises nothing",
					rec.Slug, id))
				return
			}
		}
	}

	subjectID, subjectRef, ok := h.resolveGrantSubject(w, r, wsID, req.SubjectType, req.Subject)
	if !ok {
		return
	}

	var panelJSON any
	if len(panels) > 0 {
		encoded, err := json.Marshal(panels)
		if err != nil {
			replyInternalError(w, h.logger, "marshal grant panel scope", err)
			return
		}
		panelJSON = string(encoded)
	}
	now := h.evaluator().Now().UTC().Format(time.RFC3339)

	if _, err := h.db.ExecContext(r.Context(), `
		INSERT INTO page_grants (page_id, subject_type, subject_id, level, panel_ids, granted_by_user_id, granted_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (page_id, subject_type, subject_id, level) DO UPDATE SET
			panel_ids          = excluded.panel_ids,
			granted_by_user_id = excluded.granted_by_user_id,
			granted_at         = excluded.granted_at`,
		rec.ID, req.SubjectType, subjectID, req.Level, panelJSON, user.ID, now); err != nil {
		replyInternalError(w, h.logger, "insert page grant", err)
		return
	}

	h.journalGrantChange(r.Context(), journal.EntryPageGrantAdded, wsID, user, rec,
		req.SubjectType, subjectID, subjectRef, req.Level, panels)

	out, ok := h.grantsDocument(w, r, wsID, rec, 1)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ── 3. Revoke — DELETE /api/v1/pages/{slug}/grants ─────────────────────────

// DeleteGrant withdraws a subject's grants. The subject rides on the query
// string (`?subject_type=agent&subject=watcher[&level=produce]`) rather than
// in a body: a DELETE body is optional in HTTP and proxies drop it, and a
// revoke that arrives with its subject silently missing would delete either
// nothing or everything — both unacceptable for the one operation an operator
// runs when something has gone wrong.
//
// `level` is optional, and omitting it revokes every level the subject holds.
// §7.1b's own example is `crewship page revoke <slug> --agent <agent-slug>`
// with no level, and that has to mean "this agent no longer reaches this
// page", not "guess which of its three grants I meant".
func (h *PageHandler) DeleteGrant(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if isAgent, reason := pageGrantCallerIsAgent(r); isAgent {
		replyError(w, http.StatusForbidden, reason)
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())
	rec, ok := h.grantPage(w, r, wsID)
	if !ok {
		return
	}
	if !h.mayAdministerGrants(r.Context(), wsID, user.ID, RoleFromContext(r.Context()), rec) {
		replyError(w, http.StatusForbidden, pageGrantAdminRefusal)
		return
	}

	q := r.URL.Query()
	subjectType := strings.ToLower(strings.TrimSpace(q.Get("subject_type")))
	subject := strings.TrimSpace(q.Get("subject"))
	level := strings.ToLower(strings.TrimSpace(q.Get("level")))

	if !validPageSubjectType(subjectType) {
		replyError(w, http.StatusBadRequest,
			`subject_type must be "user", "crew" or "agent"`)
		return
	}
	if subject == "" {
		replyError(w, http.StatusBadRequest, "subject is required: the user, crew or agent to revoke")
		return
	}
	if level != "" && !validPageGrantLevel(level) {
		replyError(w, http.StatusBadRequest,
			`level must be "read", "produce" or "write", or be omitted to revoke every level this subject holds`)
		return
	}

	subjectID, subjectRef, ok := h.resolveGrantSubject(w, r, wsID, subjectType, subject)
	if !ok {
		return
	}

	// Read the rows first: the journal entry names the level that was actually
	// removed, and "removed nothing" has to be distinguishable from "removed
	// three levels" in the response as well as in the audit trail.
	records, err := h.loadPageGrantRecords(r.Context(), wsID, rec)
	if err != nil {
		replyInternalError(w, h.logger, "load page grants for revoke", err)
		return
	}
	var removed []pageGrantRecord
	for _, g := range records {
		if g.SubjectType != subjectType || g.SubjectID != subjectID {
			continue
		}
		if level != "" && g.Level != level {
			continue
		}
		removed = append(removed, g)
	}
	if len(removed) == 0 {
		replyError(w, http.StatusNotFound, fmt.Sprintf(
			"page %q has no %s grant for %s/%s", rec.Slug, pageGrantLevelPhrase(level), subjectType, subjectRef))
		return
	}

	args := []any{rec.ID, subjectType, subjectID}
	stmt := `DELETE FROM page_grants WHERE page_id = ? AND subject_type = ? AND subject_id = ?`
	if level != "" {
		stmt += ` AND level = ?`
		args = append(args, level)
	}
	if _, err := h.db.ExecContext(r.Context(), stmt, args...); err != nil {
		replyInternalError(w, h.logger, "delete page grant", err)
		return
	}

	for _, g := range removed {
		h.journalGrantChange(r.Context(), journal.EntryPageGrantRemoved, wsID, user, rec,
			g.SubjectType, g.SubjectID, subjectRef, g.Level, g.PanelIDs)
	}

	out, ok := h.grantsDocument(w, r, wsID, rec, len(removed))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ── Shared ─────────────────────────────────────────────────────────────────

const pageGrantAdminRefusal = "only the page owner or a workspace admin may read or change this page's grants; " +
	"a `write` grant rearranges the page, it does not widen who reaches it (§7.1 rule 3)"

// pageGrantLevelPhrase renders the level for a message, where an omitted level
// means "any".
func pageGrantLevelPhrase(level string) string {
	if level == "" {
		return "matching"
	}
	return level
}

// trimPageGrantPanels drops blanks and duplicates, preserving the order the
// caller wrote — the scope is read by people as well as by code.
func trimPageGrantPanels(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		id := strings.TrimSpace(raw)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// grantPage loads the page named in the path, answering 404 the same way every
// other page route does.
func (h *PageHandler) grantPage(w http.ResponseWriter, r *http.Request, wsID string) (*pageRecord, bool) {
	slug := r.PathValue("slug")
	rec, err := h.loadPage(r.Context(), wsID, slug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, fmt.Sprintf("page %q not found", slug))
			return nil, false
		}
		replyInternalError(w, h.logger, "load page for grants", err)
		return nil, false
	}
	return rec, true
}

// mayAdministerGrants answers §7.1 rule 3's "Grants are issued by the page
// owner or by a workspace ADMIN/OWNER".
//
// It does not consult page_grants, and that omission is the rule: authority to
// widen access is not itself grantable, or the first grant issued would be the
// last one the owner controls. It is also the predicate the resolver
// re-evaluates at USE time for the ISSUER of every stored grant
// (pages_grants_authz.go) — the same standing, asked twice, once when the
// grant is written and once every time it is read.
func (h *PageHandler) mayAdministerGrants(ctx context.Context, wsID, userID, role string, rec *pageRecord) bool {
	if canRole(role, "manage") {
		return true
	}
	return h.isPageOwner(ctx, wsID, userID, rec)
}

// pagePanelIDs is the set of panel ids on this page, for validating a produce
// scope.
func (h *PageHandler) pagePanelIDs(ctx context.Context, wsID string, rec *pageRecord) (map[string]bool, error) {
	panels, err := h.loadPanels(ctx, wsID, rec.ID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(panels))
	for _, p := range panels {
		out[p.PanelID] = true
	}
	return out, nil
}

// resolveGrantSubject turns a typed reference into a stored id and the label
// to show it under.
//
//   - user: an email or a user id, and the user must be a member of THIS
//     workspace. Granting page access to somebody with no workspace membership
//     would be an ACL row that nothing else in the product honours.
//   - crew / agent: a slug or an id, alive, in this workspace.
//
// A miss is 400 with the reference echoed, never a silent no-op row: a grant
// to a subject that does not exist is exactly the kind of thing an operator
// believes worked.
func (h *PageHandler) resolveGrantSubject(w http.ResponseWriter, r *http.Request, wsID, subjectType, ref string) (id, label string, ok bool) {
	var err error
	switch subjectType {
	case pageSubjectUser:
		err = h.db.QueryRowContext(r.Context(), `
			SELECT u.id, u.email
			FROM users u
			JOIN workspace_members wm ON wm.user_id = u.id AND wm.workspace_id = ?
			WHERE u.id = ? OR lower(u.email) = lower(?)`, wsID, ref, ref).Scan(&id, &label)
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusBadRequest, fmt.Sprintf(
				"no member of this workspace matches user %q", ref))
			return "", "", false
		}
	case pageSubjectCrew:
		err = h.db.QueryRowContext(r.Context(), `
			SELECT id, slug FROM crews
			WHERE workspace_id = ? AND (id = ? OR slug = ?) AND deleted_at IS NULL`, wsID, ref, ref).Scan(&id, &label)
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusBadRequest, fmt.Sprintf(
				"crew/%s does not exist in this workspace", ref))
			return "", "", false
		}
	case pageSubjectAgent:
		err = h.db.QueryRowContext(r.Context(), `
			SELECT id, slug FROM agents
			WHERE workspace_id = ? AND (id = ? OR slug = ?) AND deleted_at IS NULL`, wsID, ref, ref).Scan(&id, &label)
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusBadRequest, fmt.Sprintf(
				"agent/%s does not exist in this workspace", ref))
			return "", "", false
		}
	default:
		replyError(w, http.StatusBadRequest, `subject_type must be "user", "crew" or "agent"`)
		return "", "", false
	}
	if err != nil {
		replyInternalError(w, h.logger, "resolve grant subject", err)
		return "", "", false
	}
	return id, label, true
}

// grantSubjectLabel renders a stored subject id back as the reference a person
// typed. A subject whose row has since disappeared is shown as its bare id
// rather than omitted — the grant is still there, and hiding the row it points
// at would make the listing lie about what the page's ACL contains.
func (h *PageHandler) grantSubjectLabel(ctx context.Context, wsID, subjectType, subjectID string) string {
	var label string
	var err error
	switch subjectType {
	case pageSubjectUser:
		err = h.db.QueryRowContext(ctx, `SELECT email FROM users WHERE id = ?`, subjectID).Scan(&label)
	case pageSubjectCrew:
		err = h.db.QueryRowContext(ctx, `SELECT slug FROM crews WHERE id = ? AND workspace_id = ?`, subjectID, wsID).Scan(&label)
	case pageSubjectAgent:
		err = h.db.QueryRowContext(ctx, `SELECT slug FROM agents WHERE id = ? AND workspace_id = ?`, subjectID, wsID).Scan(&label)
	}
	if err != nil || strings.TrimSpace(label) == "" {
		return subjectID
	}
	return label
}

// grantsDocument builds the response envelope: the page's whole ACL, with the
// use-time verdict on every row.
func (h *PageHandler) grantsDocument(w http.ResponseWriter, r *http.Request, wsID string, rec *pageRecord, changed int) (*pageGrantsWire, bool) {
	records, err := h.loadPageGrantRecords(r.Context(), wsID, rec)
	if err != nil {
		replyInternalError(w, h.logger, "load page grants", err)
		return nil, false
	}
	out := &pageGrantsWire{Page: rec.Slug, Grants: make([]pageGrantWire, 0, len(records)), Changed: changed}
	issuers := map[string]string{}
	for _, g := range records {
		issuer, seen := issuers[g.GrantedBy]
		if !seen {
			issuer = h.grantSubjectLabel(r.Context(), wsID, pageSubjectUser, g.GrantedBy)
			issuers[g.GrantedBy] = issuer
		}
		out.Grants = append(out.Grants, pageGrantWire{
			SubjectType:     g.SubjectType,
			Subject:         h.grantSubjectLabel(r.Context(), wsID, g.SubjectType, g.SubjectID),
			SubjectID:       g.SubjectID,
			Level:           g.Level,
			Panels:          g.PanelIDs,
			GrantedBy:       issuer,
			GrantedByUserID: g.GrantedBy,
			GrantedAt:       g.GrantedAt,
			Live:            g.Live,
			InertReason:     g.inertReason(),
		})
	}
	return out, true
}

// journalGrantChange writes the audit record §7.1b requires: actor and
// subject, on both verbs.
//
// Best-effort with respect to the response — the grant is issued or withdrawn
// whether or not the journal accepted the entry — but a failure is logged
// loudly, for the reason the rule exists: an ACL nobody can audit is not a
// security control.
func (h *PageHandler) journalGrantChange(ctx context.Context, entryType journal.EntryType,
	wsID string, actor *AuthUser, rec *pageRecord,
	subjectType, subjectID, subjectRef, level string, panels []string) {
	if h.journal == nil {
		return
	}
	verb, preposition := "granted", "to"
	if entryType == journal.EntryPageGrantRemoved {
		verb, preposition = "revoked", "from"
	}
	payload := map[string]any{
		"page":          rec.Slug,
		"page_id":       rec.ID,
		"subject_type":  subjectType,
		"subject_id":    subjectID,
		"subject":       subjectRef,
		"level":         level,
		"actor_user_id": actor.ID,
	}
	if len(panels) > 0 {
		payload["panels"] = panels
	}
	if _, err := h.journal.Emit(ctx, journal.Entry{
		WorkspaceID: wsID,
		Type:        entryType,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorUser,
		ActorID:     actor.ID,
		Summary: fmt.Sprintf("%s %s %s on page %s %s %s/%s",
			actor.Email, verb, level, rec.Slug, preposition, subjectType, subjectRef),
		Payload: payload,
	}); err != nil && h.logger != nil {
		h.logger.Warn("pages: grant change was not journalled",
			"page", rec.Slug, "type", string(entryType), "subject", subjectType+"/"+subjectRef, "error", err)
	}
}
