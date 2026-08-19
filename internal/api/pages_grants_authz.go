package api

// Pages — grant RESOLUTION (docs/prd/pages.md §7.1 rules 3–5, §7.1b).
//
// pages_authz.go answers "what may this caller see"; this file answers the
// prior question — "which grant rows are still worth anything" — and it is
// the only place in the package that reads page_grants for an authorization
// decision. Everything that consults a grant (mayEditSpec, mayProduce, the
// agent surface, and the grants listing itself) comes through
// loadPageGrantRecords, so there is exactly one implementation of the rule
// below and no second one to drift.
//
// THE RULE, and why it is a query rather than a sweep:
//
//	"The invariant that makes this safe: an agent's authority is a subset of
//	 the authorising human's, never a superset. A grant to an agent is
//	 evaluated against the granting human's own rights AT USE TIME, not at
//	 grant time — if that human loses access to a crew, every agent grant they
//	 issued narrows with them." (§7.1b)
//
// A grant row that outlives its issuer's access is a privilege-escalation
// primitive: revoke the human and their delegated authority keeps working,
// forever, under a name nobody can reach. Two designs were available.
//
//   - A periodic sweep that deletes grants whose issuer lost access. Rejected.
//     It is correct only between runs of a job that can fail, be paused, or be
//     configured out — and the window it leaves open is precisely the window an
//     attacker wants: the minutes after an account is demoted.
//   - Re-deriving the issuer's authority on every read, in the same statement
//     that loads the grant. Chosen. There is no window, because there is no
//     interval; the authority of a grant is not stored anywhere, so it cannot
//     be stale. The cost is one LEFT JOIN and one correlated EXISTS on a table
//     read once per page request.
//
// The SQL below is therefore the rule itself, not an optimisation of it. A
// grant is LIVE only while the human who issued it could issue it again right
// now:
//
//	the issuer is still a member of this workspace,   AND
//	  they are still a workspace ADMIN/OWNER,          OR
//	  they are still the page's user owner,            OR
//	  they are still a member of the page's owner crew.
//
// That last arm is §7.1b's sentence in code. A crew-owned page administered by
// a crew member, who is then removed from the crew, loses every grant that
// member issued — at the next read, not at the next sweep.
//
// The issuer's OWN grants are deliberately not consulted when deriving their
// authority: only intrinsic standing (workspace role, page ownership, crew
// membership) counts. That keeps the recursion out of the resolver, and it
// closes delegation chains — a grantee cannot mint a second grant that
// outlives the first, because a grantee cannot mint a grant at all
// (pages_grants.go gates issuing on ownership, never on a grant).
//
// A hard-deleted issuer needs no arm here at all: page_grants.granted_by_user_id
// is NOT NULL and cascades on user delete (the migration), so their rows are
// gone before this query runs. NOT NULL is §7.1b rule 1 in the schema — only a
// human issues a grant — and the cascade is the same rule's second half.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// Grant vocabulary. These strings are the database's CHECK constraints
// (page_grants.level, page_grants.subject_type) and the wire's enum in one
// place, so a level the handler accepts can never be a level the schema
// refuses.
const (
	pageGrantRead    = "read"
	pageGrantProduce = "produce"
	pageGrantWrite   = "write"

	pageSubjectUser  = "user"
	pageSubjectCrew  = "crew"
	pageSubjectAgent = "agent"
)

func validPageGrantLevel(level string) bool {
	switch level {
	case pageGrantRead, pageGrantProduce, pageGrantWrite:
		return true
	}
	return false
}

func validPageSubjectType(kind string) bool {
	switch kind {
	case pageSubjectUser, pageSubjectCrew, pageSubjectAgent:
		return true
	}
	return false
}

// pageGrantRecord is one page_grants row PLUS the use-time verdict on its
// issuer. The verdict travels with the row rather than being recomputed by
// each consumer: an authorization path filters on Live, and the audit listing
// shows the inert rows with the reason, from the same load.
type pageGrantRecord struct {
	SubjectType string
	SubjectID   string
	Level       string
	PanelIDs    []string // nil = every panel; only meaningful for produce
	GrantedBy   string
	GrantedAt   string

	// IssuerRole is the issuer's CURRENT workspace role, "" when they are no
	// longer a member. Live is the whole rule above, decided in SQL.
	IssuerRole string
	Live       bool
}

// inertReason says, in a sentence a person can act on, why a stored grant is
// currently worth nothing. An ACL row that silently does nothing is worse than
// no row: somebody believes access was granted.
//
// The `read` case (issue C5, §7.1b's verb table: "`read` — may see the page
// and its panels") is checked FIRST, ahead of the liveness verdict, and it is
// unconditional — a `read` grant is inert even when its issuer is fully live.
// That is deliberate, not an oversight: liveness answers "could this human
// still issue this grant right now", and for `read` the honest answer to a
// different question — "does issuing it change anything" — is no, in THIS
// build, regardless of who issued it or how current their standing is. See
// the long comment below for why, and what would have to change to make it
// stop being true.
func (g pageGrantRecord) inertReason() string {
	if g.Level == pageGrantRead {
		return "read grants are accepted, stored and journalled, but no authorization " +
			"decision in this build consults them — see the comment on pageReadGrantOpenDecision " +
			"in this file for the two ways to resolve that and what each would touch"
	}
	if g.Live {
		return ""
	}
	if g.IssuerRole == "" {
		return "the human who issued it is no longer a member of this workspace"
	}
	return "the human who issued it no longer owns or administers this page"
}

// pageReadGrantOpenDecision is not a function that runs; it is where the
// open question C5 raised lives, so it has one findable name instead of being
// scattered across commit messages. Nothing below this comment executes.
//
// THE GAP: §7.1b's verb table states plainly that `read` "may see the page
// and its panels". validPageGrantLevel (above) accepts it, pages_grants.go
// stores it, journals it, and lists it back as Live=true — every surface an
// administrator looks at says the grant succeeded. But grantsFor (this file)
// is consulted by exactly two callers — mayEditSpec's `write` check and
// mayProduce's `produce` check (pages_authz.go) — and neither one, nor
// canSeePanel, nor PageHandler.List/Get (pages_handler.go), nor anything else
// in this package, ever asks "does a live `read` grant cover this caller".
// Page reachability today is decided entirely by workspace membership
// (pages_authz.go's header comment pins this reading and explains why: §7
// states the PANEL rule precisely — panel.owner_crew_id is the ACL — and
// leaves the PAGE rule implicit, and "every member reaches the page's
// structure" is the reading that makes §7.1b's "sealed placeholder for a
// panel it cannot read" sentence make sense). Under that reading a `read`
// grant has nothing left to widen: everyone already in the workspace can
// already reach the page. So the grant is accepted into a schema that has no
// use for it — which is worse than rejecting it, because a human who issued
// one believes they changed something.
//
// This file does NOT resolve the gap by picking a behaviour. §7 never states
// who may reach a PAGE (only a panel), so both readings below are consistent
// with the PRD as written, and they behave very differently. Whoever owns
// docs/prd/pages.md §7.1 decides; this comment exists so that decision is
// between two described options, not a re-derivation from scratch.
//
// ── Reading A: keep the status quo (workspace membership reaches the page,
//    exactly as pages_authz.go's header comment already documents; `read`
//    grants exist for a subject that isn't a colleague — §7.3's public
//    token, once it exists) ──
//
//   Changes required: none. This is what the code already does. The only
//   change under this reading is the one this file makes: inertReason()
//   above tells the truth about it, so the grants-listing endpoint
//   (pages_grants.go GET, which already surfaces InertReason for the
//   liveness case) shows an administrator the same honesty for `read` that
//   it already shows for a revoked issuer, instead of reporting Live=true
//   for a grant that changes no decision.
//
// ── Reading B: a page is reachable only by (1) its owner — isPageOwner,
//    pages_authz.go, (2) a member of any crew that owns one of its panels —
//    i.e. viewer.Crews intersects the page's panels' owner_crew_id set, and
//    (3) a caller named by a live `read` grant — grantsFor, this file ──
//
//   Changes required, all in this package:
//
//     - pages_authz.go gains a new function, e.g. canSeePage(ctx, wsID,
//       userID, rec, viewer) bool, mirroring canSeePanel's shape: owner OR
//       any crew overlap with the page's panels OR a live read grant via
//       grantsFor. canRole(role, "manage") still short-circuits to true, for
//       the same reason canSeePanel's ADMIN carve-out exists — effectiveRole
//       already takes the max of workspace and crew role, so denying ADMIN
//       here would only make two paths disagree.
//
//     - PageHandler.List (pages_handler.go) currently lists every page in
//       `WHERE p.workspace_id = ?` unconditionally. It would need to load
//       each page's panels' owner_crew_id set (or join it in SQL) before
//       filtering, and drop the ones where canSeePage is false — a page a
//       caller cannot reach must not even appear as a locked row, the same
//       "sealed rather than visible-but-denied" posture §11b decision 14
//       already takes for panels.
//
//     - PageHandler.Get (pages_handler.go) would call canSeePage right after
//       loadPage and, on false, return 404 rather than 403 — matching how a
//       page a caller cannot reach should look identical to one that does
//       not exist, so the endpoint is not an existence oracle for pages
//       outside the caller's reach.
//
//     - Any other call site that loads a page for a viewer-scoped response —
//       at minimum pages_versions.go's history endpoints and
//       pages_wake.go's wake-gate listing — would need the same gate. Each
//       one currently trusts "loaded within this workspace" as sufficient;
//       Reading B removes that assumption everywhere, not just in List/Get.
//
//     - grantsFor itself needs no change — it already loads live `read`
//       grants correctly. Reading B is entirely about finally calling it
//       from a page-reachability check, not about how it resolves grants.
//
//   Reading B is a strictly larger and more invasive change than Reading A,
//   and it is the one that makes the currently-dead `read` level start doing
//   something. It is also the one most likely to change who can see what for
//   existing workspaces the moment it ships, which is exactly why it should
//   not be picked implicitly inside a fix for a conformance audit.

// grant narrows the record to what the permission checks read.
func (g pageGrantRecord) grant() pageGrant {
	return pageGrant{Level: g.Level, PanelIDs: g.PanelIDs}
}

// loadPageGrantRecords returns EVERY grant on the page with its use-time
// verdict attached. It is the single reader of page_grants.
//
// The CASE is the rule documented at the top of this file. It is expressed in
// the statement rather than in Go so that no caller can load grants without
// also loading the verdict — a Go-side helper is one `if` away from being
// forgotten, and the forgotten `if` is the escalation.
func (h *PageHandler) loadPageGrantRecords(ctx context.Context, wsID string, rec *pageRecord) ([]pageGrantRecord, error) {
	rows, err := h.db.QueryContext(ctx, `
		SELECT g.subject_type, g.subject_id, g.level, COALESCE(g.panel_ids, ''),
		       g.granted_by_user_id, g.granted_at,
		       COALESCE(wm.role, ''),
		       CASE
		         WHEN wm.user_id IS NULL                                THEN 0
		         WHEN wm.role IN ('OWNER', 'ADMIN')                     THEN 1
		         WHEN ? <> '' AND g.granted_by_user_id = ?              THEN 1
		         WHEN ? <> '' AND EXISTS (
		              SELECT 1 FROM crew_members cm
		               WHERE cm.crew_id = ? AND cm.user_id = g.granted_by_user_id) THEN 1
		         ELSE 0
		       END
		FROM page_grants g
		LEFT JOIN workspace_members wm
		       ON wm.user_id = g.granted_by_user_id AND wm.workspace_id = ?
		WHERE g.page_id = ?
		ORDER BY g.subject_type, g.subject_id, g.level`,
		rec.OwnerUserID, rec.OwnerUserID,
		rec.OwnerCrewID, rec.OwnerCrewID,
		wsID, rec.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []pageGrantRecord
	for rows.Next() {
		var g pageGrantRecord
		var panelIDs string
		var live int
		if err := rows.Scan(&g.SubjectType, &g.SubjectID, &g.Level, &panelIDs,
			&g.GrantedBy, &g.GrantedAt, &g.IssuerRole, &live); err != nil {
			return nil, err
		}
		g.Live = live == 1
		if strings.TrimSpace(panelIDs) != "" {
			var ids []string
			if err := json.Unmarshal([]byte(panelIDs), &ids); err == nil {
				g.PanelIDs = ids
			}
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// livePageGrants loads the grants that are live AND match the caller. The
// match function names the subject; the liveness is not the caller's business
// and is not optional.
func (h *PageHandler) livePageGrants(ctx context.Context, wsID string, rec *pageRecord,
	match func(pageGrantRecord) bool) ([]pageGrant, error) {
	records, err := h.loadPageGrantRecords(ctx, wsID, rec)
	if err != nil {
		return nil, err
	}
	var out []pageGrant
	for _, g := range records {
		if !g.Live || !match(g) {
			continue
		}
		out = append(out, g.grant())
	}
	return out, nil
}

// grantsFor loads the live grants that apply to a HUMAN caller: their own user
// grants plus any grant to a crew they belong to.
//
// Agent grants are deliberately not consulted here, and that is not tidiness.
// A grant to an agent is authority delegated to a token in a container; a
// human who happens to sit in the same crew borrowing it would be exactly the
// escalation §7.1b closes. The agent surface reads its own grants through
// agentGrants, from an identity the token proves.
func (h *PageHandler) grantsFor(ctx context.Context, wsID string, rec *pageRecord, viewer *pageViewer) ([]pageGrant, error) {
	return h.livePageGrants(ctx, wsID, rec, func(g pageGrantRecord) bool {
		switch g.SubjectType {
		case pageSubjectUser:
			return g.SubjectID == viewer.UserID
		case pageSubjectCrew:
			return viewer.Crews[g.SubjectID]
		default:
			return false
		}
	})
}

// ── The agent side of §7.1b ────────────────────────────────────────────────
//
// An agent is a grant SUBJECT, never a grant issuer (§7.1b rule 1), and its
// authority is whatever a live grant names — narrowed, at every read, by the
// standing of the human who issued it.
//
// A crew grant does NOT reach the agents of that crew. `crew` in §7.1b's
// subject vocabulary sits next to `agent` as a separate kind, and the rule
// that an agent's blast radius is exactly what a human wrote down is only true
// if reaching an agent requires naming it. Widening a crew's grant onto its
// agents would hand every container in the crew whatever the crew's people
// hold, which is the opposite of the intent.

// agentGrants returns the live grants held by one agent on one page.
func (h *PageHandler) agentGrants(ctx context.Context, wsID string, rec *pageRecord, agentID string) ([]pageGrant, error) {
	if strings.TrimSpace(agentID) == "" {
		return nil, nil
	}
	return h.livePageGrants(ctx, wsID, rec, func(g pageGrantRecord) bool {
		return g.SubjectType == pageSubjectAgent && g.SubjectID == agentID
	})
}

// agentViewer is an agent's standing for the panel-visibility check, and it is
// §7.1b rule 2 in one struct: "layout and data are separate authorities".
//
// The agent's crews — in practice the one crew it belongs to — are what reach
// a panel's data, exactly as a human's crews are (§7.1 rule 2). Role is left
// EMPTY on purpose: canRole("") is false, so an agent never takes the
// ADMIN carve-out in canSeePanel. An agent cannot hold a workspace role, and
// synthesising one for it would hand a container the one shortcut past every
// per-crew check in the file.
//
// No grant appears here either (§7.1 rule 3). An agent holding `write` may
// arrange a panel owned by a crew it cannot see, and receives the sealed
// placeholder for it like any other viewer — `write` is authority over
// arrangement, never over content.
func (h *PageHandler) agentViewer(ctx context.Context, wsID, agentID string) (*pageViewer, error) {
	v := &pageViewer{UserID: "", Role: "", Crews: map[string]bool{}}
	rows, err := h.db.QueryContext(ctx, `
		SELECT a.crew_id
		FROM agents a
		JOIN crews c ON c.id = a.crew_id
		WHERE a.id = ? AND a.workspace_id = ? AND a.deleted_at IS NULL
		  AND c.workspace_id = ? AND c.deleted_at IS NULL
		  AND a.crew_id IS NOT NULL`, agentID, wsID, wsID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var crewID string
		if err := rows.Scan(&crewID); err != nil {
			return nil, err
		}
		v.Crews[crewID] = true
	}
	return v, rows.Err()
}

// agentMayEditSpec answers the `write` verb for an agent: add, remove and
// re-arrange panels. An agent has no ownership and no workspace role, so a
// live `write` grant is the only thing that can say yes.
func (h *PageHandler) agentMayEditSpec(ctx context.Context, wsID string, rec *pageRecord, agentID string) bool {
	grants, err := h.agentGrants(ctx, wsID, rec, agentID)
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

// agentMayProduce answers §7.1b's produce scope for an agent: "an agent granted
// produce on one panel cannot overwrite another agent's panel on the same
// page". The panel list is the scope and the scope is checked per push.
func (h *PageHandler) agentMayProduce(ctx context.Context, wsID string, rec *pageRecord, agentID, panelID string) bool {
	grants, err := h.agentGrants(ctx, wsID, rec, agentID)
	if err != nil {
		return false
	}
	for _, g := range grants {
		if g.Level == pageGrantProduce && g.covers(panelID) {
			return true
		}
	}
	return false
}

// crewMayProduce is agentMayProduce's sibling for the other unattended
// subject. A routine runs as a crew even when no agent is acting, so the
// internal push path asks about both and neither may reach past the panel
// scope §7.1b pins.
//
// It exists so that path has no reason to read page_grants itself. That is not
// tidiness: the rule had two implementations for a few hours, with two
// different definitions of when a grant is live, and each side had its own
// passing tests. One reader is the property; these helpers are how call sites
// get it without a closure each.
func (h *PageHandler) crewMayProduce(ctx context.Context, wsID string, rec *pageRecord, crewID, panelID string) bool {
	crewID = strings.TrimSpace(crewID)
	if crewID == "" {
		return false
	}
	grants, err := h.livePageGrants(ctx, wsID, rec, func(g pageGrantRecord) bool {
		return g.Level == pageGrantProduce && g.SubjectType == "crew" && g.SubjectID == crewID
	})
	if err != nil {
		return false
	}
	for _, g := range grants {
		if g.covers(panelID) {
			return true
		}
	}
	return false
}

// ── §7.1b rule 1: only a human issues a grant ──────────────────────────────

// pageGrantCallerIsAgent reports whether this request was made by, or on
// behalf of, an agent rather than by a human — and returns the sentence the
// 403 will carry.
//
// §7.1b rule 1: "An agent with `write` may rebuild the page freely but can
// never widen who reaches it — not even to an agent in its own crew. This
// closes the escalation path where an injected agent grows its own blast
// radius one grant at a time."
//
// The check is a positive test for humanity, not a negative test for a list of
// known agent markers, because the list of markers is the thing that goes out
// of date:
//
//   - RequireAuth is the ONE place that records which credential authenticated
//     a request (middleware.go, ctxAuthKind), and it records it for both kinds
//     a human presents: an interactive session and a CLI token. An EMPTY auth
//     kind means the request never went through RequireAuth — an internal /
//     sidecar route, or a hand-built context — and per that file's own
//     contract, absence must deny.
//   - On top of that, the three markers an agent-originated request carries are
//     refused explicitly, so a future adapter that mounts this handler behind
//     the internal wrapper (as internal_routines.go does for pipelines) is
//     refused loudly rather than silently accepted: the workspace/crew binding
//     the internal token proves, the raw internal-token header, and the acting
//     agent slug the sidecar forwards.
//
// An agent that needs another agent's help asks a human, and that request is a
// normal inbox item.
func pageGrantCallerIsAgent(r *http.Request) (bool, string) {
	const refusal = "only a human may issue or revoke a page grant (§7.1b rule 1); " +
		"an agent with `write` may rebuild the page but can never widen who reaches it — " +
		"ask a human to run `crewship page grant`"

	ctx := r.Context()
	if InternalTokenWorkspaceFromContext(ctx) != "" || InternalTokenCrewFromContext(ctx) != "" {
		return true, refusal
	}
	if strings.TrimSpace(r.Header.Get("X-Internal-Token")) != "" {
		return true, refusal
	}
	if strings.TrimSpace(r.Header.Get(actingAgentSlugHeader)) != "" {
		return true, refusal
	}
	switch AuthKindFromContext(ctx) {
	case AuthKindSession, AuthKindCLIToken:
		return false, ""
	default:
		return true, refusal
	}
}
