package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// These helpers belong to private Page content. Board schema remains in the
// migration chain so historical pins keep protecting retained artifact bytes.
const boardHistoryWindow = 24 * time.Hour

var errBoardQuota = errors.New("board mutation quota reached")
var errBoardHistoryExpired = errors.New("board retry history expired; reload before creating a new mutation")

func boardMutationValid(id string) bool {
	return len(id) > 0 && len(id) <= 128 && strings.TrimSpace(id) == id
}

func boardFingerprint(kind string, v any) string {
	raw, _ := json.Marshal(v)
	digest := sha256.Sum256(append([]byte(kind+"\n"), raw...))
	return hex.EncodeToString(digest[:])
}

func (h *PageHandler) boardError(w http.ResponseWriter, err error) {
	if errors.Is(err, errBoardHistoryExpired) {
		artifactCommentError(w, 409, "history_expired", err.Error())
		return
	}
	if errors.Is(err, errBoardQuota) {
		replyError(w, 429, err.Error())
	} else {
		replyInternalError(w, h.logger, "board mutation", err)
	}
}

func (h *PageHandler) loadBoardRequestPage(ctx context.Context, id string) (*pageRecord, error) {
	var slug string
	if err := h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT slug FROM pages WHERE id=? AND workspace_id=?`, id, WorkspaceIDFromContext(ctx)).Scan(&slug); err != nil {
		return nil, err
	}
	return h.loadPage(ctx, WorkspaceIDFromContext(ctx), slug)
}
