package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

// tokenFingerprint returns a short, non-reversible identifier safe to
// log in place of a raw token: a 6-char value prefix joined by ".." to
// the first 8 hex chars of the SHA-256 of the full value. ~16 chars
// total so log lines stay scannable. The prefix lets an operator
// visually correlate two log lines for the same token without exposing
// enough to authenticate; the SHA suffix guarantees collision
// resistance even for short prefixes.
//
// Empty string maps to the literal "<empty>" so an unset/cleared token
// is still distinguishable from the absence of the field in the log.
func tokenFingerprint(s string) string {
	if s == "" {
		return "<empty>"
	}
	sum := sha256.Sum256([]byte(s))
	prefix := s
	if len(prefix) > 6 {
		prefix = prefix[:6]
	}
	return prefix + ".." + hex.EncodeToString(sum[:4])
}

// broadcastChannelEvent is an api-package shortcut to hub.BroadcastChannel.
// No-op if hub is nil (e.g. when the API is initialized without a WebSocket hub
// in tests or headless tooling), so call sites don't need their own nil guard.
func broadcastChannelEvent(hub *ws.Hub, prefix, id, eventType string, payload any) {
	if hub == nil {
		return
	}
	hub.BroadcastChannel(prefix, id, eventType, payload)
}

// broadcastWorkspaceEvent is an api-package shortcut to hub.BroadcastWorkspace.
// No-op if hub is nil — see broadcastChannelEvent.
func broadcastWorkspaceEvent(hub *ws.Hub, wsID, eventType string, payload any) {
	if hub == nil {
		return
	}
	hub.BroadcastWorkspace(wsID, eventType, payload)
}

// isSafeRedirect validates that a redirect target is a same-origin
// relative path. The check is "leading slash, second char is neither
// `/` nor `\\`, no embedded backslashes anywhere, parseable, and the
// parsed URL has no scheme/host" — which together close the open
// redirect classes CodeQL go/bad-redirect-check flags:
//
//   - "//evil.com/path"   — protocol-relative; second char is '/'
//   - "/\\evil.com/path"  — backslash trick that some browsers
//     normalise to '/' before resolving
//   - "https://evil.com"  — absolute URL with explicit scheme
//   - "javascript:..."    — non-http(s) scheme
//
// Empty strings are rejected so caller code doesn't accidentally
// redirect to the current path on a missing param.
func isSafeRedirect(target string) bool {
	if target == "" {
		return false
	}
	// Must start with "/"
	if target[0] != '/' {
		return false
	}
	// The second char being '/' or '\\' is the classic protocol-
	// relative bypass that strings.HasPrefix(target, "//") alone
	// misses (CodeQL go/bad-redirect-check).
	if len(target) > 1 && (target[1] == '/' || target[1] == '\\') {
		return false
	}
	// Any backslash anywhere (some browsers normalize "\/" → "//").
	if strings.Contains(target, "\\") {
		return false
	}
	// Defence in depth: a malformed URL or one that parses with a
	// host (means an absolute URL slipped through) is not a same-
	// origin relative path.
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return false
	}
	return true
}

// maxListCapacity is the upper bound on any per-request slice/map
// pre-allocation derived from a ?limit= query param. parseListPagination
// already clamps to a per-endpoint max, but CodeQL's
// go/uncontrolled-allocation-size rule can only see proximate bounds —
// applying capacityHint at every make(...) call gives the analyser the
// local evidence and protects against future code paths that bypass
// parseListPagination.
const maxListCapacity = 1000

// capacityHint returns a safe cap-hint for a user-derived list size,
// floored at 0 and ceiled at maxListCapacity. Uses the Go 1.21+ min
// builtin so CodeQL's go/uncontrolled-allocation-size rule recognises
// the cap as a bound on the inflow value.
func capacityHint(n int) int {
	if n < 0 {
		return 0
	}
	return min(n, maxListCapacity)
}

// parsePagination reads "limit" and "offset" query params, clamping limit to
// [1, maxLimit] with the given default, and offset to >= 0.
//
// Unparseable, missing, or non-positive limits fall back to defaultLimit.
// Limits larger than maxLimit are clamped DOWN to maxLimit (not reset to
// defaultLimit) — otherwise a request for ?limit=1000 against
// (defaultLimit=20, maxLimit=100) would silently return 20 instead of 100 and
// shift pagination windows in surprising ways.
func parsePagination(r *http.Request, defaultLimit, maxLimit int) (limit, offset int) {
	limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ = strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 {
		limit = defaultLimit
	} else if limit > maxLimit {
		limit = maxLimit
	}
	if offset < 0 {
		offset = 0
	}
	return
}

// sqlPlaceholderCache stores pre-built placeholder strings for the sizes that
// show up on the batch-loader hot path (single-digit to low-double-digit
// IN clauses). A cache hit is a zero-alloc table lookup; a miss falls
// through to the single-allocation builder below.
var sqlPlaceholderCache = func() [64]string {
	var cache [64]string
	for n := 1; n < len(cache); n++ {
		cache[n] = buildPlaceholders(n)
	}
	return cache
}()

// buildPlaceholders writes "?,?,?..." of length n directly into a single
// byte buffer, avoiding the intermediate []string + strings.Join that the
// previous implementation paid.
func buildPlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	buf := make([]byte, 2*n-1)
	buf[0] = '?'
	for i := 1; i < n; i++ {
		buf[2*i-1] = ','
		buf[2*i] = '?'
	}
	return string(buf)
}

// sqlPlaceholders returns a comma-separated string of n "?" placeholders
// for use in SQL IN clauses (e.g. "?,?,?").
func sqlPlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	if n < len(sqlPlaceholderCache) {
		return sqlPlaceholderCache[n]
	}
	return buildPlaceholders(n)
}

// agentExists checks that an agent with the given ID belongs to the workspace
// and is not soft-deleted. Returns (true, nil) if the agent exists,
// (false, nil) if it does not exist, or (false, err) on a real DB failure.
// Callers must distinguish the two zero-value cases: a false/nil return means
// "legitimately not found" (map to 404), while a non-nil err means an
// operational error (map to 500).
func agentExists(ctx context.Context, db *sql.DB, agentID, workspaceID string) (bool, error) {
	var id string
	err := db.QueryRowContext(ctx,
		"SELECT id FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		agentID, workspaceID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// crewExists checks that a crew with the given ID belongs to the workspace
// and is not soft-deleted. Returns (true, nil) if the crew exists,
// (false, nil) if it does not exist, or (false, err) on a real DB failure.
// See agentExists for the 404-vs-500 contract.
func crewExists(ctx context.Context, db *sql.DB, crewID, workspaceID string) (bool, error) {
	var id string
	err := db.QueryRowContext(ctx,
		"SELECT id FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		crewID, workspaceID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// projectExists checks that a project with the given ID belongs to the workspace.
// Returns (true, nil) if the project exists, (false, nil) if it does not exist,
// or (false, err) on a real DB failure. See agentExists for the 404-vs-500 contract.
func projectExists(ctx context.Context, db *sql.DB, projectID, workspaceID string) (bool, error) {
	var id int
	err := db.QueryRowContext(ctx,
		"SELECT 1 FROM projects WHERE id = ? AND workspace_id = ?",
		projectID, workspaceID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// credentialExists checks that a credential with the given ID belongs to the
// workspace and is not soft-deleted. Returns (true, nil) if the credential
// exists, (false, nil) if it does not exist, or (false, err) on a real DB
// failure. See agentExists for the 404-vs-500 contract.
func credentialExists(ctx context.Context, db *sql.DB, credentialID, workspaceID string) (bool, error) {
	var id string
	err := db.QueryRowContext(ctx,
		"SELECT id FROM credentials WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		credentialID, workspaceID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// deleteByID issues the canonical workspace-scoped single-table delete that
// many simple resources share:
//
//	DELETE FROM <table> WHERE id = ? AND workspace_id = ?
//
// and reports whether a row was actually removed. A (false, nil) return means
// "no such row in this workspace" — callers map that to 404 — while a non-nil
// err is an operational failure to map to 500.
//
// `table` MUST be a trusted compile-time constant supplied by the handler,
// never user input: it is interpolated directly into the SQL string, so a
// request-derived value would be a SQL-injection vector. The id and
// workspace_id values stay bound parameters and are safe.
func deleteByID(ctx context.Context, db *sql.DB, table, id, wsID string) (bool, error) {
	res, err := db.ExecContext(ctx,
		"DELETE FROM "+table+" WHERE id = ? AND workspace_id = ?", id, wsID)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

// validSlugRe matches safe slug values: lowercase alphanumeric, starting with a letter or digit.
var validSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// validSlugFormat validates that a slug contains only safe characters.
func validSlugFormat(slug string) bool {
	return validSlugRe.MatchString(slug)
}

// writeJSON serializes v as JSON and writes it to the response with the given HTTP status code.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// replyError writes a JSON error response with the canonical {"error": msg} shape.
// Wraps the previously repeated writeJSON(w, status, map[string]string{"error": ...}) idiom.
func replyError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// internalError is the canonical "log the error, return a 500" tail that was
// hand-written at hundreds of handler sites. It reproduces the dominant idiom
// exactly:
//
//	logger.Error(msg, "error", err)
//	writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
//
// The logger is guarded so a nil logger (e.g. a handler constructed without
// one in a test) degrades to just writing the problem response rather than
// panicking. Callers still issue their own `return` after this call.
func internalError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, msg string, err error) {
	if logger != nil {
		logger.Error(msg, "error", err)
	}
	writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
}

// readJSON decodes the request body (up to 1 MB) into v.
func readJSON(r *http.Request, v interface{}) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB limit
	if err != nil {
		return err
	}
	return json.Unmarshal(body, v)
}

// requireRole checks the request's actor role against canRole. On failure it
// writes a 403 problem response (matching writeProblem's RFC 7807 shape) and
// returns false; callers should "return" immediately when this returns false.
//
// The previously repeated pattern was:
//
//	role := RoleFromContext(r.Context())
//	if !canRole(role, "create") {
//	    writeProblem(w, r, http.StatusForbidden, "Forbidden")
//	    return
//	}
//
// and now collapses to:
//
//	if !requireRole(w, r, "create") { return }
func requireRole(w http.ResponseWriter, r *http.Request, actions ...string) bool {
	if !canRole(RoleFromContext(r.Context()), actions...) {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return false
	}
	return true
}

// updateBuilder accumulates SET clauses for dynamic UPDATE queries.
// Always includes "updated_at = ?" as the first clause.
type updateBuilder struct {
	sets []string
	args []any
}

// newUpdate creates an updateBuilder pre-populated with an updated_at clause.
func newUpdate() *updateBuilder {
	now := time.Now().UTC().Format(time.RFC3339)
	return &updateBuilder{
		sets: []string{"updated_at = ?"},
		args: []any{now},
	}
}

// Set adds a "column = ?" clause with the given value.
func (u *updateBuilder) Set(column string, value any) {
	u.sets = append(u.sets, column+" = ?")
	u.args = append(u.args, value)
}

// SetNull adds a "column = NULL" clause (no placeholder needed).
func (u *updateBuilder) SetNull(column string) {
	u.sets = append(u.sets, column+" = NULL")
}

// Build returns the full UPDATE query and combined args slice.
func (u *updateBuilder) Build(table, whereClause string, whereArgs ...any) (string, []any) {
	query := "UPDATE " + table + " SET " + strings.Join(u.sets, ", ") + " WHERE " + whereClause
	return query, append(u.args, whereArgs...)
}

// Empty returns true if only the default updated_at clause was added.
func (u *updateBuilder) Empty() bool {
	return len(u.sets) <= 1
}

// canRole returns true if the given workspace role is allowed to perform all of the specified actions.
// Supported actions:
//   - "read"   — any authenticated role
//   - "create" — OWNER, ADMIN, MANAGER
//   - "update" — OWNER, ADMIN, MANAGER (same tier as create; reversible mutations)
//   - "manage" — OWNER, ADMIN
//   - "delete" — OWNER, ADMIN (same tier as manage; destructive)
//
// Empty role is rejected for every action — defense-in-depth against an auth
// middleware bypass that would otherwise let unauthenticated callers through
// the read tier.
// roleRank orders the workspace roles so we can compute "max" between a
// user's workspace role and any per-crew override. Pre-Patch-M1 the
// workspace role was the only source; v99 added crew_members.role as
// an opt-in elevation per crew (a MANAGER workspace member can be
// promoted to ADMIN inside a specific crew, but NEVER demoted below
// the workspace floor). Unknown / empty roles rank 0 so a missing
// value never accidentally outranks a real one.
//
// The list is hard-coded rather than computed because the role set is
// a hard product surface — adding a role is a deliberate change, not
// data the runtime should infer.
var roleRank = map[string]int{
	"VIEWER":  1,
	"MEMBER":  2,
	"MANAGER": 3,
	"ADMIN":   4,
	"OWNER":   5,
}

// effectiveRole returns the higher-ranked of two roles, treating empty
// or unknown as the lowest (0). Used by the crew-scoped permission
// helpers so a workspace MANAGER who has been promoted to ADMIN inside
// a particular crew passes the crew-scoped admin gate, while their
// workspace role still gates workspace-wide endpoints. Per-crew role
// can never DROP below the workspace floor — that's enforced by
// taking max here, not min.
func effectiveRole(workspaceRole, crewRole string) string {
	if roleRank[crewRole] > roleRank[workspaceRole] {
		return crewRole
	}
	return workspaceRole
}

func canRole(role string, actions ...string) bool {
	if role == "" {
		return false
	}
	for _, action := range actions {
		switch action {
		case "create", "update":
			switch role {
			case "OWNER", "ADMIN", "MANAGER":
				continue
			default:
				return false
			}
		case "manage", "delete":
			switch role {
			case "OWNER", "ADMIN":
				continue
			default:
				return false
			}
		case "read":
			continue
		default:
			return false
		}
	}
	return true
}
