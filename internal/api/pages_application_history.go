package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
)

// ApplicationPanelHistory exposes only the readable panel ring, never producer
// tokens, run metadata or an arbitrary API endpoint. Publication fences also
// apply to reads initiated by a stale or withdrawn application.
func (h *PageHandler) ApplicationPanelHistory(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, 401, "Unauthorized")
		return
	}
	publication, err := strconv.ParseInt(r.URL.Query().Get("publication"), 10, 64)
	if err != nil || publication < 1 {
		replyError(w, 400, "publication is required")
		return
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 20 {
			replyError(w, 400, "limit must be between 1 and 20")
			return
		}
	}
	before := int64(0)
	if raw := r.URL.Query().Get("before"); raw != "" {
		before, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || before < 1 {
			replyError(w, 400, "before must be a positive sequence")
			return
		}
	}
	rec, panel, err := h.resolvePanelForCaller(r.Context(), WorkspaceIDFromContext(r.Context()), user.ID, r.PathValue("slug"), r.PathValue("panelId"))
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, errActionNotVisible) {
		replyError(w, 404, "Page panel not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "resolve panel history", err)
		return
	}
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		replyInternalError(w, h.logger, "read panel history", err)
		return
	}
	defer tx.Rollback()
	var version int64
	err = tx.QueryRowContext(r.Context(), `SELECT l.version FROM page_project_live l JOIN page_project_publications v ON v.page_id=l.page_id AND v.version=l.version JOIN pages p ON p.id=l.page_id WHERE l.page_id=? AND l.published=1 AND p.spec_json=v.spec_json`, rec.ID).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && version != publication) {
		replyError(w, 409, "Page publication changed; reload the application")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "read publication fence", err)
		return
	}
	rows, err := tx.QueryContext(r.Context(), `SELECT seq,payload_json,produced_at,state FROM page_panel_data WHERE panel_id=? AND (?=0 OR seq<?) ORDER BY seq DESC LIMIT ?`, panel.RowID, before, before, limit+1)
	if err != nil {
		replyInternalError(w, h.logger, "read panel history", err)
		return
	}
	defer rows.Close()
	type item struct {
		Sequence   int64           `json:"sequence"`
		Data       json.RawMessage `json:"data"`
		ProducedAt string          `json:"producedAt"`
		State      string          `json:"state"`
	}
	// Allocation stays fixed even if request validation changes later.
	items := make([]item, 0, 20)
	used := 128 // envelope, cursors and array delimiters
	next := int64(0)
	for rows.Next() {
		var row item
		var raw string
		if err := rows.Scan(&row.Sequence, &raw, &row.ProducedAt, &row.State); err != nil {
			replyInternalError(w, h.logger, "decode panel history", err)
			return
		}
		row.Data = json.RawMessage(raw)
		encoded, encodeErr := json.Marshal(row)
		if encodeErr != nil {
			replyError(w, 500, "Invalid stored panel data")
			return
		}
		// Match writeJSON's HTML escaping; counting raw JSON can undercount
		// strings containing <, > or & by a factor of six.
		if len(items) == limit || used+len(encoded)+1 > 1024*1024 {
			if len(items) == 0 {
				replyError(w, 413, "Panel history entry exceeds the 1 MiB response limit")
				return
			}
			next = items[len(items)-1].Sequence
			break
		}
		used += len(encoded) + 1
		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		replyInternalError(w, h.logger, "read panel history", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"items": items, "next_before": next, "publication": version})
}
