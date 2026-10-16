package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// Limit simultaneous decoded uploads in the single writer process. Waiting
// requests retain their transport stream, not a 60 MiB JSON buffer. This covers
// human publication and restore.
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

func decodeArtifactJSON(w http.ResponseWriter, r *http.Request, target *artifactMutation) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 6*maxArtifactHTMLBytes+4096)
	decoder := json.NewDecoder(r.Body)
	fail := func(err error) bool {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			replyError(w, 413, "artifact request too large")
		} else {
			replyError(w, 400, "artifact request must contain one object with exact known fields and non-null values")
		}
		return false
	}
	token, err := decoder.Token()
	if err != nil {
		return fail(err)
	}
	if token != json.Delim('{') {
		return fail(fmt.Errorf("object required"))
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return fail(err)
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return fail(fmt.Errorf("duplicate key"))
		}
		seen[key] = true
		var field any
		switch key {
		case "page_id":
			field = &target.PageID
		case "html":
			field = &target.HTML
		case "base_rev":
			field = &target.BaseRev
		case "mutation_id":
			field = &target.MutationID
		case "revision":
			field = &target.Revision
		case "format_version":
			field = &target.FormatVersion
		case "runtime_profile":
			field = &target.RuntimeProfile
		default:
			return fail(fmt.Errorf("unknown key"))
		}
		var raw json.RawMessage
		if err = decoder.Decode(&raw); err != nil {
			return fail(err)
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fail(fmt.Errorf("null field"))
		}
		if err = json.Unmarshal(raw, field); err != nil {
			return fail(err)
		}
	}
	if _, err = decoder.Token(); err != nil {
		return fail(err)
	}
	if _, err = decoder.Token(); err != io.EOF {
		if err != nil {
			return fail(err)
		}
		return fail(fmt.Errorf("trailing input"))
	}
	for _, key := range []string{"page_id", "base_rev", "mutation_id"} {
		if !seen[key] {
			return fail(fmt.Errorf("required field missing"))
		}
	}
	return true
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
