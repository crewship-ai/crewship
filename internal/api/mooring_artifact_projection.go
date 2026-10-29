package api

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// Historical edit DATA is never a grant, live task, creation receipt or content.
type nativeArtifactEdit struct {
	PageID        string `json:"page_id"`
	Title         string `json:"title"`
	ApplicationID string `json:"application_id"`
	Provenance    string `json:"provenance"`
	ChatID        string `json:"chat_id"`
	MessageID     string `json:"message_id"`
	AgentSlug     string `json:"agent_slug"`
	LastRevision  string `json:"last_revision"`
	EditedAt      string `json:"edited_at"`
}
type nativeProvenanceResponse struct {
	Artifacts        []nativeArtifactEdit `json:"artifacts"`
	ArtifactsPartial bool                 `json:"artifacts_partial"`
}

type nativeMetadataRead struct {
	ctx        context.Context
	tx         *sql.Tx
	controller *http.ResponseController
	cleanups   []func()
}

func (read *nativeMetadataRead) close() {
	for i := len(read.cleanups) - 1; i >= 0; i-- {
		read.cleanups[i]()
	}
}
func (read *nativeMetadataRead) finish(w http.ResponseWriter, value any) {
	if read.ctx.Err() != nil {
		replyError(w, 503, "document metadata unavailable")
		return
	}
	writeJSON(w, 200, value)
	// This read-only writer transaction stays open through the physical flush,
	// excluding concurrent SQL Page/chat/credential revocation until delivery.
	// close rolls back its no-op writer fence; metadata writes/COMMIT are absent.
	_ = read.controller.Flush()
}

// Shared by current grants/tasks and historical provenance. Each projection
// still separately checks its current Page and original source chat audience.
func (r *Router) beginNativeMetadataRead(w http.ResponseWriter, req *http.Request) *nativeMetadataRead {
	artifactCommentHeaders(w)
	user := UserFromContext(req.Context())
	source := auth.OriginalCredentialFromContext(req.Context())
	if user == nil || source == nil || user.OriginalCredential == nil || *source != *user.OriginalCredential || source.UserID != user.ID || AuthKindFromContext(req.Context()) != source.Kind ||
		(source.Kind == AuthKindSession && (user.SessionID != source.ID || user.TokenID != "")) ||
		(source.Kind == AuthKindCLIToken && (user.TokenID != source.ID || user.SessionID != "")) ||
		(source.Kind != AuthKindSession && source.Kind != AuthKindCLIToken) ||
		InternalTokenWorkspaceFromContext(req.Context()) != "" || InternalTokenCrewFromContext(req.Context()) != "" || projectAgentFrom(req.Context()) != nil || req.Context().Value(artifactRunKey{}) != nil {
		replyError(w, 401, "current human authentication required")
		return nil
	}
	expiry, err := source.Expiry()
	if err != nil || (!expiry.IsZero() && !time.Now().Before(expiry)) {
		replyError(w, 403, "current human authentication required")
		return nil
	}
	horizon := time.Now().Add(3 * time.Second)
	if !expiry.IsZero() && expiry.Before(horizon) {
		horizon = expiry
	}
	ctx, cancel := context.WithDeadline(req.Context(), horizon)
	read := &nativeMetadataRead{ctx: ctx, controller: http.NewResponseController(w), cleanups: []func(){cancel}}
	ready := false
	defer func() {
		if !ready {
			read.close()
		}
	}()
	deadline, _ := ctx.Deadline()
	if read.controller.SetWriteDeadline(deadline) != nil {
		replyError(w, 503, "bounded response unavailable")
		return nil
	}
	read.cleanups = append(read.cleanups, func() { _ = read.controller.SetWriteDeadline(time.Time{}) })
	// Retain the same read bound through net/http unread-body cleanup, just
	// like native session admission. A GET body cannot stall header flush.
	if read.controller.SetReadDeadline(deadline) != nil || !collaborationSessionCanFlush(w) {
		replyError(w, 503, "bounded request unavailable")
		return nil
	}
	if manager := r.mooringHTTP; manager != nil {
		stamp, err := manager.fence.capture(ctx)
		if err != nil {
			replyError(w, 503, "document metadata unavailable")
			return nil
		}
		release, err := manager.fence.acquire(ctx, stamp)
		if err != nil {
			replyError(w, 503, "document metadata unavailable")
			return nil
		}
		read.cleanups = append(read.cleanups, release)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		replyError(w, 503, "document metadata unavailable")
		return nil
	}
	read.tx = tx
	read.cleanups = append(read.cleanups, func() { _ = tx.Rollback() })
	if _, err = tx.ExecContext(ctx, `UPDATE mooring_session_writer_fence SET seq=seq WHERE id=1`); err != nil {
		replyError(w, 503, "document metadata unavailable")
		return nil
	}
	currentExpiry, err := validateOriginalHumanCredential(ctx, tx, *source, "pages:read")
	if err != nil {
		replyError(w, 403, "current pages:read access required")
		return nil
	}
	// SQL may shorten the original accepted credential horizon. Keep both
	// physical I/O and all subsequent reads inside that current expiry while
	// retaining the same writer and original ingress leases through flush.
	if !currentExpiry.IsZero() && currentExpiry.Before(deadline) {
		if !time.Now().Before(currentExpiry) {
			replyError(w, 403, "current pages:read access required")
			return nil
		}
		var cancelCurrent context.CancelFunc
		ctx, cancelCurrent = context.WithDeadline(ctx, currentExpiry)
		read.cleanups = append(read.cleanups, cancelCurrent)
		if read.controller.SetReadDeadline(currentExpiry) != nil || read.controller.SetWriteDeadline(currentExpiry) != nil {
			replyError(w, 503, "bounded response unavailable")
			return nil
		}
	}
	read.ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
	role, err := r.pages.liveContentHumanRole(read.ctx, WorkspaceIDFromContext(ctx), user.ID)
	if err != nil {
		replyError(w, 403, "current document access required")
		return nil
	}
	read.ctx = context.WithValue(read.ctx, ctxRole, role)
	ready = true
	return read
}

// Bound both SQL output and independent per-chat checks. Select recent saved
// records, then collapse to the latest row per Page/chat. This is a partial
// recent projection, always marked partial. The marker is constant so hidden
// rows cannot leak through a truncation/count side channel. Never a history browser.
const nativeEditCandidateLimit = 64

func (r *Router) projectNativeEdits(ctx context.Context, tx *sql.Tx, workspace, user, chat, page string) ([]nativeArtifactEdit, bool, error) {
	query := `SELECT page_id,chat_id,message_id,agent_id,last_revision,created_at FROM mooring_collab_artifact_edits WHERE workspace_id=? AND actor_kind='agent'`
	args := []any{workspace}
	if chat != "" {
		query += ` AND chat_id=?`
		args = append(args, chat)
	}
	if page != "" {
		query += ` AND page_id=?`
		args = append(args, page)
	}
	query += ` ORDER BY created_at DESC,length(last_revision) DESC,last_revision DESC LIMIT 65`
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, err
	}
	type candidate struct{ page, chat, message, agent, revision, at string }
	candidates := []candidate{}
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.page, &c.chat, &c.message, &c.agent, &c.revision, &c.at); err != nil {
			_ = rows.Close()
			return nil, false, err
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, false, err
	}
	more := len(candidates) > nativeEditCandidateLimit
	if more {
		candidates = candidates[:nativeEditCandidateLimit]
	}
	result := make([]nativeArtifactEdit, 0)
	type chatAccess struct {
		allowed     bool
		agent, slug string
	}
	chats := map[string]chatAccess{}
	seen := map[[2]string]bool{}
	role, err := r.pages.liveContentHumanRole(ctx, workspace, user)
	if err != nil {
		return nil, false, err
	}
	ctx = context.WithValue(ctx, ctxRole, role)
	for _, c := range candidates {
		if !mooring.ValidWireIdentifier(c.page) || !mooring.ValidWireIdentifier(c.chat) || !mooring.ValidWireIdentifier(c.message) {
			continue
		}
		key := [2]string{c.page, c.chat}
		if seen[key] {
			continue
		}
		seen[key] = true
		access, ok := chats[c.chat]
		if !ok {
			receipt, e := chataudience.CaptureTrustedQuery(ctx, tx, c.chat, user, nil)
			if e != nil {
				return nil, false, e
			}
			if receipt != nil && receipt.WorkspaceID == workspace {
				access.agent = receipt.AgentID
				e = tx.QueryRowContext(ctx, `SELECT slug FROM agents WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, receipt.AgentID, workspace).Scan(&access.slug)
				if e != nil && e != sql.ErrNoRows {
					return nil, false, e
				}
				access.allowed = e == nil
			}
			chats[c.chat] = access
		}
		if !access.allowed || access.agent != c.agent {
			continue
		}
		proof, e := r.pages.authorizeCollaborationHumanPage(ctx, tx, mooring.SessionKey{WorkspaceID: workspace, ArtifactID: c.page, ApplicationID: "wordcraft"}, "read")
		if e != nil || proof.record == nil || proof.record.PrivateAudience {
			continue
		}
		result = append(result, nativeArtifactEdit{PageID: c.page, Title: proof.record.Name, ApplicationID: "wordcraft", Provenance: "agent_edited", ChatID: c.chat, MessageID: c.message, AgentSlug: access.slug, LastRevision: c.revision, EditedAt: c.at})
	}
	return result, true, nil
}

func (r *Router) nativeDocumentProvenance(w http.ResponseWriter, req *http.Request) {
	read := r.beginNativeMetadataRead(w, req)
	if read == nil {
		return
	}
	defer read.close()
	ctx, tx := read.ctx, read.tx
	workspace := WorkspaceIDFromContext(ctx)
	user := UserFromContext(ctx)
	rec, err := r.pages.loadPageForRead(ctx, workspace, req.PathValue("slug"))
	if err != nil {
		replyError(w, 404, "document not found")
		return
	}
	proof, err := r.pages.authorizeCollaborationHumanPage(ctx, tx, mooring.SessionKey{WorkspaceID: workspace, ArtifactID: rec.ID, ApplicationID: "wordcraft"}, "read")
	if err != nil || proof.record == nil || proof.record.PrivateAudience {
		replyError(w, 404, "document not found")
		return
	}
	edits, more, err := r.projectNativeEdits(ctx, tx, workspace, user.ID, "", rec.ID)
	if err != nil {
		replyError(w, 503, "document metadata unavailable")
		return
	}
	read.finish(w, nativeProvenanceResponse{Artifacts: edits, ArtifactsPartial: more})
}
