package api

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// Admin › Workspaces and Admin › Users.
//
// Scope. A workspace ADMIN or OWNER sees the workspace they are in — the
// same rule every other /admin read follows, so a tenant admin never
// enumerates the other tenants. An instance administrator (instance_admin.go:
// CREWSHIP_OWNER_EMAIL, a named instance admin, or by fallback the oldest
// workspace's owner) sees every workspace and every account. Which of the two a response is says itself in the
// X-Admin-Scope header ("instance" or "workspace"), so a client never has to
// guess whether one row means "one workspace" or "one you may see".
//
// Timestamps are returned as stored: RFC3339 for everything this server has
// written since the RFC3339 migration, SQLite's "YYYY-MM-DD HH:MM:SS" for
// rows older than it.

const (
	adminScopeHeader   = "X-Admin-Scope"
	adminScopeInstance = "instance"
	adminScopeWS       = "workspace"

	// revoked_reason values for the admin write paths. admin_invalidate is
	// the value the host-side `crewship admin invalidate-sessions` already
	// writes, so an audit query for "forced logouts" finds both.
	reasonAdminRevoke     = "admin_revoke"
	reasonAdminInvalidate = "admin_invalidate"
)

// adminScope is "instance" for an instance administrator (instance_admin.go),
// who sees every workspace and account, and "workspace" for everyone else.
func adminScope(r *http.Request, db *sql.DB) string {
	if isInstanceAdmin(r, db) {
		return adminScopeInstance
	}
	return adminScopeWS
}

// ListWorkspaces returns the workspaces the caller may administer, with the
// figures the Admin › Workspaces table shows.
// GET /api/v1/admin/workspaces — requires ADMIN+.
func (h *AdminHandler) ListWorkspaces(w http.ResponseWriter, r *http.Request) {
	if !canAdministerInstance(r.Context()) {
		replyError(w, http.StatusForbidden, "Forbidden: ADMIN or OWNER only")
		return
	}
	ctx := r.Context()
	currentWS := WorkspaceIDFromContext(ctx)
	scope := adminScope(r, h.db)
	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339)
	since30 := now.Add(-30 * 24 * time.Hour).Format(time.RFC3339)

	q := `
		SELECT w.id, w.name, w.slug, w.created_at, w.updated_at,
			(SELECT COUNT(*) FROM workspace_members WHERE workspace_id = w.id),
			(SELECT COUNT(*) FROM agents WHERE workspace_id = w.id AND deleted_at IS NULL),
			(SELECT COUNT(*) FROM crews WHERE workspace_id = w.id AND deleted_at IS NULL),
			w.preferred_language, w.run_retention_days, COALESCE(w.allow_privileged_credentials, 0),
			(SELECT COUNT(*) FROM workspace_invitations i
				WHERE i.workspace_id = w.id AND i.accepted_at IS NULL AND i.expires_at > ?),
			(SELECT MAX(a.created_at) FROM audit_logs a WHERE a.workspace_id = w.id),
			(SELECT COALESCE(SUM(COALESCE(m.total_estimated_cost, 0)), 0) FROM missions m
				WHERE m.workspace_id = w.id AND m.updated_at >= ?)
		FROM workspaces w
		WHERE w.deleted_at IS NULL`
	args := []any{nowStr, since30}
	if scope == adminScopeWS {
		q += ` AND w.id = ?`
		args = append(args, currentWS)
	}
	q += ` ORDER BY w.created_at DESC`

	rows, err := h.db.QueryContext(ctx, q, args...)
	if err != nil {
		replyInternalError(w, h.logger, "list workspaces (admin)", err)
		return
	}
	defer rows.Close()

	type wsRow struct {
		ID                         string    `json:"id"`
		Name                       string    `json:"name"`
		Slug                       string    `json:"slug"`
		CreatedAt                  string    `json:"created_at"`
		UpdatedAt                  string    `json:"updated_at"`
		MemberCount                int       `json:"_count_members"`
		AgentCount                 int       `json:"_count_agents"`
		CrewCount                  int       `json:"_count_crews"`
		PreferredLanguage          *string   `json:"preferred_language"`
		RunRetentionDays           *int64    `json:"run_retention_days"`
		AllowPrivilegedCredentials bool      `json:"allow_privileged_credentials"`
		PendingInvitations         int       `json:"pending_invitations"`
		LastActivityAt             *string   `json:"last_activity_at"`
		Runs7d                     int       `json:"runs_7d"`
		RunsByDay                  [7]int    `json:"runs_by_day"`
		Cost30dUSD                 float64   `json:"cost_30d_usd"`
		Current                    bool      `json:"current"`
		Owners                     []wsOwner `json:"owners"`
	}

	result := []*wsRow{}
	byID := map[string]*wsRow{}
	for rows.Next() {
		var ws wsRow
		var lang, last sql.NullString
		var retention sql.NullInt64
		var priv int64
		if err := rows.Scan(&ws.ID, &ws.Name, &ws.Slug, &ws.CreatedAt, &ws.UpdatedAt,
			&ws.MemberCount, &ws.AgentCount, &ws.CrewCount,
			&lang, &retention, &priv, &ws.PendingInvitations, &last, &ws.Cost30dUSD); err != nil {
			replyInternalError(w, h.logger, "scan workspace (admin)", err)
			return
		}
		if lang.Valid && lang.String != "" {
			ws.PreferredLanguage = &lang.String
		}
		if retention.Valid {
			v := retention.Int64
			ws.RunRetentionDays = &v
		}
		if last.Valid && last.String != "" {
			ws.LastActivityAt = &last.String
		}
		ws.AllowPrivilegedCredentials = priv != 0
		ws.Current = ws.ID == currentWS
		ws.Owners = []wsOwner{}
		result = append(result, &ws)
		byID[ws.ID] = &ws
	}
	if err := rows.Err(); err != nil {
		replyInternalError(w, h.logger, "rows iteration (workspaces)", err)
		return
	}
	_ = rows.Close()

	// Runs per UTC day for the last seven days, counted the way the
	// runs_count metric counts them: one run = one trace with a run.started
	// journal entry. One query for every listed workspace.
	if len(result) > 0 {
		today := now.Truncate(24 * time.Hour)
		first := today.AddDate(0, 0, -6)
		days := map[string]int{}
		for i := 0; i < 7; i++ {
			days[first.AddDate(0, 0, i).Format("2006-01-02")] = i
		}
		rq := `SELECT je.workspace_id, substr(je.ts, 1, 10) AS d, COUNT(DISTINCT je.trace_id)
			FROM journal_entries je
			WHERE je.entry_type = 'run.started' AND je.ts >= ?`
		rargs := []any{first.Format(time.RFC3339)}
		if scope == adminScopeWS {
			rq += ` AND je.workspace_id = ?`
			rargs = append(rargs, currentWS)
		}
		rq += ` GROUP BY je.workspace_id, d`
		rr, err := h.db.QueryContext(ctx, rq, rargs...)
		if err != nil {
			replyInternalError(w, h.logger, "workspace runs (admin)", err)
			return
		}
		defer rr.Close()
		for rr.Next() {
			var wsID, day string
			var n int
			if err := rr.Scan(&wsID, &day, &n); err != nil {
				replyInternalError(w, h.logger, "scan workspace runs (admin)", err)
				return
			}
			ws, ok := byID[wsID]
			idx, inWindow := days[day]
			if !ok || !inWindow {
				continue
			}
			ws.RunsByDay[idx] += n
			ws.Runs7d += n
		}
		if err := rr.Err(); err != nil {
			replyInternalError(w, h.logger, "rows iteration (workspace runs)", err)
			return
		}
	}

	if len(result) > 0 {
		oq := `SELECT wm.workspace_id, u.id, u.email, u.full_name
			FROM workspace_members wm JOIN users u ON u.id = wm.user_id
			WHERE wm.role = 'OWNER'`
		var oargs []any
		if scope == adminScopeWS {
			oq += ` AND wm.workspace_id = ?`
			oargs = append(oargs, currentWS)
		}
		oq += ` ORDER BY wm.created_at`
		or, err := h.db.QueryContext(ctx, oq, oargs...)
		if err != nil {
			replyInternalError(w, h.logger, "workspace owners (admin)", err)
			return
		}
		defer or.Close()
		for or.Next() {
			var wsID string
			var o wsOwner
			if err := or.Scan(&wsID, &o.ID, &o.Email, &o.FullName); err != nil {
				replyInternalError(w, h.logger, "scan workspace owner (admin)", err)
				return
			}
			if ws, ok := byID[wsID]; ok {
				ws.Owners = append(ws.Owners, o)
			}
		}
		if err := or.Err(); err != nil {
			replyInternalError(w, h.logger, "rows iteration (workspace owners)", err)
			return
		}
	}

	w.Header().Set(adminScopeHeader, scope)
	writeJSON(w, http.StatusOK, result)
}

// wsOwner is one OWNER of a workspace in the admin list.
type wsOwner struct {
	ID       string  `json:"id"`
	Email    string  `json:"email"`
	FullName *string `json:"full_name"`
}

// ListUsers returns the accounts the caller may administer: members of the
// current workspace, or — for an instance admin — every account, members
// of no workspace included. One row per person.
// GET /api/v1/admin/users — requires ADMIN+.
func (h *AdminHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	if !canAdministerInstance(r.Context()) {
		replyError(w, http.StatusForbidden, "Forbidden: ADMIN or OWNER only")
		return
	}
	ctx := r.Context()
	currentWS := WorkspaceIDFromContext(ctx)
	scope := adminScope(r, h.db)
	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339)

	q := `
		SELECT u.id, u.email, u.full_name, u.avatar_url, u.created_at,
			COALESCE(u.failed_login_count, 0), u.locked_until, u.email_verified,
			(SELECT MAX(s.last_used_at) FROM user_sessions s
				WHERE s.user_id = u.id AND s.revoked_at IS NULL),
			(SELECT COUNT(*) FROM user_sessions s
				WHERE s.user_id = u.id AND s.revoked_at IS NULL AND s.expires_at > ?),
			(SELECT COUNT(*) FROM cli_tokens t
				WHERE t.user_id = u.id AND t.revoked_at IS NULL
				AND (t.expires_at IS NULL OR t.expires_at > ?)),
			u.instance_role, u.suspended_at, u.suspended_reason,
			(SELECT MAX(v.expires) FROM verification_tokens v
				WHERE v.identifier = u.email AND v.purpose = 'account_setup' AND v.expires > ?)
		FROM users u`
	args := []any{nowStr, nowStr, nowStr}
	if scope == adminScopeWS {
		q += ` WHERE EXISTS (SELECT 1 FROM workspace_members wm WHERE wm.user_id = u.id AND wm.workspace_id = ?)`
		args = append(args, currentWS)
	}
	q += ` ORDER BY u.created_at DESC`

	rows, err := h.db.QueryContext(ctx, q, args...)
	if err != nil {
		replyInternalError(w, h.logger, "list users", err)
		return
	}
	defer rows.Close()

	type wsRef struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Slug string `json:"slug"`
	}
	type membership struct {
		MemberID    string `json:"member_id"`
		WorkspaceID string `json:"workspace_id"`
		Name        string `json:"name"`
		Slug        string `json:"slug"`
		Role        string `json:"role"`
		JoinedAt    string `json:"joined_at"`
	}
	type userRow struct {
		ID               string       `json:"id"`
		Email            string       `json:"email"`
		FullName         *string      `json:"full_name"`
		AvatarURL        *string      `json:"avatar_url"`
		CreatedAt        string       `json:"created_at"`
		Workspace        *wsRef       `json:"workspace"`
		Role             *string      `json:"role"`
		Memberships      []membership `json:"memberships"`
		LastActiveAt     *string      `json:"last_active_at"`
		ActiveSessions   int          `json:"active_sessions"`
		CLITokens        int          `json:"cli_tokens"`
		LockedUntil      *string      `json:"locked_until"`
		FailedLoginCount int          `json:"failed_login_count"`
		EmailVerified    bool         `json:"email_verified"`
		// InstanceAdmin and its source ("env", "role" or
		// ) — see instance_admin.go.
		InstanceAdmin       bool    `json:"instance_admin"`
		InstanceAdminSource *string `json:"instance_admin_source"`
		SuspendedAt         *string `json:"suspended_at"`
		SuspendedReason     *string `json:"suspended_reason"`
		// SetupLinkExpiresAt is set while an unused setup link is pending:
		// the account exists and nobody has chosen its password yet.
		SetupLinkExpiresAt *string `json:"setup_link_expires_at"`
	}

	result := []*userRow{}
	byID := map[string]*userRow{}
	for rows.Next() {
		var u userRow
		var locked, verified, lastActive, instRole, suspended, suspendedReason, setupExp sql.NullString
		if err := rows.Scan(&u.ID, &u.Email, &u.FullName, &u.AvatarURL, &u.CreatedAt,
			&u.FailedLoginCount, &locked, &verified, &lastActive, &u.ActiveSessions, &u.CLITokens,
			&instRole, &suspended, &suspendedReason, &setupExp); err != nil {
			replyInternalError(w, h.logger, "scan user", err)
			return
		}
		if locked.Valid && locked.String != "" {
			// Only a lock still in force is a lock. An expired one is cleared
			// lazily on the next sign-in attempt, so the column can hold a
			// past time for a perfectly usable account.
			if t, perr := time.Parse(time.RFC3339, locked.String); perr == nil && t.After(now) {
				u.LockedUntil = &locked.String
			}
		}
		u.EmailVerified = emailVerified(verified)
		if lastActive.Valid && lastActive.String != "" {
			u.LastActiveAt = &lastActive.String
		}
		if suspended.Valid && suspended.String != "" {
			u.SuspendedAt = &suspended.String
			if suspendedReason.Valid && suspendedReason.String != "" {
				u.SuspendedReason = &suspendedReason.String
			}
		}
		if setupExp.Valid && setupExp.String != "" {
			u.SetupLinkExpiresAt = &setupExp.String
		}
		if src := instanceAdminSourceFor(u.Email, instRole.String, u.SuspendedAt != nil); src != "" {
			u.InstanceAdmin = true
			u.InstanceAdminSource = &src
		}
		u.Memberships = []membership{}
		result = append(result, &u)
		byID[u.ID] = &u
	}
	if err := rows.Err(); err != nil {
		replyInternalError(w, h.logger, "rows iteration (users)", err)
		return
	}
	_ = rows.Close()

	mq := `SELECT wm.id, wm.user_id, wm.workspace_id, w.name, w.slug, wm.role, wm.created_at
		FROM workspace_members wm
		JOIN workspaces w ON w.id = wm.workspace_id
		WHERE w.deleted_at IS NULL`
	var margs []any
	if scope == adminScopeWS {
		mq += ` AND wm.workspace_id = ?`
		margs = append(margs, currentWS)
	}
	mq += ` ORDER BY w.name`
	mr, err := h.db.QueryContext(ctx, mq, margs...)
	if err != nil {
		replyInternalError(w, h.logger, "list memberships (admin)", err)
		return
	}
	defer mr.Close()
	for mr.Next() {
		var m membership
		var userID string
		if err := mr.Scan(&m.MemberID, &userID, &m.WorkspaceID, &m.Name, &m.Slug, &m.Role, &m.JoinedAt); err != nil {
			replyInternalError(w, h.logger, "scan membership (admin)", err)
			return
		}
		if u, ok := byID[userID]; ok {
			u.Memberships = append(u.Memberships, m)
		}
	}
	if err := mr.Err(); err != nil {
		replyInternalError(w, h.logger, "rows iteration (memberships)", err)
		return
	}

	// workspace + role keep their old meaning for existing clients: the
	// membership in the caller's workspace, else the first one there is.
	for _, u := range result {
		var pick *membership
		for i := range u.Memberships {
			if u.Memberships[i].WorkspaceID == currentWS {
				pick = &u.Memberships[i]
				break
			}
		}
		if pick == nil && len(u.Memberships) > 0 {
			pick = &u.Memberships[0]
		}
		if pick != nil {
			u.Workspace = &wsRef{ID: pick.WorkspaceID, Name: pick.Name, Slug: pick.Slug}
			role := pick.Role
			u.Role = &role
		}
	}

	w.Header().Set(adminScopeHeader, scope)
	writeJSON(w, http.StatusOK, result)
}

// emailVerified reads users.email_verified, which NextAuth writes as a
// timestamp and the Google sign-in path as a boolean.
func emailVerified(v sql.NullString) bool {
	if !v.Valid {
		return false
	}
	s := strings.TrimSpace(strings.ToLower(v.String))
	return s != "" && s != "0" && s != "false"
}

// AdminUsersHandler holds the per-person admin actions: read a person's
// signed-in devices and CLI tokens, sign one or all of them out, and lift a
// sign-in lockout.
type AdminUsersHandler struct {
	db       *sql.DB
	logger   *slog.Logger
	sessions sessions.Store
}

// NewAdminUsersHandler wires the per-person admin actions. The session
// store must be the one the auth middleware checks, or a revoke would flip
// a row nothing reads.
func NewAdminUsersHandler(db *sql.DB, logger *slog.Logger, store sessions.Store) *AdminUsersHandler {
	return &AdminUsersHandler{db: db, logger: logger, sessions: store}
}

// target resolves and authorises the {userId} a per-person action is about.
//
// Outside an instance admin, the person must be a member of the caller's
// workspace — anything else is a 404, the same answer as "no such user", so
// the route cannot be used to probe which accounts exist. An ADMIN may not
// act on an OWNER of the workspace (403): signing the owner out, or lifting
// a lock someone put on the owner's account by guessing, is the owner's call.
func (h *AdminUsersHandler) target(w http.ResponseWriter, r *http.Request) (actor *AuthUser, wsID, targetID string, ok bool) {
	actor = UserFromContext(r.Context())
	if actor == nil || actor.ID == "" {
		replyError(w, http.StatusUnauthorized, "authentication required")
		return nil, "", "", false
	}
	callerRole := RoleFromContext(r.Context())
	if !canAdministerInstance(r.Context()) {
		replyError(w, http.StatusForbidden, "Forbidden: ADMIN or OWNER only")
		return nil, "", "", false
	}
	wsID = WorkspaceIDFromContext(r.Context())
	targetID = strings.TrimSpace(r.PathValue("userId"))
	if targetID == "" {
		replyError(w, http.StatusBadRequest, "userId path parameter required")
		return nil, "", "", false
	}

	if isInstanceAdmin(r, h.db) {
		var one int
		err := h.db.QueryRowContext(r.Context(), `SELECT 1 FROM users WHERE id = ?`, targetID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, "user not found")
			return nil, "", "", false
		}
		if err != nil {
			replyInternalError(w, h.logger, "admin user lookup", err)
			return nil, "", "", false
		}
		return actor, wsID, targetID, true
	}

	var targetRole string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT role FROM workspace_members WHERE workspace_id = ? AND user_id = ?`, wsID, targetID).Scan(&targetRole)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "user not found")
		return nil, "", "", false
	}
	if err != nil {
		replyInternalError(w, h.logger, "admin user membership lookup", err)
		return nil, "", "", false
	}
	if strings.EqualFold(targetRole, "OWNER") && !strings.EqualFold(callerRole, "OWNER") {
		replyError(w, http.StatusForbidden, "Forbidden: only an OWNER can act on an OWNER")
		return nil, "", "", false
	}
	return actor, wsID, targetID, true
}

// adminUserSessionsResponse is GET /api/v1/admin/users/{userId}/sessions:
// a person's active sign-ins and live CLI tokens.
type adminUserSessionsResponse struct {
	Sessions  []adminUserSession  `json:"sessions"`
	CLITokens []adminUserCLIToken `json:"cli_tokens"`
}

// adminUserSession is one signed-in device. current marks the session the
// admin is using right now.
type adminUserSession struct {
	ID         string `json:"id"`
	CreatedAt  string `json:"created_at"`
	LastUsedAt string `json:"last_used_at"`
	ExpiresAt  string `json:"expires_at"`
	UserAgent  string `json:"user_agent"`
	IP         string `json:"ip"`
	Current    bool   `json:"current"`
}

// adminUserCLIToken is one live CLI token, never its secret.
type adminUserCLIToken struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Scopes     []string `json:"scopes"`
	CreatedAt  string   `json:"created_at"`
	LastUsedAt *string  `json:"last_used_at"`
	ExpiresAt  *string  `json:"expires_at"`
}

// adminRevokeAllResponse is POST …/sessions/revoke-all: how many sessions
// were signed out.
type adminRevokeAllResponse struct {
	Revoked int64 `json:"revoked"`
}

// Sessions lists a person's active sign-ins (with device and IP) and live
// CLI tokens.
// GET /api/v1/admin/users/{userId}/sessions — requires ADMIN+.
func (h *AdminUsersHandler) Sessions(w http.ResponseWriter, r *http.Request) {
	actor, _, targetID, ok := h.target(w, r)
	if !ok {
		return
	}
	out := adminUserSessionsResponse{Sessions: []adminUserSession{}, CLITokens: []adminUserCLIToken{}}

	list, err := h.sessions.ListActiveForUser(r.Context(), targetID)
	if err != nil {
		replyInternalError(w, h.logger, "admin list sessions", err)
		return
	}
	const ts = "2006-01-02T15:04:05Z"
	for _, s := range list {
		out.Sessions = append(out.Sessions, adminUserSession{
			ID:         s.ID,
			CreatedAt:  s.CreatedAt.UTC().Format(ts),
			LastUsedAt: s.LastUsedAt.UTC().Format(ts),
			ExpiresAt:  s.ExpiresAt.UTC().Format(ts),
			UserAgent:  s.UserAgent,
			IP:         s.IP,
			Current:    actor.SessionID != "" && s.ID == actor.SessionID,
		})
	}

	now := time.Now().UTC().Format(time.RFC3339)
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT id, name, scopes, created_at, last_used_at, expires_at
		FROM cli_tokens
		WHERE user_id = ? AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > ?)
		ORDER BY created_at DESC`, targetID, now)
	if err != nil {
		replyInternalError(w, h.logger, "admin list cli tokens", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var t adminUserCLIToken
		var scopes, lastUsed, expires sql.NullString
		if err := rows.Scan(&t.ID, &t.Name, &scopes, &t.CreatedAt, &lastUsed, &expires); err != nil {
			replyInternalError(w, h.logger, "scan cli token (admin)", err)
			return
		}
		t.Scopes = []string{}
		for s := range parseScopes(scopes.String) {
			t.Scopes = append(t.Scopes, s)
		}
		sort.Strings(t.Scopes)
		if lastUsed.Valid {
			t.LastUsedAt = &lastUsed.String
		}
		if expires.Valid {
			t.ExpiresAt = &expires.String
		}
		out.CLITokens = append(out.CLITokens, t)
	}
	if err := rows.Err(); err != nil {
		replyInternalError(w, h.logger, "rows iteration (admin cli tokens)", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// RevokeSession signs a person out of one device.
// POST /api/v1/admin/users/{userId}/sessions/{sessionId}/revoke — ADMIN+.
func (h *AdminUsersHandler) RevokeSession(w http.ResponseWriter, r *http.Request) {
	_, _, targetID, ok := h.target(w, r)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.PathValue("sessionId"))
	if sessionID == "" {
		replyError(w, http.StatusBadRequest, "sessionId path parameter required")
		return
	}
	sess, err := h.sessions.Get(r.Context(), sessionID)
	if err != nil {
		if errors.Is(err, sessions.ErrNotFound) {
			replyError(w, http.StatusNotFound, "session not found")
			return
		}
		replyInternalError(w, h.logger, "admin get session", err)
		return
	}
	// A session of somebody else is "not found" — the id alone must not
	// reach across people.
	if sess.UserID != targetID {
		replyError(w, http.StatusNotFound, "session not found")
		return
	}
	if err := h.sessions.Revoke(r.Context(), sessionID, reasonAdminRevoke); err != nil {
		if errors.Is(err, sessions.ErrNotFound) {
			replyError(w, http.StatusNotFound, "session not found")
			return
		}
		replyInternalError(w, h.logger, "admin revoke session", err)
		return
	}
	auditFromRequest(r, h.db, "user.session_revoked", "USER", targetID, map[string]interface{}{"session_id": sessionID})
	w.WriteHeader(http.StatusNoContent)
}

// RevokeAllSessions signs a person out everywhere. CLI tokens are separate
// credentials and stay valid.
// POST /api/v1/admin/users/{userId}/sessions/revoke-all — ADMIN+.
func (h *AdminUsersHandler) RevokeAllSessions(w http.ResponseWriter, r *http.Request) {
	_, _, targetID, ok := h.target(w, r)
	if !ok {
		return
	}
	n, err := h.sessions.RevokeAllForUser(r.Context(), targetID, reasonAdminInvalidate)
	if err != nil {
		replyInternalError(w, h.logger, "admin revoke all sessions", err)
		return
	}
	auditFromRequest(r, h.db, "user.sessions_revoked", "USER", targetID, map[string]interface{}{"revoked": n})
	writeJSON(w, http.StatusOK, adminRevokeAllResponse{Revoked: n})
}

// Unlock lifts a sign-in lockout and resets the failed-attempt counter.
// POST /api/v1/admin/users/{userId}/unlock — ADMIN+.
func (h *AdminUsersHandler) Unlock(w http.ResponseWriter, r *http.Request) {
	_, _, targetID, ok := h.target(w, r)
	if !ok {
		return
	}
	if _, err := h.db.ExecContext(r.Context(),
		`UPDATE users SET locked_until = NULL, failed_login_count = 0 WHERE id = ?`, targetID); err != nil {
		replyInternalError(w, h.logger, "admin unlock user", err)
		return
	}
	auditFromRequest(r, h.db, "user.unlocked", "USER", targetID, nil)
	w.WriteHeader(http.StatusNoContent)
}
