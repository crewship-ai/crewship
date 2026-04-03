package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

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
