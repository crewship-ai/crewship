package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// agentExists checks that an agent with the given ID belongs to the workspace
// and is not soft-deleted. Returns nil on success, sql.ErrNoRows if not found.
func agentExists(ctx context.Context, db *sql.DB, agentID, workspaceID string) error {
	var id string
	return db.QueryRowContext(ctx,
		"SELECT id FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		agentID, workspaceID).Scan(&id)
}

// validSlugRe matches safe slug values: lowercase alphanumeric, starting with a letter or digit.
var validSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// validSlugFormat validates that a slug contains only safe characters.
func validSlugFormat(slug string) bool {
	return validSlugRe.MatchString(slug)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

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

func newUpdate() *updateBuilder {
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
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

func canRole(role string, actions ...string) bool {
	for _, action := range actions {
		switch action {
		case "create":
			switch role {
			case "OWNER", "ADMIN", "MANAGER":
				continue
			default:
				return false
			}
		case "manage":
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
