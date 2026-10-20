package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"regexp"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/documents"
)

type chatArtifactPreviewResponse struct {
	Chat      chatArtifactPreviewChat   `json:"chat"`
	Target    chatArtifactPreviewTarget `json:"target"`
	CanUpdate bool                      `json:"can_update"`
	Reason    string                    `json:"reason"`
}

type chatArtifactPreviewChat struct {
	ID         string `json:"id"`
	Visibility string `json:"visibility"`
	Audience   string `json:"audience"`
}

type chatArtifactPreviewTarget struct {
	PageID   string                      `json:"page_id"`
	Type     string                      `json:"type"`
	Audience string                      `json:"audience"`
	Expected chatArtifactPreviewExpected `json:"expected"`
}

type chatArtifactPreviewExpected struct {
	Schema     int   `json:"schema"`
	Rev        int64 `json:"rev"`
	Generation int64 `json:"generation"`
}

// PreviewChatDocumentContext reads a narrow preparation snapshot. It never
// retains a send intent, seals a chat, or grants authority to edit the target.
func (h *PageHandler) PreviewChatDocumentContext(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	user := UserFromContext(r.Context())
	agent, _ := pageGrantCallerIsAgent(r)
	if user == nil || agent || projectAgentFrom(r.Context()) != nil || r.Context().Value(artifactRunKey{}) != nil || InternalTokenWorkspaceFromContext(r.Context()) != "" || InternalTokenCrewFromContext(r.Context()) != "" {
		replyError(w, http.StatusUnauthorized, "human authentication required")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		replyInternalError(w, h.logger, "read Document context", err)
		return
	}
	defer tx.Rollback()
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	r = r.WithContext(ctx)
	if err := validateContentHumanCredential(r, tx, "pages:read"); err != nil {
		if errors.Is(err, errContentCredentialScope) {
			replyError(w, http.StatusForbidden, "current pages:read scope required")
		} else {
			replyError(w, http.StatusUnauthorized, "current human credential required")
		}
		return
	}
	workspace, chat := WorkspaceIDFromContext(ctx), r.PathValue("chatId")
	notFound := func() { replyError(w, http.StatusNotFound, "Document context not found") }
	receipt, err := chataudience.CaptureTrustedQuery(ctx, tx, chat, user.ID, nil)
	if err != nil {
		replyInternalError(w, h.logger, "authorize Document context chat", err)
		return
	}
	if receipt == nil || receipt.WorkspaceID != workspace {
		notFound()
		return
	}
	var protected sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT private_artifact_author_user_id FROM chats WHERE id=? AND workspace_id=? AND created_by=? AND visibility='private'`, chat, workspace, user.ID).Scan(&protected)
	if errors.Is(err, sql.ErrNoRows) {
		notFound()
		return
	} else if err != nil {
		replyInternalError(w, h.logger, "read Document context audience", err)
		return
	}
	sealed := protected.Valid && protected.String != ""
	if !sealed {
		// Bearer links add readers outside the workspace audience below. Keep
		// this preparation flow unavailable until those links are revoked;
		// do not guess whether an expired or currently unusable link is safe.
		var shared bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM chat_read_shares WHERE workspace_id=? AND chat_id=? AND revoked_at IS NULL)`, workspace, chat).Scan(&shared); err != nil {
			replyInternalError(w, h.logger, "read Document context sharing", err)
			return
		}
		if shared {
			notFound()
			return
		}
	}
	if h.documentCall == nil {
		replyError(w, http.StatusServiceUnavailable, "Documents is unavailable")
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query["page_id"]) != 1 || !documentPreviewPageID.MatchString(query.Get("page_id")) {
		replyError(w, http.StatusBadRequest, "one immutable page_id is required")
		return
	}
	page := query.Get("page_id")
	intent := documents.IterationIntent{SubmissionID: "preview", PageID: page}
	var slug string
	err = tx.QueryRowContext(ctx, `SELECT p.slug,d.schema_version,d.head_rev,d.generation FROM documents d JOIN pages p ON p.id=d.page_id AND p.workspace_id=d.workspace_id WHERE d.workspace_id=? AND d.page_id=?`, workspace, page).Scan(&slug, &intent.Schema, &intent.Rev, &intent.Generation)
	if errors.Is(err, sql.ErrNoRows) {
		notFound()
		return
	} else if err != nil {
		replyInternalError(w, h.logger, "read Document context revision", err)
		return
	}
	if intent.Validate() != nil {
		notFound()
		return
	}
	// A preparation read distinguishes missing targets from persistence errors:
	// a transient failure must not tell the client its source was revoked.
	rec, err := h.loadPage(ctx, workspace, slug)
	if errors.Is(err, sql.ErrNoRows) {
		notFound()
		return
	} else if err != nil {
		replyInternalError(w, h.logger, "load Document context target", err)
		return
	}
	if rec.ID != page || rec.OwnerUserID != user.ID || !rec.PrivateAudience || rec.FolderID != "" || rec.OwnerCrewID != "" || rec.HasProject || rec.HasApplication {
		notFound()
		return
	}
	var panels int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM page_panels WHERE page_id=?`, page).Scan(&panels); err != nil {
		replyInternalError(w, h.logger, "read Document context panels", err)
		return
	}
	if panels != 0 {
		notFound()
		return
	}
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "finish Document context preview", err)
		return
	}
	audience := "author_and_workspace_operators"
	if sealed {
		audience = "author_and_workspace_owners"
	}
	writeJSON(w, http.StatusOK, chatArtifactPreviewResponse{
		Chat: chatArtifactPreviewChat{ID: chat, Visibility: "private", Audience: audience},
		Target: chatArtifactPreviewTarget{PageID: page, Type: "document", Audience: "author_and_workspace_owners",
			Expected: chatArtifactPreviewExpected{Schema: intent.Schema, Rev: intent.Rev, Generation: intent.Generation}},
		CanUpdate: false, Reason: "targeted_iteration_unavailable",
	})
}

var documentPreviewPageID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
