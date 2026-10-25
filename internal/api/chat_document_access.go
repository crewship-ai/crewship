package api

import (
	"context"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/mooring"
)

type chatDocumentAccess struct {
	PageID        string                       `json:"page_id"`
	Title         string                       `json:"title"`
	DocumentType  string                       `json:"document_type"`
	ApplicationID string                       `json:"application_id"`
	Grant         string                       `json:"document_grant"`
	PolicyAllows  bool                         `json:"policy_allows"`
	TaskStatus    string                       `json:"task_status"`
	Tasks         []collaborationAgentTaskView `json:"tasks"`
	Provenance    string                       `json:"provenance"`
}

type chatDocumentAccessResponse struct {
	Documents []chatDocumentAccess `json:"documents"`
}

// The outer wrapper precedes authentication. Mooring may be disabled while the
// ordinary Page grant list remains useful; no task is then reported as active.
func (r *Router) chatDocumentAccessIngress(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if m := r.mooringHTTP; m != nil {
			m.fence.Wrap(next).ServeHTTP(w, req)
			return
		}
		next.ServeHTTP(w, req)
	})
}

// Document grants are metadata from the existing Page ACL, not a new ACL or
// task capability. Chat visibility alone never exposes a hidden Page.
func (r *Router) chatDocumentAccess(w http.ResponseWriter, req *http.Request) {
	artifactCommentHeaders(w)
	user := UserFromContext(req.Context())
	source := auth.OriginalCredentialFromContext(req.Context())
	if user == nil || source == nil || user.OriginalCredential == nil || *source != *user.OriginalCredential || source.UserID != user.ID || AuthKindFromContext(req.Context()) != source.Kind ||
		(source.Kind == AuthKindSession && (user.SessionID != source.ID || user.TokenID != "")) ||
		(source.Kind == AuthKindCLIToken && (user.TokenID != source.ID || user.SessionID != "")) ||
		(source.Kind != AuthKindSession && source.Kind != AuthKindCLIToken) ||
		InternalTokenWorkspaceFromContext(req.Context()) != "" || InternalTokenCrewFromContext(req.Context()) != "" || projectAgentFrom(req.Context()) != nil || req.Context().Value(artifactRunKey{}) != nil {
		replyError(w, http.StatusUnauthorized, "current human authentication required")
		return
	}
	ctx, cancel := context.WithTimeout(req.Context(), 3*time.Second)
	defer cancel()
	controller := http.NewResponseController(w)
	deadline, _ := ctx.Deadline()
	if err := controller.SetWriteDeadline(deadline); err != nil {
		replyError(w, http.StatusServiceUnavailable, "bounded response unavailable")
		return
	}
	defer func() { _ = controller.SetWriteDeadline(time.Time{}) }()
	manager := r.mooringHTTP
	if manager != nil {
		stamp, err := manager.fence.capture(ctx)
		if err != nil {
			replyError(w, http.StatusServiceUnavailable, "document access unavailable")
			return
		}
		release, err := manager.fence.acquire(ctx, stamp)
		if err != nil {
			replyError(w, http.StatusServiceUnavailable, "document access unavailable")
			return
		}
		defer release()
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		replyError(w, http.StatusServiceUnavailable, "document access unavailable")
		return
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE mooring_session_writer_fence SET seq=seq WHERE id=1`); err != nil {
		replyError(w, http.StatusServiceUnavailable, "document access unavailable")
		return
	}
	if _, err = validateOriginalHumanCredential(ctx, tx, *source, "pages:read"); err != nil {
		replyError(w, http.StatusForbidden, "current pages:read access required")
		return
	}
	workspace := WorkspaceIDFromContext(ctx)
	receipt, err := chataudience.CaptureTrustedQuery(ctx, tx, req.PathValue("chatId"), user.ID, nil)
	if err != nil || receipt == nil || receipt.WorkspaceID != workspace {
		replyError(w, http.StatusNotFound, "chat not found")
		return
	}
	ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
	role, err := r.pages.liveContentHumanRole(ctx, workspace, user.ID)
	if err != nil {
		replyError(w, http.StatusNotFound, "chat not found")
		return
	}
	ctx = context.WithValue(ctx, ctxRole, role)
	viewer, err := r.pages.loadViewer(ctx, workspace, user.ID)
	if err != nil {
		replyError(w, http.StatusServiceUnavailable, "document access unavailable")
		return
	}
	index, err := r.pages.loadPageIndex(ctx, workspace, viewer, pagesInWorkspace(workspace))
	if err != nil {
		replyError(w, http.StatusServiceUnavailable, "document access unavailable")
		return
	}
	grants, err := r.pages.loadPageGrantRecordsIn(ctx, workspace, "")
	if err != nil {
		replyError(w, http.StatusServiceUnavailable, "document access unavailable")
		return
	}
	var crew string
	if err = tx.QueryRowContext(ctx, `SELECT crew_id FROM agents WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, receipt.AgentID, workspace).Scan(&crew); err != nil {
		replyError(w, http.StatusNotFound, "chat not found")
		return
	}
	policy := r.pages.artifactAgentPolicyAllows(ctx, workspace, crew)
	documents := make([]chatDocumentAccess, 0)
	for _, row := range index.rows {
		rec := row.rec
		if rec.PrivateAudience || !(rec.HasCollaboration || rec.HasWorkbook || rec.HasMooring || rec.HasDocument) {
			continue
		}
		level := ""
		for _, grant := range liveGrantsIn(grants[rec.ID], func(g pageGrantRecord) bool {
			return g.SubjectType == "agent" && g.SubjectID == receipt.AgentID
		}) {
			if grant.Level == pageGrantWrite {
				level = "write"
			} else if grant.Level == pageGrantRead && level == "" {
				level = "read"
			}
		}
		if level == "" {
			continue
		}
		key := mooring.SessionKey{WorkspaceID: workspace, ArtifactID: rec.ID, ApplicationID: "wordcraft"}
		// Native content read and the actual application identity must hold even
		// when the runtime/task source is unavailable.
		if rec.HasCollaboration {
			if _, err = r.pages.authorizeCollaborationHumanPage(ctx, tx, key, "read"); err != nil {
				continue
			}
		}
		doc := chatDocumentAccess{PageID: rec.ID, Title: rec.Name, Grant: level, PolicyAllows: policy, TaskStatus: "unavailable", Tasks: []collaborationAgentTaskView{}, Provenance: "unknown", DocumentType: "document"}
		if rec.HasCollaboration {
			doc.ApplicationID = "wordcraft"
		}
		if rec.HasWorkbook || rec.HasMooring {
			doc.DocumentType = "spreadsheet"
		}
		if rec.HasCollaboration && manager != nil && manager.agentMandates != nil {
			if tasks, taskErr := manager.agentMandates.currentTaskViews(ctx, tx, key, receipt.ChatID); taskErr == nil {
				doc.Tasks = tasks
				doc.TaskStatus = "no_active_task"
				if len(tasks) > 0 {
					doc.TaskStatus = "active"
				}
			}
		}
		documents = append(documents, doc)
	}
	if ctx.Err() != nil || tx.Commit() != nil {
		replyError(w, http.StatusServiceUnavailable, "document access unavailable")
		return
	}
	writeJSON(w, http.StatusOK, chatDocumentAccessResponse{Documents: documents})
	// Flush while the original ingress lease is held. Context cancellation alone
	// does not bound ResponseWriter.Write or the final net/http buffered send.
	_ = controller.Flush()
}
