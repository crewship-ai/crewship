package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
)

// Instance administration: the people who run the server itself, as opposed
// to administering one workspace on it.
//
// Before this, every instance-wide power (rate limits, log level, Keeper's
// models, notification providers, instance settings, flag definitions)
// reduced to OWNER/ADMIN of whichever workspace the caller named — an admin
// of any workspace could retune the whole server. isInstanceAdmin is the one
// place that answers "may this person change the instance", in this order:
//
//  1. CREWSHIP_OWNER_EMAIL — the break-glass owner, never removable over
//     the API;
//  2. users.instance_role = 'ADMIN' — named by another instance admin, or by
//     `crewship admin instance-admin grant --local` on the host;
//  3. with neither in place, the OWNERs of the oldest live workspace — so a
//     fresh or seeded install, and every install upgraded into this, has an
//     administrator on day one. Naming anyone switches this off: from then
//     on the list is the whole truth.
//
// A suspended account administers nothing, whichever rule would name it.

const (
	instanceAdminSourceEnv  = "env"
	instanceAdminSourceRole = "role"

	// roleInstance declares a route behind the instance gate; scopeInstanceAdmin
	// is the CLI-token scope it requires.
	roleInstance       = "instance"
	scopeInstanceAdmin = "instance:admin"
)

// ErrAccountSuspended is a sign-in refused because an instance admin
// suspended the account. Answered on the wire exactly like bad credentials.
var ErrAccountSuspended = errors.New("account suspended")

// accountSuspended reports whether the account is suspended. A lookup
// failure answers false: the caller already verified the password, and a DB
// hiccup must not look like a suspension.
func accountSuspended(ctx context.Context, db *sql.DB, userID string) bool {
	var at sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT suspended_at FROM users WHERE id = ?`, userID).Scan(&at); err != nil {
		return false
	}
	return at.Valid && at.String != ""
}

// instanceAdminStatus reports whether the user administers the instance and
// by which rule (one of the instanceAdminSource* constants).
func instanceAdminStatus(ctx context.Context, db *sql.DB, userID, email string) (bool, string, error) {
	if userID == "" {
		return false, "", nil
	}
	var role, suspended sql.NullString
	err := db.QueryRowContext(ctx, `SELECT instance_role, suspended_at FROM users WHERE id = ?`, userID).Scan(&role, &suspended)
	if errors.Is(err, sql.ErrNoRows) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	if suspended.Valid && suspended.String != "" {
		return false, "", nil
	}
	if backup.IsInstanceOwner(email) {
		return true, instanceAdminSourceEnv, nil
	}
	if role.String == "ADMIN" {
		return true, instanceAdminSourceRole, nil
	}
	promoted, err := ensureInstanceAdminBootstrap(ctx, db)
	if err != nil {
		return false, "", err
	}
	if promoted[userID] {
		return true, instanceAdminSourceRole, nil
	}
	return false, "", nil
}

// instanceAdminBootstrapKey is the app_settings row recording that the
// one-time bootstrap has run (or was not needed).
const instanceAdminBootstrapKey = "instance.admin_bootstrapped"

// ensureInstanceAdminBootstrap names the first instance admins, once.
//
// It replaces a rule that used to hold forever: "while nobody is named and no
// env owner is configured, the OWNERs of the oldest workspace are instance
// admins". That rule switched itself back on whenever the last named admin
// went, handing the instance to whoever owned that workspace. Now the first
// time anyone's admin status is asked, an install with nobody named and no
// env owner names exactly those owners — the people the old rule already
// treated as admins, so nobody gains or loses anything — records that it did
// (app_settings + the instance audit) and never runs again. An install that
// already names someone, or has an env owner, is only marked done. An install
// with no workspace yet waits for its first one.
//
// Recovery when nobody can sign in to administer: `crewship admin instance
// add-admin --local`, which writes the database on the host.
func ensureInstanceAdminBootstrap(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	var done int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM app_settings WHERE key = ?`, instanceAdminBootstrapKey).Scan(&done); err != nil {
		return nil, err
	}
	if done > 0 {
		return nil, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM app_settings WHERE key = ?`, instanceAdminBootstrapKey).Scan(&done); err != nil {
		return nil, err
	}
	if done > 0 {
		return nil, nil
	}
	mark := func(how string) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO app_settings (key, value, updated_at) VALUES (?, ?, ?)`,
			instanceAdminBootstrapKey, how, time.Now().UTC().Format(time.RFC3339))
		return err
	}
	var named int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE instance_role = 'ADMIN' AND suspended_at IS NULL`).Scan(&named); err != nil {
		return nil, err
	}
	if named > 0 || backup.InstanceOwnerConfigured() {
		if err := mark("already named"); err != nil {
			return nil, err
		}
		return nil, tx.Commit()
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT wm.user_id FROM workspace_members wm JOIN users u ON u.id = wm.user_id
		WHERE wm.role = 'OWNER' AND u.suspended_at IS NULL AND wm.workspace_id = (
			SELECT id FROM workspaces WHERE deleted_at IS NULL ORDER BY created_at, id LIMIT 1
		)`)
	if err != nil {
		return nil, err
	}
	var owners []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		owners = append(owners, id)
	}
	rows.Close()
	if len(owners) == 0 {
		return nil, nil // no workspace yet: ask again once there is one
	}
	promoted := map[string]bool{}
	for _, id := range owners {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET instance_role = 'ADMIN' WHERE id = ?`, id); err != nil {
			return nil, err
		}
		promoted[id] = true
	}
	if err := mark("oldest workspace owners"); err != nil {
		return nil, err
	}
	if err := auditInstance(ctx, nil, tx, "instance.admin_bootstrapped", "instance", "", "", map[string]any{"user_ids": owners}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return promoted, nil
}

// errLastInstanceAdmin ends a change that would leave no active admin.
var errLastInstanceAdmin = errors.New("this is the last active instance administrator; name another one first")

// lastActiveInstanceAdminTx reports whether taking targetID's instance power
// away would leave nobody who can administer the instance: no other named,
// unsuspended admin and no env owner. Run it in the change's transaction, so
// two admins removing each other at once cannot both pass it.
func lastActiveInstanceAdminTx(ctx context.Context, tx *sql.Tx, targetID string) (bool, error) {
	if backup.InstanceOwnerConfigured() {
		return false, nil
	}
	var others int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM users WHERE instance_role = 'ADMIN' AND suspended_at IS NULL AND id != ?`, targetID).Scan(&others); err != nil {
		return false, err
	}
	return others == 0, nil
}

// instanceAdminSourceFor is instanceAdminStatus for a row already in hand
// (a list). "" means not an admin.
func instanceAdminSourceFor(email, instanceRole string, suspended bool) string {
	switch {
	case suspended:
		return ""
	case backup.IsInstanceOwner(email):
		return instanceAdminSourceEnv
	case instanceRole == "ADMIN":
		return instanceAdminSourceRole
	}
	return ""
}

// isInstanceAdmin reports whether the request's caller administers the
// instance. A request without a user is not; a lookup failure is not.
func isInstanceAdmin(r *http.Request, db *sql.DB) bool {
	u := UserFromContext(r.Context())
	if u == nil {
		return false
	}
	ok, _, err := instanceAdminStatus(r.Context(), db, u.ID, u.Email)
	return err == nil && ok
}

// requireInstanceAdminMW refuses anyone who does not administer the instance,
// then applies the instance:admin CLI-token scope.
func (r *Router) requireInstanceAdminMW(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !isInstanceAdmin(req, r.db) {
			writeProblem(w, req, http.StatusForbidden, "Only an instance administrator can do this")
			return
		}
		if !canScope(req.Context(), scopeInstanceAdmin) {
			writeProblem(w, req, http.StatusForbidden, "Forbidden")
			return
		}
		h(w, req.WithContext(context.WithValue(req.Context(), ctxInstanceAdmin, true)))
	})
}

type instanceAdminKey struct{}

// ctxInstanceAdmin marks a request the instance gate has already cleared.
var ctxInstanceAdmin = instanceAdminKey{}

// canAdministerInstance is the in-handler check for an instance-wide setting:
// the instance gate cleared this request, or — for a handler exercised
// without the router — the caller holds a managing workspace role. The
// router is what enforces the instance rule; this stays as defence in depth
// without refusing an instance admin who is only a MEMBER where they stand.
func canAdministerInstance(ctx context.Context) bool {
	if ok, _ := ctx.Value(ctxInstanceAdmin).(bool); ok {
		return true
	}
	return canRole(RoleFromContext(ctx), "manage")
}

// instanceRoute is one entry in the walkable instance-gated route table.
type instanceRoute struct {
	Method  string
	Pattern string
}

// authedInstance registers a route that acts on the instance as a whole —
// people, workspaces, instance admins — behind the instance gate and WITHOUT
// a workspace: an instance admin need not be a member of what they manage.
// Mutations are also recorded with roleInstance so the mutation-route
// invariants and the role manifest see them.
func (r *Router) authedInstance(method, pattern string, h http.HandlerFunc) {
	r.recordInstance(method, pattern)
	r.mux.Handle(method+" "+pattern, r.authMw.RequireAuth(r.requireInstanceAdminMW(h)))
}

// authedInstanceMut registers an instance-wide setting whose handler still
// reads the caller's current workspace (to validate a vault key against it,
// or to keep its existing request shape): workspace membership is resolved
// as for authedMut, then the instance gate replaces the workspace role.
func (r *Router) authedInstanceMut(method, pattern string, h http.HandlerFunc) {
	r.recordInstance(method, pattern)
	r.mux.Handle(method+" "+pattern,
		r.authMw.RequireAuth(r.authMw.RequireWorkspace(r.requireInstanceAdminMW(h))))
}

func (r *Router) recordInstance(method, pattern string) {
	r.instanceRoutes = append(r.instanceRoutes, instanceRoute{Method: method, Pattern: pattern})
	if method != http.MethodGet && method != http.MethodHead {
		r.recordMut(method, pattern, roleInstance, scopeInstanceAdmin)
	}
}

// instanceAuditExecer is the transaction the change runs in.
type instanceAuditExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// auditInstance records an instance-level action. It has no workspace, and
// survives the deletion of the workspace or account it names.
//
// It takes the transaction that makes the change, and its error must fail
// that transaction: a grant of instance power, a suspension or a deletion
// that commits with no audit entry is exactly what this trail exists to rule
// out (review R1, 2026-09-30).
func auditInstance(ctx context.Context, r *http.Request, tx instanceAuditExecer, action, entityType, entityID, targetWorkspaceID string, metadata map[string]any) error {
	userID := ""
	if u := UserFromContext(ctx); u != nil {
		userID = u.ID
	}
	meta := "{}"
	if metadata != nil {
		b, err := json.Marshal(metadata)
		if err != nil {
			return fmt.Errorf("audit metadata: %w", err)
		}
		meta = string(b)
	}
	nullable := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	// r is nil for a change the server makes on its own (the bootstrap).
	ip, ua := "", ""
	if r != nil {
		ip, ua = clientIP(r), r.UserAgent()
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO instance_audit_logs (id, user_id, action, entity_type, entity_id, target_workspace_id, metadata, ip_address, user_agent, created_at)
		VALUES (lower(hex(randomblob(16))), ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		nullable(userID), action, entityType, nullable(entityID), nullable(targetWorkspaceID), meta,
		nullable(ip), nullable(ua), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("instance audit %s: %w", action, err)
	}
	return nil
}
