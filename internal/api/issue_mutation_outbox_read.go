package api

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
)

type failedIssueMutationJob struct {
	ID          string  `json:"id"`
	Kind        string  `json:"kind"`
	Attempts    int     `json:"attempts"`
	MaxAttempts int     `json:"max_attempts"`
	CreatedAt   string  `json:"created_at"`
	FailedAt    *string `json:"failed_at"`
	ErrorCode   string  `json:"error_code"`
}
type failedIssueMutationPage struct {
	Jobs       []failedIssueMutationJob `json:"jobs"`
	NextCursor string                   `json:"next_cursor"`
	HasMore    bool                     `json:"has_more"`
}
type failedIssueMutationCursor struct {
	Workspace string `json:"w"`
	User      string `json:"u"`
	Created   string `json:"c"`
	ID        string `json:"i"`
}

func (r *Router) encodeFailedIssueMutationCursor(c failedIssueMutationCursor) (string, error) {
	a, err := r.entityCursorCipher()
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, a.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(a.Seal(nonce, nonce, raw, []byte("issue-outbox-failed:v1"))), nil
}
func (r *Router) decodeFailedIssueMutationCursor(raw string) (failedIssueMutationCursor, error) {
	var c failedIssueMutationCursor
	a, err := r.entityCursorCipher()
	if err != nil {
		return c, err
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return c, err
	}
	if len(data) < a.NonceSize() {
		return c, strconv.ErrSyntax
	}
	data, err = a.Open(nil, data[:a.NonceSize()], data[a.NonceSize():], []byte("issue-outbox-failed:v1"))
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(data, &c)
	if err == nil && (c.Created == "" || c.ID == "") {
		err = strconv.ErrSyntax
	}
	return c, err
}

// failedIssueMutationJobs exposes delivery diagnostics, never the source
// payload, comment, requester receipt, raw transport error or a retry action.
func (r *Router) failedIssueMutationJobs(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	user := UserFromContext(req.Context())
	workspace := WorkspaceIDFromContext(req.Context())
	if user == nil || !canScope(req.Context(), "workspace:admin") {
		replyError(w, 403, "workspace_admin_required")
		return
	}
	var allowed bool
	err := r.db.QueryRowContext(req.Context(), `SELECT EXISTS(SELECT 1 FROM workspace_members wm JOIN workspaces ws ON ws.id=wm.workspace_id AND ws.deleted_at IS NULL JOIN users u ON u.id=wm.user_id AND u.suspended_at IS NULL WHERE wm.workspace_id=? AND wm.user_id=? AND wm.role IN ('OWNER','ADMIN') AND wm.access_mode='trusted' AND NOT EXISTS(SELECT 1 FROM workspace_members denied WHERE denied.user_id=wm.user_id AND denied.access_mode='restricted'))`, workspace, user.ID).Scan(&allowed)
	if err != nil {
		replyError(w, 503, "outbox_unavailable")
		return
	}
	if !allowed {
		replyError(w, 403, "workspace_admin_required")
		return
	}
	if state := req.URL.Query().Get("state"); state != "" && state != "failed" {
		replyError(w, 400, "state_invalid")
		return
	}
	limit := 50
	if raw := req.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			replyError(w, 400, "limit_invalid")
			return
		}
	}
	c := failedIssueMutationCursor{Workspace: workspace, User: user.ID}
	if raw := req.URL.Query().Get("cursor"); raw != "" {
		if len(raw) > 4096 {
			replyError(w, 400, "cursor_invalid")
			return
		}
		c, err = r.decodeFailedIssueMutationCursor(raw)
		if err != nil || c.Workspace != workspace || c.User != user.ID {
			replyError(w, 400, "cursor_invalid")
			return
		}
	}
	rows, err := r.db.QueryContext(req.Context(), `SELECT id,kind,attempts,max_attempts,created_at,failed_at,failure_code FROM issue_mutation_outbox WHERE workspace_id=? AND state='failed' AND (created_at>? OR (created_at=? AND id>?)) ORDER BY created_at,id LIMIT ?`, workspace, c.Created, c.Created, c.ID, limit+1)
	if err != nil {
		replyError(w, 503, "outbox_unavailable")
		return
	}
	defer rows.Close()
	out := failedIssueMutationPage{Jobs: []failedIssueMutationJob{}}
	for rows.Next() {
		var job failedIssueMutationJob
		var code string
		if err = rows.Scan(&job.ID, &job.Kind, &job.Attempts, &job.MaxAttempts, &job.CreatedAt, &job.FailedAt, &code); err != nil {
			replyError(w, 503, "outbox_unavailable")
			return
		}
		switch code {
		case "invalid_payload", "delivery_exhausted", "authority_revoked":
			job.ErrorCode = code
		default:
			job.ErrorCode = "unknown"
		}
		out.Jobs = append(out.Jobs, job)
	}
	if rows.Err() != nil {
		replyError(w, 503, "outbox_unavailable")
		return
	}
	out.HasMore = len(out.Jobs) > limit
	if out.HasMore {
		out.Jobs = out.Jobs[:limit]
		last := out.Jobs[len(out.Jobs)-1]
		out.NextCursor, err = r.encodeFailedIssueMutationCursor(failedIssueMutationCursor{Workspace: workspace, User: user.ID, Created: last.CreatedAt, ID: last.ID})
		if err != nil {
			replyError(w, 503, "outbox_unavailable")
			return
		}
	}
	writeJSON(w, 200, out)
}
