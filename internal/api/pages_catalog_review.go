package api

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

// Review receipts are process-local and confer no authority. A restart requires
// another preview. Current canonical authorization is always performed first.
var pageCatalogReviewKey = func() []byte {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic("page review entropy unavailable")
	}
	return key
}()

type pageCatalogEpoch struct {
	Workspace     string
	User          string
	MemberID      string
	Member        string
	UserRelevance string
	PageRelevance string
}

// A folder_id change fires the two canonical Page triggers and, when an
// existing Page relevance tombstone is present, the two relevance triggers.
// Each relevance increment also closes the broad delivery epoch. No other
// authority change is part of this operation, even if its final ACL is equal.
func epochAfterFolderMove(before pageCatalogEpoch) (pageCatalogEpoch, bool) {
	after := before
	steps := int64(2)
	if before.PageRelevance != "" {
		n, err := strconv.ParseInt(before.PageRelevance, 10, 64)
		if err != nil || n < 1 || n > int64(^uint64(0)>>1)-2 {
			return after, false
		}
		after.PageRelevance = strconv.FormatInt(n+2, 10)
		steps += 2
	}
	n, err := strconv.ParseInt(before.Workspace, 10, 64)
	if err != nil || n < 1 || n > int64(^uint64(0)>>1)-steps {
		return after, false
	}
	after.Workspace = strconv.FormatInt(n+steps, 10)
	return after, true
}

func (h *PageHandler) catalogReviewEpoch(r *http.Request, pageID string) (pageCatalogEpoch, error) {
	var e pageCatalogEpoch
	err := h.pageAuthReader(r.Context()).QueryRowContext(r.Context(), `
	 SELECT CAST(w.authority_revision AS TEXT),CAST(u.authority_revision AS TEXT),m.id,CAST(m.access_revision AS TEXT),
	 COALESCE((SELECT CAST(revision AS TEXT) FROM mooring_authority_generations WHERE workspace_id=w.id AND scope_kind='user' AND scope_id=u.id),''),
	 COALESCE((SELECT CAST(revision AS TEXT) FROM mooring_authority_generations WHERE workspace_id=w.id AND scope_kind='page' AND scope_id=?),'')
	 FROM workspaces w JOIN workspace_members m ON m.workspace_id=w.id JOIN users u ON u.id=m.user_id
	 WHERE w.id=? AND u.id=? AND w.deleted_at IS NULL AND u.suspended_at IS NULL
	 AND NOT EXISTS(SELECT 1 FROM workspace_members restricted WHERE restricted.user_id=u.id AND restricted.access_mode='restricted')`, pageID, WorkspaceIDFromContext(r.Context()), UserFromContext(r.Context()).ID).Scan(&e.Workspace, &e.User, &e.MemberID, &e.Member, &e.UserRelevance, &e.PageRelevance)
	return e, err
}

func pageReviewToken(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, pageCatalogReviewKey)
	_, _ = mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func validPageReviewToken(got, want string) bool {
	return len(got) == 64 && hmac.Equal([]byte(got), []byte(want))
}

func hasPageReviewFields(raw []byte) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	for key := range fields {
		for _, field := range []string{"dry_run", "confirm", "review_token"} {
			if strings.EqualFold(key, field) {
				return true
			}
		}
	}
	return false
}

func pageReviewFieldsNonNull(raw []byte) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	for _, value := range fields {
		if strings.TrimSpace(string(value)) == "null" {
			return false
		}
	}
	return true
}

// Reuse the strict bounded codec to reject duplicate, case-aliased, unknown
// and trailing fields. Numbers never pass through float64.
func decodePageCatalogReview(w http.ResponseWriter, r *http.Request, value any, fields ...string) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	raw, err := io.ReadAll(r.Body)
	if err == nil && !pageReviewFieldsNonNull(raw) {
		replyError(w, 400, "review fields cannot be null")
		return false
	}
	if err == nil {
		err = artifacts.DecodeDataJSON(raw, value, fields...)
	}
	if err != nil {
		replyError(w, http.StatusBadRequest, "invalid review request: "+err.Error())
		return false
	}
	return true
}

func (h *PageHandler) catalogReviewViewer(w http.ResponseWriter, r *http.Request) (*pageViewer, bool) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, 401, "Unauthorized")
		return nil, false
	}
	if agent, _ := pageGrantCallerIsAgent(r); agent {
		replyError(w, 403, "catalog management requires a human")
		return nil, false
	}
	if projectAgentFrom(r.Context()) != nil || r.Context().Value(artifactRunKey{}) != nil {
		replyError(w, 403, "catalog management requires an original human request")
		return nil, false
	}
	if validateContentHumanCredential(r, h.pageAuthReader(r.Context()), "pages:write") != nil {
		replyError(w, 403, "current human pages:write credential required")
		return nil, false
	}
	role, err := h.liveContentHumanRole(r.Context(), WorkspaceIDFromContext(r.Context()), user.ID)
	if err != nil {
		replyError(w, 404, "page not found")
		return nil, false
	}
	viewer, err := h.loadViewer(r.Context(), WorkspaceIDFromContext(r.Context()), user.ID)
	if err != nil {
		replyInternalError(w, h.logger, "load catalog viewer", err)
		return nil, false
	}
	viewer.Role = role
	return viewer, true
}
