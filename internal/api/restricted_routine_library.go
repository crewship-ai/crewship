package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/restrictedworkflow"
)

// View is explicit: existing Run grants retain their narrow invocation contract.
// Definition reads never resolve vault credentials or disclose private runs.
type guestRoutineLibraryItem struct {
	ID           string `json:"id"`
	Slug         string `json:"slug"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	CanView      bool   `json:"can_view"`
	CanRun       bool   `json:"can_run"`
	RuntimeReady bool   `json:"runtime_ready"`
}
type guestRoutineDefinition struct {
	guestRoutineLibraryItem
	Definition     json.RawMessage `json:"definition"`
	DefinitionHash string          `json:"definition_hash"`
	Version        int             `json:"version"`
}

func guestRoutineMember(ctx context.Context, tx *sql.Tx, user, workspace string) (access.Membership, error) {
	var m access.Membership
	err := tx.QueryRowContext(ctx, `SELECT wm.id,wm.access_mode,wm.access_revision FROM workspace_members wm JOIN users u ON u.id=wm.user_id JOIN workspaces w ON w.id=wm.workspace_id WHERE wm.user_id=? AND wm.workspace_id=? AND wm.access_mode='restricted' AND u.suspended_at IS NULL AND w.deleted_at IS NULL`, user, workspace).Scan(&m.ID, &m.Mode, &m.Revision)
	return m, err
}

func (r *Router) restrictedRoutineLibrary(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user := UserFromContext(req.Context())
	workspace, matches := memberAccessWorkspace(req)
	if user == nil || !matches {
		replyError(w, 404, "Routine unavailable")
		return
	}
	// Invocation eligibility uses the existing graph/provider/agent ceiling.
	// Definition reads never require an execution runtime or agent Run grants.
	runnable, err := restrictedworkflow.AuthorizedCatalog(req.Context(), r.db, user.ID, workspace)
	if err != nil && !errors.Is(err, restrictedworkflow.ErrDenied) {
		replyError(w, 500, "Routine unavailable")
		return
	}
	authorized := map[string]string{}
	for _, item := range runnable {
		authorized[item.ID] = item.DefinitionHash
	}
	tx, err := r.db.BeginTx(req.Context(), nil)
	if err != nil {
		replyError(w, 500, "Routine unavailable")
		return
	}
	defer tx.Rollback()
	m, err := guestRoutineMember(req.Context(), tx, user.ID, workspace)
	if err != nil {
		replyError(w, 404, "Routine unavailable")
		return
	}
	id := req.PathValue("routineId")
	rows, err := tx.QueryContext(req.Context(), `SELECT p.id,p.slug,p.name,p.status,p.definition_hash,
       EXISTS(SELECT 1 FROM live_access_grants v WHERE v.member_id=? AND v.resource_kind='routine' AND v.routine_id=p.id AND v.operation='view')
       FROM pipelines p WHERE p.workspace_id=? AND p.deleted_at IS NULL AND (?='' OR p.id=? OR p.slug=?)
       AND EXISTS(SELECT 1 FROM live_access_grants g WHERE g.member_id=? AND g.resource_kind='routine' AND g.routine_id=p.id AND g.operation IN ('view','run')) ORDER BY p.slug`, m.ID, workspace, id, id, id, m.ID)
	if err != nil {
		replyError(w, 500, "Routine unavailable")
		return
	}
	items := []guestRoutineLibraryItem{}
	for rows.Next() {
		var item guestRoutineLibraryItem
		var hash string
		if err = rows.Scan(&item.ID, &item.Slug, &item.Name, &item.Status, &hash, &item.CanView); err != nil {
			break
		}
		item.CanRun = authorized[item.ID] != "" && authorized[item.ID] == hash
		item.RuntimeReady = r.restrictedWorkflow != nil
		items = append(items, item)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil || rowErr != nil {
		replyError(w, 500, "Routine unavailable")
		return
	}
	if id == "" {
		w.Header().Set("X-Total-Count", strconv.Itoa(len(items)))
		writeJSON(w, 200, items)
		return
	}
	if len(items) != 1 || !items[0].CanView {
		replyError(w, 404, "Routine unavailable")
		return
	}
	result := guestRoutineDefinition{guestRoutineLibraryItem: items[0]}
	var raw string
	err = tx.QueryRowContext(req.Context(), `SELECT definition_json,definition_hash,head_version FROM pipelines WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, items[0].ID, workspace).Scan(&raw, &result.DefinitionHash, &result.Version)
	if err != nil || !json.Valid([]byte(raw)) {
		replyError(w, 500, "Routine unavailable")
		return
	}
	result.Definition = json.RawMessage(raw)
	writeJSON(w, 200, result)
}
