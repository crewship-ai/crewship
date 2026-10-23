package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/appengine"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

type gridcraftShare struct {
	Editor  string `json:"editor"`
	Publish bool   `json:"publish"`
}

type gridcraftShared struct {
	Page            string `json:"page"`
	Editor          string `json:"editor"`
	EditorUserID    string `json:"editor_user_id"`
	Published       bool   `json:"published"`
	PublishedRev    int64  `json:"published_rev,omitempty"`
	RevokedMandates int64  `json:"revoked_mandates"`
}

// share names a workspace member as an editor of the owner's workbook.
//
// The editor is an ordinary page_grants row (user, write) issued by the owner,
// so `page grants` lists it and `page revoke` withdraws it. A private workbook
// first needs explicit publication: it ends the private-output audience, revokes
// every live agent mandate and is audited, all in the same transaction as the
// grant. Publication is one-way; revoking the last editor does not make the
// Page private again. Only the current head is shared: earlier revisions,
// receipts and operation status of other authorities stay unavailable.
func (h *gridcraftHTTP) share(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	user := UserFromContext(r.Context())
	if agent, _ := pageGrantCallerIsAgent(r); user == nil || agent || projectAgentFrom(r.Context()) != nil {
		gridcraftError(w, appengine.ErrForbidden)
		return
	}
	k, err := h.humanKey(r, false)
	if err != nil {
		gridcraftError(w, err)
		return
	}
	var req gridcraftShare
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	raw, err := io.ReadAll(r.Body)
	if err != nil || appengine.DecodeStrict(raw, &req) != nil || strings.TrimSpace(req.Editor) == "" || len(req.Editor) > 320 {
		gridcraftError(w, appengine.ErrInvalid)
		return
	}
	tx, err := h.pages.db.BeginTx(r.Context(), nil)
	if err != nil {
		gridcraftError(w, err)
		return
	}
	defer tx.Rollback()
	// Serialize with page grant mutations and with workbook writers.
	if _, err = tx.ExecContext(r.Context(), `UPDATE workspaces SET updated_at=updated_at WHERE id=?`, k.WorkspaceID); err != nil {
		gridcraftError(w, err)
		return
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE gridcraft_writer_fence SET seq=seq WHERE id=1`); err != nil {
		gridcraftError(w, err)
		return
	}
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	r = r.WithContext(ctx)
	if validateContentHumanCredential(r, tx, "pages:write") != nil {
		gridcraftError(w, appengine.ErrForbidden)
		return
	}
	rec, access, err := h.pages.workbookHumanAccess(ctx, k.WorkspaceID, k.PageID, user.ID, true)
	if err != nil {
		gridcraftError(w, err)
		return
	}
	if access != workbookOwner {
		replyError(w, http.StatusForbidden, "only the workbook owner may name its editors")
		return
	}
	var rev, generation int64
	if err = tx.QueryRowContext(ctx, `SELECT head_rev,generation FROM gridcraft_heads WHERE workspace_id=? AND page_id=?`, k.WorkspaceID, k.PageID).Scan(&rev, &generation); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusConflict, "import a workbook before sharing it")
			return
		}
		gridcraftError(w, err)
		return
	}
	editorID, editorRef, ok := h.pages.resolveGrantSubject(w, r, k.WorkspaceID, pageSubjectUser, strings.TrimSpace(req.Editor))
	if !ok {
		return
	}
	if editorID == user.ID {
		replyError(w, http.StatusBadRequest, "the owner already edits this workbook")
		return
	}
	if _, err = h.pages.liveContentHumanRole(ctx, k.WorkspaceID, editorID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusBadRequest, "an editor must be an active, unrestricted member of this workspace")
			return
		}
		gridcraftError(w, err)
		return
	}
	out := gridcraftShared{Page: rec.Slug, Editor: editorRef, EditorUserID: editorID}
	var notifyPublication func()
	if rec.PrivateAudience {
		if !req.Publish {
			replyError(w, http.StatusConflict, "publication_required: sharing ends this workbook's private audience; the editor sees the full current workbook (every sheet and its revision number, not earlier revisions) and the ordinary Page ACL, which admins also manage, decides who sees its name. Repeat with publish=true")
			return
		}
		stamp := tsformat.Format(time.Now())
		if err = execOneRow(ctx, tx, `UPDATE pages SET private_audience=0, updated_at=? WHERE id=? AND workspace_id=? AND private_audience=1`, stamp, rec.ID, k.WorkspaceID); err != nil {
			gridcraftError(w, err)
			return
		}
		revoked, err := tx.ExecContext(ctx, `UPDATE gridcraft_intents SET revoked_at=? WHERE workspace_id=? AND page_id=? AND revoked_at IS NULL`, stamp, k.WorkspaceID, k.PageID)
		if err != nil {
			gridcraftError(w, err)
			return
		}
		if out.RevokedMandates, err = revoked.RowsAffected(); err != nil {
			gridcraftError(w, err)
			return
		}
		metadata, _ := json.Marshal(map[string]any{"owner_user_id": rec.OwnerUserID, "head_rev": rev, "generation": generation, "revoked_mandates": out.RevokedMandates})
		if err = execOneRow(ctx, tx, `INSERT INTO audit_logs(id,workspace_id,user_id,action,entity_type,entity_id,metadata,created_at) VALUES(?,?,?,?,?,?,?,?)`, generateCUID(), k.WorkspaceID, user.ID, "artifact.private_output_published", "page", rec.ID, string(metadata), stamp); err != nil {
			gridcraftError(w, err)
			return
		}
		notifyPublication, err = appendAuthorizationAudit(ctx, tx, h.pages.journal, journal.Entry{
			WorkspaceID: k.WorkspaceID, Type: "artifact.private_output_published", ActorType: journal.ActorUser, ActorID: user.ID,
			Summary: "Workbook published", Payload: map[string]any{"page_id": rec.ID, "owner_user_id": rec.OwnerUserID, "head_rev": rev, "generation": generation, "revoked_mandates": out.RevokedMandates},
		})
		if err != nil {
			gridcraftError(w, err)
			return
		}
		rec.PrivateAudience = false
		out.Published, out.PublishedRev = true, rev
	}
	before, err := readPageGrantSubject(ctx, tx, rec.ID, "", "")
	if err != nil {
		gridcraftError(w, err)
		return
	}
	now := h.pages.evaluator().Now().UTC().Format(time.RFC3339)
	if err = execOneRow(ctx, tx, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,panel_ids,granted_by_user_id,granted_at) VALUES(?,?,?,?,NULL,?,?)
ON CONFLICT(page_id,subject_type,subject_id,level) DO UPDATE SET panel_ids=NULL,granted_by_user_id=excluded.granted_by_user_id,granted_at=excluded.granted_at`, rec.ID, pageSubjectUser, editorID, pageGrantWrite, user.ID, now); err != nil {
		gridcraftError(w, err)
		return
	}
	after := pageGrantSnapshot{PageID: rec.ID, SubjectType: pageSubjectUser, SubjectID: editorID, Level: pageGrantWrite, GrantedBy: user.ID, GrantedAt: now}
	var previous *pageGrantSnapshot
	expected := make([]pageGrantSnapshot, 0, len(before)+1)
	for _, g := range before {
		if g.SubjectType == pageSubjectUser && g.SubjectID == editorID && g.Level == pageGrantWrite {
			old := g
			previous = &old
			continue
		}
		expected = append(expected, g)
	}
	expected = append(expected, after)
	entry := pageGrantChangeEntry(journal.EntryPageGrantAdded, k.WorkspaceID, user, rec, pageSubjectUser, editorID, editorRef, pageGrantWrite, nil)
	if err = h.pages.commitPageGrantMutation(ctx, tx, rec, user.ID, "", "", expected, []pageGrantChange{{entry, previous, &after}}); err != nil {
		if errors.Is(err, errPageGrantIssuerNotCurrent) {
			gridcraftError(w, appengine.ErrForbidden)
			return
		}
		gridcraftError(w, err)
		return
	}
	if notifyPublication != nil {
		notifyPublication()
	}
	broadcastWorkspaceEvent(h.pages.hub, k.WorkspaceID, "page.updated", map[string]any{})
	writeJSON(w, http.StatusOK, out)
}

func execOneRow(ctx context.Context, tx *sql.Tx, query string, args ...any) error {
	result, err := tx.ExecContext(ctx, query, args...)
	return requirePageGrantRows(result, err, 1)
}
