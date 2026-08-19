package api

// Pages — permissions (docs/prd/pages.md §7).
//
// Everything here is decided server-side. The client receives the CONTENT of
// only the panels it may see — the rest arrive as sealed placeholders (§11b
// decision 14) carrying nothing but their slot — because a hidden-but-delivered
// panel is a data leak (§7.1 rule 5), and the decision runs before
// serialisation rather than in the renderer.
//
// Three authorities, deliberately separate (§7.1b):
//
//	read     — may see the page and its panels. A panel's visibility IS its
//	           owning crew's visibility (§7.1 rule 2); a grant widens access to
//	           the PAGE and never to a crew's data (§7.1 rule 3), so a grantee
//	           still sees only what their own membership already permits.
//	produce  — may push payloads into named panels. Separate from viewer
//	           authority: a crew member who can SEE a panel cannot WRITE it
//	           (§7.1 rule 4).
//	write    — may edit the page spec. Authority over arrangement, never over
//	           content (§7.1b rule 2).
//
// §7 states the panel rule precisely and leaves the PAGE rule implicit, so the
// page rule is written down here, in one function (canSeePage) rather than in
// each endpoint's head:
//
//	a page is reached by its owner, by a workspace ADMIN/OWNER, by a member of
//	any crew that owns one of its panels, and by whoever a live grant names.
//
// An earlier build let membership of the workspace reach every page, which made
// `read` grants decide nothing — the contradiction pageReachRule
// (pages_grants_authz.go) records in full, along with why the PRD's own
// sentences ("three verbs, not one"; "the owner can grant read and write to
// others") cannot be satisfied under that reading. Reaching the page is still
// the easy half and reading a panel still the guarded one: a caller who reaches
// a page they hold no crew membership on is served §11b decision 14's sealed
// placeholders, which is exactly the shape §7.1b rule 2 describes.

import "context"

// pageViewer is one caller's standing in the workspace, loaded once per
// request: their workspace role and the crews they belong to.
type pageViewer struct {
	UserID string
	Role   string
	Crews  map[string]bool
}

func (h *PageHandler) loadViewer(ctx context.Context, wsID, userID string) (*pageViewer, error) {
	v := &pageViewer{UserID: userID, Role: RoleFromContext(ctx), Crews: map[string]bool{}}
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

// canSeePanel answers §7.1 rule 2: "A panel's visibility is its owning crew's
// visibility. panel.owner_crew_id is not decoration; it is the ACL."
//
// A `false` here does not delete the panel from the response — §11b decision
// 14 replaces it with the sealed placeholder, so the page has the same shape
// for everyone (§2.3) — but it does delete everything ABOUT it: no schema, no
// payload, no producer, no SLA, no state. The decision is made here, once,
// server-side, and never in the renderer, because a hidden-but-delivered panel
// is a data leak (§7.1 rule 5).
//
// The ADMIN carve-out is not a favour: effectiveRole already takes the max of
// workspace and crew role (helpers.go), so an ADMIN sees the crew's panels
// through the ordinary path anyway. Denying it here would only make the two
// paths disagree.
//
// A grant does NOT appear in this check, and that is §7.1 rule 3 in code: "A
// grant widens access to the page, never to a crew's data. A grantee still
// sees only the panels their own crew membership and workspace role already
// permit" — otherwise a page owner could use a grant to leak their crew's
// panel to somebody outside it.
func (h *PageHandler) canSeePanel(viewer *pageViewer, p *panelRecord) bool {
	if viewer == nil {
		return true
	}
	if canRole(viewer.Role, "manage") {
		return true
	}
	return viewer.Crews[p.OwnerCrewID]
}

// pageReachedWithoutGrant answers the part of page reachability that needs no
// grant lookup: ownership, the workspace role, and "may this viewer see any
// panel on this page at all".
//
// The panel arm goes through canSeePanel rather than testing viewer.Crews
// itself, so that "you can see a panel on it" and "you can open it" can never
// disagree — a viewer served a panel through a path that refused them the page
// would be a bug in whichever of the two was written second.
//
// A nil viewer means an unscoped render (Create and Update echo the page back
// to the author, Import to the importer); those callers have already made their
// own decision and pageDocument serves them everything.
func (h *PageHandler) pageReachedWithoutGrant(rec *pageRecord, panels []*panelRecord, viewer *pageViewer) bool {
	if viewer == nil {
		return true
	}
	if canRole(viewer.Role, "manage") {
		return true
	}
	// Ownership, from the standing already loaded: owner_user_id is the caller,
	// or owner_crew_id is a crew they belong to (§7.1 rule 1's xor).
	if rec.OwnerUserID != "" && rec.OwnerUserID == viewer.UserID {
		return true
	}
	if rec.OwnerCrewID != "" && viewer.Crews[rec.OwnerCrewID] {
		return true
	}
	for _, p := range panels {
		if h.canSeePanel(viewer, p) {
			return true
		}
	}
	return false
}

// canSeePage is the page-level twin of canSeePanel: may this caller open this
// page at all. See pageReachRule (pages_grants_authz.go) for the decision it
// implements and the endpoints that must call it.
//
// A false is a 404, not a 403 — a page outside the caller's reach must look
// exactly like one that does not exist, or the endpoint becomes an existence
// oracle for every page in the workspace. That is the same posture §11b
// decision 14 takes for a panel, one level up.
//
// The grant lookup is last because it is the only arm that costs a query, and
// it goes through grantsFor so the issuer's use-time standing is re-derived
// here exactly as it is for `produce` and `write` (§7.1b).
func (h *PageHandler) canSeePage(ctx context.Context, wsID string, rec *pageRecord,
	panels []*panelRecord, viewer *pageViewer) (bool, error) {
	if h.pageReachedWithoutGrant(rec, panels, viewer) {
		return true, nil
	}
	grants, err := h.grantsFor(ctx, wsID, rec, viewer)
	if err != nil {
		return false, err
	}
	return anyGrantReachesPage(grants), nil
}

// anyGrantReachesPage is the shared verdict over an already-resolved grant set,
// so the single-page path (canSeePage) and the bulk listing path
// (PageHandler.List) reach it by the same route.
func anyGrantReachesPage(grants []pageGrant) bool {
	for _, g := range grants {
		if reachesPage(g.Level) {
			return true
		}
	}
	return false
}

// pageGrant is one row of page_grants as the checks read it.
type pageGrant struct {
	Level    string
	PanelIDs []string // nil = every panel; only meaningful for level = produce
}

// grantsFor — which grants apply to this caller — lives in
// pages_grants_authz.go, together with the use-time evaluation of the
// authorising human that every grant read is subject to (§7.1b). It is one
// function rather than a query in each consumer for one reason: a grant read
// that skipped the issuer check would be a privilege-escalation primitive, and
// the way to make that impossible is to leave nowhere to skip it from.

// covers reports whether a produce grant reaches this panel. A NULL panel_ids
// covers every panel; a list covers exactly what it names, so an agent granted
// produce on one panel cannot overwrite another agent's panel on the same page
// (§7.1b).
func (g pageGrant) covers(panelID string) bool {
	if g.PanelIDs == nil {
		return true
	}
	for _, id := range g.PanelIDs {
		if id == panelID {
			return true
		}
	}
	return false
}

// isPageOwner reports whether the caller IS the page's owner — the user named
// in owner_user_id, or a member of the crew named in owner_crew_id (§7.1
// rule 1: exactly one of the two).
func (h *PageHandler) isPageOwner(ctx context.Context, wsID, userID string, rec *pageRecord) bool {
	if rec.OwnerUserID != "" {
		return rec.OwnerUserID == userID
	}
	if rec.OwnerCrewID == "" {
		return false
	}
	var one int
	err := h.db.QueryRowContext(ctx,
		`SELECT 1 FROM crew_members WHERE crew_id = ? AND user_id = ?`, rec.OwnerCrewID, userID).Scan(&one)
	return err == nil
}

// mayEditSpec answers the `write` verb: add, remove and re-arrange panels.
func (h *PageHandler) mayEditSpec(ctx context.Context, wsID, userID, role string, rec *pageRecord) bool {
	if canRole(role, "manage") {
		return true
	}
	if h.isPageOwner(ctx, wsID, userID, rec) {
		return true
	}
	viewer, err := h.loadViewer(ctx, wsID, userID)
	if err != nil {
		return false
	}
	grants, err := h.grantsFor(ctx, wsID, rec, viewer)
	if err != nil {
		return false
	}
	for _, g := range grants {
		if g.Level == pageGrantWrite {
			return true
		}
	}
	return false
}

// mayProduce answers §7.1 rule 4 — "Only the declared producer may write a
// panel's payload" — for a caller on the PUBLIC surface, which is always a
// human (agents reach the platform through the sidecar).
//
// The reasoning, because the rule is easy to get wrong in either direction:
//
//   - An explicit `produce` grant covering the panel is always sufficient. It
//     is the mechanism §7.1b defines for exactly this, and it is journalled
//     when it is issued.
//   - For a `routine` or `agent` producer there IS a stronger identity
//     available — the run, the token — so a human pushing by hand is refused
//     unless they hold that grant. Letting an admin write an agent's panel
//     would forge that agent's data, and provenance would then record a
//     producer that did not produce it.
//   - For a `script` or `webhook` producer there is no identity to check
//     against: `script/watch-services.sh` is a name, not a principal, and the
//     script runs under whichever human's CLI token invoked it. The honest
//     gate is therefore "who may act as this page's producer at all" — the
//     page's owner, or a workspace ADMIN/OWNER, who can issue themselves the
//     grant in one call anyway.
//
// Returns the reason on refusal so the 403, the journal entry and the owner's
// notification all say the same thing.
func (h *PageHandler) mayProduce(ctx context.Context, wsID, userID, role string, rec *pageRecord, panel *panelRecord) (bool, string) {
	viewer, err := h.loadViewer(ctx, wsID, userID)
	if err != nil {
		return false, "the caller's crew membership could not be read"
	}
	grants, err := h.grantsFor(ctx, wsID, rec, viewer)
	if err != nil {
		return false, "the page's grants could not be read"
	}
	for _, g := range grants {
		if g.Level == pageGrantProduce && g.covers(panel.PanelID) {
			return true, ""
		}
	}
	switch panel.ProducerKind {
	case "script", "webhook":
		if h.isPageOwner(ctx, wsID, userID, rec) || canRole(role, "manage") {
			return true, ""
		}
		return false, "panel " + panel.PanelID + " is produced by " + panel.producerRef() +
			"; pushing to it needs a produce grant, or ownership of the page"
	default:
		return false, "panel " + panel.PanelID + " is produced by " + panel.producerRef() +
			"; only that producer may write it, and a human push needs an explicit produce grant"
	}
}

// pageOwnerUserID returns the user to notify about something that happened to
// this page, or "" when the page is crew-owned (in which case the notification
// goes to the workspace rather than to a person).
func pageOwnerUserID(rec *pageRecord) string { return rec.OwnerUserID }
