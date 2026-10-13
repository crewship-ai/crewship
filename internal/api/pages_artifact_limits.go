package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// Limit simultaneous decoded uploads in the single writer process. Waiting
// requests retain their transport stream, not a 60 MiB JSON buffer. This covers
// human publication, restore and agent creation alike.
var artifactUploadSlots = make(chan struct{}, 2)

func acquireArtifactUpload(w http.ResponseWriter, r *http.Request) (func(), bool) {
	select {
	case artifactUploadSlots <- struct{}{}:
		return func() { <-artifactUploadSlots }, true
	case <-r.Context().Done():
		replyError(w, http.StatusRequestTimeout, "artifact upload cancelled while waiting for capacity")
		return nil, false
	}
}

func decodeArtifactJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 6*maxArtifactHTMLBytes+4096)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(target)
	if err == nil {
		err = decoder.Decode(new(any))
		if err == io.EOF {
			return true
		}
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		replyError(w, http.StatusRequestEntityTooLarge, "artifact request too large")
	} else {
		replyError(w, http.StatusBadRequest, "artifact request must contain one valid JSON object with known fields")
	}
	return false
}

// Consume a slot in the same transaction as publication, after exact retries
// were handled. Receipts intentionally survive Page deletion, so spreading
// writes across Pages or deleting them cannot bypass the workspace limit.
func (h *PageHandler) enforceArtifactWorkspaceRate(w http.ResponseWriter, r *http.Request, tx *sql.Tx, workspace string, now time.Time) bool {
	var recent int
	if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM artifact_workspace_writes WHERE workspace_id=? AND created_at>?`, workspace, tsformat.Format(now.Add(-time.Hour))).Scan(&recent); err != nil {
		replyInternalError(w, h.logger, "check workspace artifact write rate", err)
		return false
	}
	if recent >= 200 {
		w.Header().Set("Retry-After", "3600")
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "workspace artifact revision rate limit reached", "code": "workspace_rate_limited"})
		return false
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM artifact_workspace_writes WHERE workspace_id=? AND created_at<=?`, workspace, tsformat.Format(now.Add(-time.Hour))); err != nil {
		replyInternalError(w, h.logger, "expire workspace artifact rate receipts", err)
		return false
	}
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO artifact_workspace_writes(id,workspace_id,created_at) VALUES (?,?,?)`, generateCUID(), workspace, tsformat.Format(now)); err != nil {
		replyInternalError(w, h.logger, "record workspace artifact write rate", err)
		return false
	}
	return true
}
