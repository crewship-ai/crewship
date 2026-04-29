package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

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

// isSafeRedirect validates that a redirect target is a relative path on the
// same origin. It rejects empty strings, absolute URLs, protocol-relative
// URLs (//evil.com), and paths with backslashes to prevent open redirects.
func isSafeRedirect(target string) bool {
	if target == "" {
		return false
	}
	// Must start with "/" (relative path)
	if !strings.HasPrefix(target, "/") {
		return false
	}
	// Reject protocol-relative URLs like "//evil.com"
	if strings.HasPrefix(target, "//") {
		return false
	}
	// Reject backslash tricks (some browsers normalize "\/")
	if strings.Contains(target, "\\") {
		return false
	}
	return true
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

// readJSON decodes the request body (up to 1 MB) into v.
func readJSON(r *http.Request, v interface{}) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB limit
	if err != nil {
		return err
	}
	return json.Unmarshal(body, v)
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
