package api

// Memory versions list endpoint — Iter 7 of the memory-hardening
// series. Pairs with the stats endpoint (Iter 2) by adding a
// row-level drill-down: stats answers "how much memory does
// workspace X have?", versions answers "which rows specifically?".
//
// Endpoint: GET /api/v1/admin/memory/versions
// Auth:     authed + wsCtx + manage role.
//
// Query parameters (all optional):
//   tier         — exact tier filter (agent / crew / workspace / pins / learned)
//   agent_slug   — extract from canonical "agent:<slug>/..." prefix
//   path_prefix  — match rows whose canonical path starts with this string
//   since        — RFC3339 lower bound on written_at (inclusive)
//   until        — RFC3339 upper bound on written_at (exclusive)
//   limit        — page size, default 50, hard max 500
//   cursor       — opaque keyset pagination cursor from a prior response's
//                  next_cursor field. Encodes (written_at, id) tuple.
//
// Ordering is fixed at written_at DESC, id DESC — newest first,
// stable tie-break on id. This pairs cleanly with the keyset
// cursor: concurrent inserts during pagination cannot shift
// the window because the cursor identifies the LAST row of the
// previous page, not an offset.
//
// Response shape (stable; pinned by the contract test):
//
//   {
//     "workspace_id": "...",
//     "rows": [
//       {
//         "id":          "mv_...",
//         "path":        "agent:martin/AGENT.md",
//         "tier":        "agent",
//         "sha256":      "abc...",
//         "bytes":       1234,
//         "written_at":  "2026-05-18T08:30:00Z",
//         "written_by":  "audit-watcher",
//         "parent_sha":  "..."   // omitted when NULL
//       }
//     ],
//     "next_cursor": "...",     // null on last page
//     "limit":       50,
//     "filters_applied": {       // echo of normalized filters for UI display
//       "tier": "agent",
//       "agent_slug": "martin"
//     }
//   }
//
// Cursor format: base64url(written_at_rfc3339_nano + "|" + id).
// Versioned at v1 via a leading "v1:" prefix so a future change
// to the encoded shape doesn't silently accept stale cursors.

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// memVersionsDefaultLimit is the page size when the caller
	// omits ?limit=. Matches the dashboard table default (50
	// rows fit on a typical 13" laptop without scrolling).
	memVersionsDefaultLimit = 50
	// memVersionsMaxLimit caps the per-page rows. 500 protects
	// the server against a runaway client doing ?limit=1000000
	// and matches the stats query's COUNT cap for consistency.
	memVersionsMaxLimit = 500
	// memVersionsCursorPrefix versions the cursor wire format.
	// A future change (adding a third tuple element, switching
	// to a numeric cursor) would bump this so stale clients
	// don't silently misinterpret a new-format cursor.
	memVersionsCursorPrefix = "v1:"
)

// validTiers enumerates the tier values the `tier` query
// parameter accepts. Mirrors the memory_versions CHECK
// constraint; a request with an unknown tier 400s rather than
// returning an empty result (which would look like "no rows
// for that tier" and hide the typo).
var validTiers = map[string]struct{}{
	"agent":     {},
	"crew":      {},
	"workspace": {},
	"pins":      {},
	"learned":   {},
}

type MemoryVersionsListHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

func NewMemoryVersionsListHandler(db *sql.DB, logger *slog.Logger) *MemoryVersionsListHandler {
	return &MemoryVersionsListHandler{db: db, logger: logger}
}

type memVersionRow struct {
	ID        string  `json:"id"`
	Path      string  `json:"path"`
	Tier      string  `json:"tier"`
	Sha256    string  `json:"sha256"`
	Bytes     int64   `json:"bytes"`
	WrittenAt string  `json:"written_at"`
	WrittenBy string  `json:"written_by"`
	ParentSha *string `json:"parent_sha,omitempty"`
}

type memVersionsListResponse struct {
	WorkspaceID    string            `json:"workspace_id"`
	Rows           []memVersionRow   `json:"rows"`
	NextCursor     *string           `json:"next_cursor"`
	Limit          int               `json:"limit"`
	FiltersApplied map[string]string `json:"filters_applied"`
}

// List serves GET /api/v1/admin/memory/versions.
//
// The keyset pagination is the load-bearing design choice:
// offset pagination would shift rows under concurrent writes
// (the audit watcher writes continuously), producing both
// duplicates (a row inserted at the offset boundary appears on
// page N AND N+1) and gaps (a row deleted at the boundary
// vanishes between pages). The cursor pins (written_at, id),
// so the boundary is fixed and inserts above the cursor land
// on the "next refresh" naturally without disturbing in-flight
// pagination.
func (h *MemoryVersionsListHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	role := RoleFromContext(ctx)
	workspaceID := WorkspaceIDFromContext(ctx)
	if !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "admin role required")
		return
	}
	if workspaceID == "" {
		replyError(w, http.StatusBadRequest, "workspace context required")
		return
	}

	q := r.URL.Query()

	tier := strings.TrimSpace(q.Get("tier"))
	if tier != "" {
		if _, ok := validTiers[tier]; !ok {
			replyError(w, http.StatusBadRequest,
				fmt.Sprintf("unknown tier %q; expected one of agent|crew|workspace|pins|learned", tier))
			return
		}
	}

	agentSlug := strings.TrimSpace(q.Get("agent_slug"))
	pathPrefix := strings.TrimSpace(q.Get("path_prefix"))

	var since, until time.Time
	var err error
	if v := strings.TrimSpace(q.Get("since")); v != "" {
		since, err = time.Parse(time.RFC3339, v)
		if err != nil {
			replyError(w, http.StatusBadRequest, "since must be RFC3339: "+err.Error())
			return
		}
	}
	if v := strings.TrimSpace(q.Get("until")); v != "" {
		until, err = time.Parse(time.RFC3339, v)
		if err != nil {
			replyError(w, http.StatusBadRequest, "until must be RFC3339: "+err.Error())
			return
		}
	}

	limit := memVersionsDefaultLimit
	if v := strings.TrimSpace(q.Get("limit")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			replyError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		if n > memVersionsMaxLimit {
			n = memVersionsMaxLimit
		}
		limit = n
	}

	cursorAt, cursorID, hasCursor := time.Time{}, "", false
	if v := strings.TrimSpace(q.Get("cursor")); v != "" {
		cursorAt, cursorID, err = decodeMemVersionsCursor(v)
		if err != nil {
			replyError(w, http.StatusBadRequest, "invalid cursor: "+err.Error())
			return
		}
		hasCursor = true
	}

	// Build the SELECT. The where-clauses compose into a list of
	// (fragment, args...) pairs and get joined with AND at the
	// end — keeps the SQL readable without resorting to a query
	// builder dependency.
	where := []string{"workspace_id = ?"}
	args := []any{workspaceID}

	if tier != "" {
		where = append(where, "tier = ?")
		args = append(args, tier)
	}
	if agentSlug != "" {
		// agent_slug maps to canonical path prefix
		// "agent:<slug>/..." — matches the encoding the audit
		// watcher writes (see internal/memory/audit_watcher.go's
		// parseMemoryPath) and the stats endpoint's by_agent
		// extraction. ESCAPE '\' below makes literal % / _ in
		// the slug match themselves (an agent named "100%" is
		// pathological but technically valid).
		where = append(where, `path LIKE ? ESCAPE '\'`)
		args = append(args, "agent:"+escapeLikeWildcards(agentSlug)+"/%")
	}
	if pathPrefix != "" {
		// LIKE with explicit % suffix — SQL injection isn't a
		// concern here since pathPrefix is bound, not
		// interpolated, but the user-supplied value MUST be
		// matched as a prefix only. ESCAPE '\' makes literal
		// % / _ in the operator-supplied prefix match
		// themselves rather than acting as wildcards (operator
		// pasting a path that happens to contain '%' should
		// find the literal file, not every file).
		where = append(where, `path LIKE ? ESCAPE '\'`)
		args = append(args, escapeLikeWildcards(pathPrefix)+"%")
	}
	if !since.IsZero() {
		where = append(where, "written_at >= ?")
		args = append(args, since.UTC().Format(time.RFC3339Nano))
	}
	if !until.IsZero() {
		where = append(where, "written_at < ?")
		args = append(args, until.UTC().Format(time.RFC3339Nano))
	}
	if hasCursor {
		// Keyset condition: rows STRICTLY after the cursor in
		// (written_at DESC, id DESC) order — i.e. (written_at,
		// id) lexicographically less than the cursor.
		where = append(where, "(written_at < ? OR (written_at = ? AND id < ?))")
		cursorStr := cursorAt.UTC().Format(time.RFC3339Nano)
		args = append(args, cursorStr, cursorStr, cursorID)
	}

	// Fetch limit+1 rows so we can tell whether there's a next
	// page without a separate COUNT.
	args = append(args, limit+1)
	query := `
		SELECT id, path, tier, sha256, bytes, written_at, COALESCE(written_by, ''), parent_sha
		  FROM memory_versions
		 WHERE ` + strings.Join(where, " AND ") + `
		 ORDER BY written_at DESC, id DESC
		 LIMIT ?`

	rows, err := h.db.QueryContext(ctx, query, args...)
	if err != nil {
		h.logger.Error("memory versions: query", "workspace_id", workspaceID, "error", err)
		replyError(w, http.StatusInternalServerError, "list query failed")
		return
	}
	defer rows.Close()

	out := make([]memVersionRow, 0, capacityHint(limit))
	for rows.Next() {
		var row memVersionRow
		var parent sql.NullString
		if err := rows.Scan(&row.ID, &row.Path, &row.Tier, &row.Sha256,
			&row.Bytes, &row.WrittenAt, &row.WrittenBy, &parent); err != nil {
			h.logger.Error("memory versions: scan", "workspace_id", workspaceID, "error", err)
			replyError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		if parent.Valid && parent.String != "" {
			s := parent.String
			row.ParentSha = &s
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("memory versions: iterate", "workspace_id", workspaceID, "error", err)
		replyError(w, http.StatusInternalServerError, "iterate failed")
		return
	}

	// If we got limit+1 rows, drop the extra and emit a cursor
	// pointing at the LAST row of the trimmed page.
	var nextCursor *string
	if len(out) > limit {
		// Trim to the limit; the (limit+1)-th row is the look-
		// ahead probe only. The cursor for the NEXT page points
		// at the limit-th row's (written_at, id), so the next
		// request's keyset filter picks up exactly the row we
		// just dropped here.
		probe := out[limit-1]
		out = out[:limit]
		probeAt, perr := time.Parse(time.RFC3339Nano, probe.WrittenAt)
		if perr != nil {
			// Stored timestamp could not be re-parsed — data
			// integrity issue. Return what we have without a
			// cursor (effectively "you can't page past this"
			// rather than 500 the whole request).
			h.logger.Warn("memory versions: cursor encode failed",
				"workspace_id", workspaceID, "row_id", probe.ID, "error", perr)
		} else {
			c := encodeMemVersionsCursor(probeAt, probe.ID)
			nextCursor = &c
		}
	}

	writeJSON(w, http.StatusOK, memVersionsListResponse{
		WorkspaceID:    workspaceID,
		Rows:           out,
		NextCursor:     nextCursor,
		Limit:          limit,
		FiltersApplied: collectAppliedFilters(tier, agentSlug, pathPrefix, since, until),
	})
}

// collectAppliedFilters echoes back the resolved filters so the
// UI can render breadcrumbs without re-parsing the request.
// Empty-value keys are omitted so the map is always small.
func collectAppliedFilters(tier, agentSlug, pathPrefix string, since, until time.Time) map[string]string {
	out := map[string]string{}
	if tier != "" {
		out["tier"] = tier
	}
	if agentSlug != "" {
		out["agent_slug"] = agentSlug
	}
	if pathPrefix != "" {
		out["path_prefix"] = pathPrefix
	}
	if !since.IsZero() {
		out["since"] = since.UTC().Format(time.RFC3339)
	}
	if !until.IsZero() {
		out["until"] = until.UTC().Format(time.RFC3339)
	}
	return out
}

// encodeMemVersionsCursor builds the opaque pagination cursor
// from a (written_at, id) tuple. Versioned with a leading
// "v1:" prefix so a future format change (e.g. adding a third
// disambiguator) doesn't silently accept stale cursors.
func encodeMemVersionsCursor(at time.Time, id string) string {
	raw := memVersionsCursorPrefix + at.UTC().Format(time.RFC3339Nano) + "|" + id
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeMemVersionsCursor reverses encodeMemVersionsCursor. The
// (time, id, err) shape mirrors what the handler needs; format
// errors surface as 400s rather than 500s because the cursor
// came from the request.
func decodeMemVersionsCursor(s string) (time.Time, string, error) {
	dec, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("base64 decode: %w", err)
	}
	str := string(dec)
	if !strings.HasPrefix(str, memVersionsCursorPrefix) {
		return time.Time{}, "", errors.New("missing or unrecognised cursor version prefix")
	}
	str = strings.TrimPrefix(str, memVersionsCursorPrefix)
	parts := strings.SplitN(str, "|", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return time.Time{}, "", errors.New("cursor must encode 'timestamp|id'")
	}
	at, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", fmt.Errorf("cursor timestamp: %w", err)
	}
	return at, parts[1], nil
}

// escapeLikeWildcards prepares a user-supplied prefix for use
// as the LHS of a LIKE clause. Without escaping, an input like
// "agent:martin/100%coverage" would interpret the '%' as a
// wildcard and produce a much larger result set than the user
// intended. We escape using a backslash, matching the
// `ESCAPE '\'` clause attached to every LIKE comparison in the
// query builder above. With escape-and-attach paired here both
// operator typos AND a future audit-watcher path containing
// '%' or '_' resolve correctly.
func escapeLikeWildcards(s string) string {
	// Defensive replacements; future-proof against operator-
	// supplied prefixes that contain literal wildcards.
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}
