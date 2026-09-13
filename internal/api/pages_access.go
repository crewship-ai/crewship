package api

// Pages — effective access, read-only (docs/prd/pages-collections-access-
// analysis-2026-09-12.md §1/12–13, §3 S-6, §4, §5/10, §6; F2 of §10).
//
//	GET /api/v1/pages/{slug}/access   who reaches this page, and by which paths
//	GET /api/v1/pages/access          which pages one subject reaches, and how
//
// Both answers are RENDERINGS of today's model and nothing more: the workspace
// role, page ownership, membership of a crew that owns the page or one of its
// panels, and the live page grants. No new way of reaching a page is defined
// here — the vocabulary is pageReach's (pages_authz.go), with the grant arm
// spelled out per level so an owner can tell a read grant from a write grant
// without opening the ACL — and nothing in this file is consulted by any
// permission check. It is a report, and a report that decided something would
// be a second copy of the rule.
//
// Three properties, each of which is a way to get it wrong:
//
//  1. CONSTANT COST. The page endpoint enumerates every member, crew and agent
//     of the workspace, and the subject endpoint every page. Each is loaded
//     ONCE, in bulk, and the paths are derived in memory; a per-subject or
//     per-page statement would make the cost of "who reaches this" linear in
//     the size of the workspace, and TestPageAccess_QueryCountDoesNotGrow holds
//     the count fixed for 60 members and 80 pages.
//
//  2. THE READER'S OWN STANDING BOUNDS WHAT IS NAMED (§1/13, §5/10). The page
//     endpoint is the owner's and the administrator's, and an owner is not
//     necessarily a member of every crew that owns a panel on their page. A
//     path through such a crew is rendered as `panel_crew:withheld`, on one
//     anonymous row, and never on a row that carries a name: attaching the
//     withheld path to a named user or crew would say which crew owns the
//     panel the caller cannot read, one intersection at a time. This is the
//     same standard pageWithheldChangeMessage (pages_project_review.go) holds
//     the publish refusal to — that a withheld part exists, never what or
//     whose it is — and TestPageAccess_WithheldCrewIsNeverNamed pins it with
//     that file's fixture.
//
//  3. ONLY A LIVE GRANT IS A PATH. The grants are read through
//     loadPageGrantRecordsIn, the single reader whose CASE decides liveness,
//     and an inert row contributes nothing here for the same reason it
//     decides nothing in canSeePage: a listing that showed an inert grant as
//     a path would tell an owner somebody reaches the page who does not.

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// ── The wire ───────────────────────────────────────────────────────────────

// pageAccessSubjectWire is one subject and the paths by which it reaches the
// page. Label is the reference a person recognises — an email, a crew slug,
// an agent slug — and is OMITTED, together with a real SubjectID, on the
// anonymous row that stands for the crews the caller cannot see.
type pageAccessSubjectWire struct {
	SubjectType string   `json:"subject_type"`
	SubjectID   string   `json:"subject_id"`
	Label       string   `json:"label,omitempty"`
	Paths       []string `json:"paths"`
}

// pageAccessWire is the page endpoint's envelope.
type pageAccessWire struct {
	Page       string                  `json:"page"`
	Subjects   []pageAccessSubjectWire `json:"subjects"`
	NextCursor string                  `json:"next_cursor,omitempty"`
}

// pageAccessRefWire names the subject the subject endpoint was asked about.
type pageAccessRefWire struct {
	SubjectType string `json:"subject_type"`
	SubjectID   string `json:"subject_id"`
	Label       string `json:"label,omitempty"`
}

// pageAccessPageWire is one page the subject reaches, and how.
type pageAccessPageWire struct {
	Slug  string   `json:"slug"`
	Name  string   `json:"name"`
	Paths []string `json:"paths"`
}

// pageSubjectAccessWire is the subject endpoint's envelope.
type pageSubjectAccessWire struct {
	Subject    pageAccessRefWire    `json:"subject"`
	Pages      []pageAccessPageWire `json:"pages"`
	NextCursor string               `json:"next_cursor,omitempty"`
}

// The paths this file adds to pageReach's vocabulary. `grant:page:<level>`
// replaces the index's bare `grant`: a grant row is keyed by (subject, level)
// today, so the level is the key that tells an owner's two grants apart. The
// PRD's `grant:page:<change_id>` arrives with the change ids themselves (F4).
//
// `panel_crew:withheld` is the rendering of a `panel_crew:<slug>` path whose
// crew the caller may not see, and `withheld` is likewise the subject_id of
// the anonymous row that carries it.
const (
	pageReachGrantPage  = "grant:page:"
	pageAccessWithheld  = "withheld"
	pageAccessPathLimit = 100
	pageAccessMaxLimit  = 1000
	pageAccessCursorTag = "v1:"
	pageAccessRefusal   = "only the page owner or a workspace admin may read who reaches this page; " +
		"a grant of any level does not include reading the page's access (§7.1 rule 3)"
	pageSubjectAccessRefusal = "only a workspace admin may read another subject's access; " +
		"a member may ask about themselves (subject=user:<their id or email>)"
)

// ── 1. Page access — GET /api/v1/pages/{slug}/access ───────────────────────

// PageAccess lists every subject that reaches the page and the paths by which
// it does. The gate is mayAdministerGrants — the same standing that reads the
// ACL, because this answer contains everything the ACL does and more.
func (h *PageHandler) PageAccess(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	rec, ok := h.grantPage(w, r, wsID)
	if !ok {
		return
	}
	if !h.mayAdministerGrants(r.Context(), wsID, user.ID, role, rec) {
		replyError(w, http.StatusForbidden, pageAccessRefusal)
		return
	}
	limit, after, ok := pageAccessPaging(w, r)
	if !ok {
		return
	}
	viewer, err := h.loadViewer(r.Context(), wsID, user.ID)
	if err != nil {
		replyInternalError(w, h.logger, "load page viewer", err)
		return
	}
	ws, err := h.loadAccessWorkspace(r.Context(), wsID)
	if err != nil {
		replyInternalError(w, h.logger, "load workspace for page access", err)
		return
	}
	panels, err := h.loadPanels(r.Context(), wsID, rec.ID)
	if err != nil {
		replyInternalError(w, h.logger, "load page panels", err)
		return
	}
	grants, err := h.loadPageGrantRecords(r.Context(), wsID, rec)
	if err != nil {
		replyInternalError(w, h.logger, "load page grants", err)
		return
	}

	// Two more statements, constant in the member count: the page's folder
	// and that folder's permissions are the `folder:<slug>` arm (#2533).
	folders, err := h.loadFoldersIn(r.Context(), wsID)
	if err != nil {
		replyInternalError(w, h.logger, "load folders", err)
		return
	}
	folderACL, err := h.loadFolderACLIn(r.Context(), wsID)
	if err != nil {
		replyInternalError(w, h.logger, "load folder permissions", err)
		return
	}
	subjects := h.pageAccessSubjects(rec, ws, panels, grants, folders, folderACL, viewer)

	// Keyset over the fixed order: the cursor names the last row sent, and
	// the next call resumes after it. Recomputing the whole set on each page
	// is the constant-cost choice — the set is derived from bulk loads that
	// cost the same however far into it the caller is.
	start := 0
	if after != "" {
		start = len(subjects)
		for i, s := range subjects {
			if s.SubjectType+"|"+s.SubjectID == after {
				start = i + 1
				break
			}
		}
	}
	out := pageAccessWire{Page: rec.Slug, Subjects: []pageAccessSubjectWire{}}
	end := start + limit
	if end > len(subjects) {
		end = len(subjects)
	}
	if start < end {
		out.Subjects = subjects[start:end]
	}
	if end < len(subjects) {
		last := subjects[end-1]
		out.NextCursor = encodePageAccessCursor(last.SubjectType + "|" + last.SubjectID)
	}
	writeJSON(w, http.StatusOK, out)
}

// ── 2. Subject access — GET /api/v1/pages/access ───────────────────────────

// SubjectAccess lists the pages one subject reaches. An administrator may ask
// about anyone; a member may ask about themselves and nobody else — the answer
// for another person is that person's ACL across the workspace, which §4 keeps
// to administrators.
func (h *PageHandler) SubjectAccess(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())

	subjectType, ref, ok := pageAccessSubjectParam(w, r)
	if !ok {
		return
	}
	limit, after, ok := pageAccessPaging(w, r)
	if !ok {
		return
	}
	subjectID, label, err := h.resolveGrantSubjectQuietly(r.Context(), wsID, subjectType, ref)
	if errors.Is(err, sql.ErrNoRows) {
		// Resolved BEFORE the authorization decision on purpose: a member
		// asking about "themselves" by an email that is not theirs must get
		// the refusal below, never a 404 that confirms whether the email is
		// a member. But an admin gets the honest miss.
		if !canRole(role, "manage") {
			replyError(w, http.StatusForbidden, pageSubjectAccessRefusal)
			return
		}
		replyError(w, http.StatusNotFound, fmt.Sprintf("%s/%s does not exist in this workspace", subjectType, ref))
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "resolve access subject", err)
		return
	}
	if !canRole(role, "manage") && !(subjectType == pageSubjectUser && subjectID == user.ID) {
		replyError(w, http.StatusForbidden, pageSubjectAccessRefusal)
		return
	}

	pagesByID, order, err := h.loadAccessPages(r.Context(), wsID)
	if err != nil {
		replyInternalError(w, h.logger, "list pages for access", err)
		return
	}
	panelsByPage, err := h.loadPanelsIn(r.Context(), wsID, pagesInWorkspace(wsID))
	if err != nil {
		replyInternalError(w, h.logger, "load page panels", err)
		return
	}
	crewSlugs, err := h.loadCrewSlugs(r.Context(), wsID)
	if err != nil {
		replyInternalError(w, h.logger, "load crew slugs", err)
		return
	}
	grantsByPage, err := h.loadPageGrantRecordsIn(r.Context(), wsID, "")
	if err != nil {
		replyInternalError(w, h.logger, "load page grants", err)
		return
	}
	// Folders and their ACL, once for the workspace: the `folder:<slug>` arm
	// of pageReach (#2533) is a path like any other and has to appear here.
	folders, err := h.loadFoldersIn(r.Context(), wsID)
	if err != nil {
		replyInternalError(w, h.logger, "load folders", err)
		return
	}
	folderACL, err := h.loadFolderACLIn(r.Context(), wsID)
	if err != nil {
		replyInternalError(w, h.logger, "load folder permissions", err)
		return
	}

	// The subject's standing, per kind: a user's is the same viewer the index
	// builds for them (role from the membership, crews from crew_members); a
	// crew's and an agent's are what those subjects can hold, which is less.
	var viewer *pageViewer
	if subjectType == pageSubjectUser {
		viewer, err = h.loadAccessUserViewer(r.Context(), wsID, subjectID)
		if err != nil {
			replyInternalError(w, h.logger, "load subject viewer", err)
			return
		}
	}

	var rows []pageAccessPageWire
	for _, id := range order {
		rec := pagesByID[id]
		ownerCrewSlug := ""
		if rec.OwnerCrewID != "" {
			ownerCrewSlug = crewSlugs[rec.OwnerCrewID]
			if ownerCrewSlug == "" {
				ownerCrewSlug = rec.OwnerCrewID
			}
		}
		var paths []string
		switch subjectType {
		case pageSubjectUser:
			paths = h.pageAccessUserPaths(rec, ownerCrewSlug, panelsByPage[rec.ID], viewer,
				folderReachSlug(rec, folders, folderACL, viewer), grantsByPage[rec.ID])
		case pageSubjectCrew:
			paths = pageAccessCrewPaths(rec, subjectID, label, panelsByPage[rec.ID], grantsByPage[rec.ID])
		default:
			paths = pageAccessAgentPaths(subjectID, grantsByPage[rec.ID])
		}
		if len(paths) == 0 {
			continue
		}
		rows = append(rows, pageAccessPageWire{Slug: rec.Slug, Name: rec.Name, Paths: paths})
	}

	start := 0
	if after != "" {
		start = len(rows)
		for i, row := range rows {
			if row.Slug == after {
				start = i + 1
				break
			}
		}
	}
	out := pageSubjectAccessWire{
		Subject: pageAccessRefWire{SubjectType: subjectType, SubjectID: subjectID, Label: label},
		Pages:   []pageAccessPageWire{},
	}
	end := start + limit
	if end > len(rows) {
		end = len(rows)
	}
	if start < end {
		out.Pages = rows[start:end]
	}
	if end < len(rows) {
		out.NextCursor = encodePageAccessCursor(rows[end-1].Slug)
	}
	writeJSON(w, http.StatusOK, out)
}

// ── Deriving the paths ─────────────────────────────────────────────────────

// pageAccessUserPaths is pageReach with the grant arm spelled out per level:
// the standing-based paths come from pageReach itself, so the two can never
// disagree about ownership, role or crews, and the live grants that name the
// user or one of their crews follow in the level vocabulary's order.
func (h *PageHandler) pageAccessUserPaths(rec *pageRecord, ownerCrewSlug string, panels []*panelRecord,
	viewer *pageViewer, folderSlug string, grants []pageGrantRecord) []string {
	paths := h.pageReach(rec, ownerCrewSlug, panels, viewer, folderSlug, false)
	return append(paths, pageAccessGrantPaths(grants, pageViewerGrantMatch(viewer))...)
}

// pageAccessCrewPaths is a crew's standing on a page: `owner` when the page
// is its, `panel_crew:<its slug>` when it owns a panel, then its live grants.
func pageAccessCrewPaths(rec *pageRecord, crewID, slug string, panels []*panelRecord, grants []pageGrantRecord) []string {
	var paths []string
	if rec.OwnerCrewID != "" && rec.OwnerCrewID == crewID {
		paths = append(paths, pageReachOwner)
	}
	for _, p := range panels {
		if p.OwnerCrewID == crewID {
			paths = append(paths, pageReachPanelCrew+slug)
			break
		}
	}
	return append(paths, pageAccessGrantPaths(grants, func(g pageGrantRecord) bool {
		return g.SubjectType == pageSubjectCrew && g.SubjectID == crewID
	})...)
}

// pageAccessAgentPaths is an agent's standing: the live grants that name it,
// and nothing else — an agent has no ownership, no role, and a crew grant
// does not reach the crew's agents (pages_grants_authz.go).
func pageAccessAgentPaths(agentID string, grants []pageGrantRecord) []string {
	return pageAccessGrantPaths(grants, func(g pageGrantRecord) bool {
		return g.SubjectType == pageSubjectAgent && g.SubjectID == agentID
	})
}

// pageAccessGrantPaths renders the live grants matching `match` as
// `grant:page:<level>` entries, one per distinct level, in the order the
// verbs are defined (read, produce, write).
func pageAccessGrantPaths(grants []pageGrantRecord, match func(pageGrantRecord) bool) []string {
	levels := map[string]bool{}
	for _, g := range liveGrantsIn(grants, match) {
		levels[g.Level] = true
	}
	var out []string
	for _, level := range []string{pageGrantRead, pageGrantProduce, pageGrantWrite} {
		if levels[level] {
			out = append(out, pageReachGrantPage+level)
		}
	}
	return out
}

// pageAccessWorkspace is everything about the workspace's subjects that the
// page endpoint needs, loaded in four statements whatever the counts.
type pageAccessWorkspace struct {
	Members   []pageAccessMember        // workspace members, by email
	Crews     map[string]pageAccessCrew // live crews by id
	CrewOrder []string                  // crew ids by slug
	CrewsOf   map[string][]string       // user id → crew ids (in CrewOrder order)
	Agents    map[string]string         // live agent id → slug
}

type pageAccessMember struct {
	ID    string
	Email string
	Role  string
}

type pageAccessCrew struct {
	ID   string
	Slug string
}

func (h *PageHandler) loadAccessWorkspace(ctx context.Context, wsID string) (*pageAccessWorkspace, error) {
	ws := &pageAccessWorkspace{Crews: map[string]pageAccessCrew{}, CrewsOf: map[string][]string{}, Agents: map[string]string{}}

	if err := h.accessRows(ctx, `
		SELECT u.id, u.email, wm.role
		FROM workspace_members wm
		JOIN users u ON u.id = wm.user_id
		WHERE wm.workspace_id = ?
		ORDER BY lower(u.email), u.id`, []any{wsID}, func(rows *sql.Rows) error {
		var m pageAccessMember
		if err := rows.Scan(&m.ID, &m.Email, &m.Role); err != nil {
			return err
		}
		ws.Members = append(ws.Members, m)
		return nil
	}); err != nil {
		return nil, err
	}

	if err := h.accessRows(ctx, `
		SELECT id, slug FROM crews WHERE workspace_id = ? AND deleted_at IS NULL ORDER BY slug, id`,
		[]any{wsID}, func(rows *sql.Rows) error {
			var c pageAccessCrew
			if err := rows.Scan(&c.ID, &c.Slug); err != nil {
				return err
			}
			ws.Crews[c.ID] = c
			ws.CrewOrder = append(ws.CrewOrder, c.ID)
			return nil
		}); err != nil {
		return nil, err
	}

	if err := h.accessRows(ctx, `
		SELECT cm.user_id, cm.crew_id
		FROM crew_members cm
		JOIN crews c ON c.id = cm.crew_id
		WHERE c.workspace_id = ? AND c.deleted_at IS NULL
		ORDER BY c.slug, c.id`, []any{wsID}, func(rows *sql.Rows) error {
		var userID, crewID string
		if err := rows.Scan(&userID, &crewID); err != nil {
			return err
		}
		ws.CrewsOf[userID] = append(ws.CrewsOf[userID], crewID)
		return nil
	}); err != nil {
		return nil, err
	}

	if err := h.accessRows(ctx, `
		SELECT id, slug FROM agents WHERE workspace_id = ? AND deleted_at IS NULL`,
		[]any{wsID}, func(rows *sql.Rows) error {
			var id, slug string
			if err := rows.Scan(&id, &slug); err != nil {
				return err
			}
			ws.Agents[id] = slug
			return nil
		}); err != nil {
		return nil, err
	}
	return ws, nil
}

// accessRows runs one statement and hands each row to scan, closing the
// cursor on every path.
func (h *PageHandler) accessRows(ctx context.Context, query string, args []any, scan func(*sql.Rows) error) error {
	rows, err := h.db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// pageAccessSubjects derives the page's whole subject list, in the order the
// cursor walks it: members by email, then crews by slug, then agents by slug,
// then the one anonymous row for the crews the caller cannot see. It costs no
// statement.
func (h *PageHandler) pageAccessSubjects(rec *pageRecord, ws *pageAccessWorkspace, panels []*panelRecord,
	grants []pageGrantRecord, folders map[string]*pageFolderRecord, folderACL map[string][]pageFolderACLRecord,
	caller *pageViewer) []pageAccessSubjectWire {
	ownerCrewSlug := ""
	if rec.OwnerCrewID != "" {
		ownerCrewSlug = rec.OwnerCrewID
		if c, ok := ws.Crews[rec.OwnerCrewID]; ok {
			ownerCrewSlug = c.Slug
		}
	}

	// A crew is visible to the caller when they administer the workspace or
	// belong to it — canSeePanel's rule, asked of the crew rather than of a
	// panel it owns. The live crews owning a panel that fail it are the
	// hidden ones: every path through them is withheld from every named row
	// and stands, once, on the anonymous row at the end.
	hiddenSlugs := map[string]bool{}
	for _, p := range panels {
		crew, live := ws.Crews[p.OwnerCrewID]
		if live && !canRole(caller.Role, "manage") && !caller.Crews[p.OwnerCrewID] {
			hiddenSlugs[crew.Slug] = true
		}
	}
	withhold := func(paths []string) []string {
		var kept []string
		for _, path := range paths {
			if strings.HasPrefix(path, pageReachPanelCrew) && hiddenSlugs[strings.TrimPrefix(path, pageReachPanelCrew)] {
				continue
			}
			kept = append(kept, path)
		}
		return kept
	}

	var out []pageAccessSubjectWire

	for _, m := range ws.Members {
		viewer := &pageViewer{UserID: m.ID, Role: m.Role, Crews: map[string]bool{}}
		for _, crewID := range ws.CrewsOf[m.ID] {
			viewer.Crews[crewID] = true
		}
		paths := withhold(h.pageAccessUserPaths(rec, ownerCrewSlug, panels, viewer,
			folderReachSlug(rec, folders, folderACL, viewer), grants))
		if len(paths) == 0 {
			continue
		}
		out = append(out, pageAccessSubjectWire{SubjectType: pageSubjectUser, SubjectID: m.ID, Label: m.Email, Paths: paths})
	}

	for _, crewID := range ws.CrewOrder {
		crew := ws.Crews[crewID]
		paths := withhold(pageAccessCrewPaths(rec, crewID, crew.Slug, panels, grants))
		if len(paths) == 0 {
			continue
		}
		out = append(out, pageAccessSubjectWire{SubjectType: pageSubjectCrew, SubjectID: crewID, Label: crew.Slug, Paths: paths})
	}

	type agentRow struct{ id, slug string }
	agents := make([]agentRow, 0, len(ws.Agents))
	for id, slug := range ws.Agents {
		agents = append(agents, agentRow{id, slug})
	}
	sort.Slice(agents, func(i, j int) bool {
		if agents[i].slug != agents[j].slug {
			return agents[i].slug < agents[j].slug
		}
		return agents[i].id < agents[j].id
	})
	for _, a := range agents {
		paths := pageAccessAgentPaths(a.id, grants)
		if len(paths) == 0 {
			continue
		}
		out = append(out, pageAccessSubjectWire{SubjectType: pageSubjectAgent, SubjectID: a.id, Label: a.slug, Paths: paths})
	}

	if len(hiddenSlugs) > 0 {
		out = append(out, pageAccessSubjectWire{
			SubjectType: pageSubjectCrew,
			SubjectID:   pageAccessWithheld,
			Paths:       []string{pageReachPanelCrew + pageAccessWithheld},
		})
	}
	return out
}

// ── Subject resolution and paging ──────────────────────────────────────────

// pageAccessSubjectParam reads the subject from either spelling the CLI and
// the docs accept: `subject=user:<ref>` in one parameter, or `subject_type=`
// plus `subject=`.
func pageAccessSubjectParam(w http.ResponseWriter, r *http.Request) (subjectType, ref string, ok bool) {
	q := r.URL.Query()
	subjectType = strings.ToLower(strings.TrimSpace(q.Get("subject_type")))
	ref = strings.TrimSpace(q.Get("subject"))
	if subjectType == "" {
		if kind, rest, found := strings.Cut(ref, ":"); found {
			subjectType, ref = strings.ToLower(strings.TrimSpace(kind)), strings.TrimSpace(rest)
		}
	}
	if ref == "" {
		replyError(w, http.StatusBadRequest,
			"subject is required: user:<email or id>, crew:<slug> or agent:<slug> (or subject_type= with a bare subject=)")
		return "", "", false
	}
	if !validPageSubjectType(subjectType) {
		replyError(w, http.StatusBadRequest, `subject must be prefixed user:, crew: or agent:, or subject_type must be one of "user", "crew", "agent"`)
		return "", "", false
	}
	return subjectType, ref, true
}

// pageAccessPaging reads ?limit and ?cursor. The cursor is opaque to the
// client and versioned so a format change is a 400, not a silent misparse.
func pageAccessPaging(w http.ResponseWriter, r *http.Request) (limit int, after string, ok bool) {
	q := r.URL.Query()
	limit = pageAccessPathLimit
	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			replyError(w, http.StatusBadRequest, "limit must be a positive integer")
			return 0, "", false
		}
		if n > pageAccessMaxLimit {
			n = pageAccessMaxLimit
		}
		limit = n
	}
	if raw := strings.TrimSpace(q.Get("cursor")); raw != "" {
		decoded, err := decodePageAccessCursor(raw)
		if err != nil {
			replyError(w, http.StatusBadRequest, "cursor is not one this endpoint issued: "+err.Error())
			return 0, "", false
		}
		after = decoded
	}
	return limit, after, true
}

func encodePageAccessCursor(key string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(pageAccessCursorTag + key))
}

func decodePageAccessCursor(s string) (string, error) {
	dec, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return "", errors.New("not valid base64url")
	}
	str := string(dec)
	if !strings.HasPrefix(str, pageAccessCursorTag) {
		return "", errors.New("missing version prefix")
	}
	key := strings.TrimPrefix(str, pageAccessCursorTag)
	if key == "" {
		return "", errors.New("empty cursor")
	}
	return key, nil
}

// loadAccessPages is the index's page read, keyed by id with the slug order
// the subject endpoint walks.
func (h *PageHandler) loadAccessPages(ctx context.Context, wsID string) (map[string]*pageRecord, []string, error) {
	rows, err := h.db.QueryContext(ctx, `
		SELECT id, slug, name, COALESCE(owner_user_id, ''), COALESCE(owner_crew_id, '')
		FROM pages WHERE workspace_id = ? ORDER BY slug ASC`, wsID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	byID := map[string]*pageRecord{}
	var order []string
	for rows.Next() {
		var p pageRecord
		if err := rows.Scan(&p.ID, &p.Slug, &p.Name, &p.OwnerUserID, &p.OwnerCrewID); err != nil {
			return nil, nil, err
		}
		byID[p.ID] = &p
		order = append(order, p.ID)
	}
	return byID, order, rows.Err()
}

// loadAccessUserViewer is loadViewer for a user who is not the caller: the
// role comes from the membership row rather than from the request context.
func (h *PageHandler) loadAccessUserViewer(ctx context.Context, wsID, userID string) (*pageViewer, error) {
	v := &pageViewer{UserID: userID, Crews: map[string]bool{}}
	if err := h.db.QueryRowContext(ctx,
		`SELECT role FROM workspace_members WHERE workspace_id = ? AND user_id = ?`, wsID, userID).Scan(&v.Role); err != nil {
		return nil, err
	}
	rows, err := h.db.QueryContext(ctx, `
		SELECT cm.crew_id
		FROM crew_members cm
		JOIN crews c ON c.id = cm.crew_id
		WHERE cm.user_id = ? AND c.workspace_id = ? AND c.deleted_at IS NULL`, userID, wsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		v.Crews[id] = true
	}
	return v, rows.Err()
}
