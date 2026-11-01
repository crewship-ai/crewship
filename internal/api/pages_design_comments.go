package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/design"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// Feedback accepts Edit or the canonical Comment grant when that Page grant
// level is installed. Read and Produce never become comment authority. The
// shared Page grant reader supplies issuer validity, membership and revocation.
func (h *PageHandler) designCommentAuthority(r *http.Request, k artifacts.VersionKey) artifacts.VersionAuthorize {
	editAuth, readAuth := h.designAuthority(r, k, true), h.designAuthority(r, k, false)
	var stamp designAuthorityStamp
	return func(ctx context.Context, tx *sql.Tx) (string, error) {
		ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
		revision, err := designAuthorityRevision(ctx, tx, k.WorkspaceID)
		if err != nil {
			return "", err
		}
		if actor, e := editAuth(ctx, tx); e == nil {
			return stamp.admit(actor, revision)
		}
		actor, err := readAuth(ctx, tx)
		if err != nil {
			return "", err
		}
		if ctx.Value(artifactRunKey{}) != nil || validateContentHumanCredential(r.WithContext(ctx), tx, "pages:write") != nil {
			return "", errDesignAccess
		}
		rec, err := h.loadPageForRead(ctx, k.WorkspaceID, "@"+k.PageID)
		if err != nil {
			return "", err
		}
		viewer, err := h.loadViewer(ctx, k.WorkspaceID, UserFromContext(ctx).ID)
		if err != nil {
			return "", err
		}
		grants, err := h.grantsFor(ctx, k.WorkspaceID, rec, viewer)
		if err != nil {
			return "", err
		}
		for _, g := range grants {
			if g.Level == "comment" {
				return stamp.admit(actor, revision)
			}
		}
		return "", errDesignAccess
	}
}
func (h *PageHandler) GetDesignComments(w http.ResponseWriter, r *http.Request) {
	k, r, ok := h.designKey(w, r)
	if !ok {
		return
	}
	out := []artifactProjectComment{}
	err := h.designTx(r, k, false, func(ctx context.Context, tx *sql.Tx, _ string) error {
		_, writeErr := h.designAuthority(r, k, true)(ctx, tx)
		sharedOnly := writeErr != nil
		if sharedOnly {
			manager, e := h.designManagerRead(ctx, tx, k)
			if e != nil {
				return e
			}
			sharedOnly = !manager
		}
		var shared int64
		if sharedOnly {
			if e := tx.QueryRowContext(ctx, `SELECT shared_version FROM artifact_version_heads WHERE workspace_id=? AND page_id=?`, k.WorkspaceID, k.PageID).Scan(&shared); e != nil {
				return e
			}
		}
		rows, e := tx.QueryContext(ctx, `SELECT c.id,c.version,c.path,c.selector,c.quote,c.text,c.author,coalesce(c.task_id,''),c.created_at,coalesce(t.status,'comment') FROM artifact_project_comments c LEFT JOIN artifact_project_comment_tasks t ON t.comment_id=c.id WHERE c.workspace_id=? AND c.page_id=? AND (?=0 OR c.version=?) ORDER BY c.created_at DESC,c.id DESC LIMIT 500`, k.WorkspaceID, k.PageID, sharedOnly, shared)
		if e != nil {
			return e
		}
		for rows.Next() {
			var c artifactProjectComment
			if e = rows.Scan(&c.ID, &c.Version, &c.Path, &c.Selector, &c.Quote, &c.Text, &c.Author, &c.TaskID, &c.CreatedAt, &c.Status); e != nil {
				rows.Close()
				return e
			}
			c.Replies = []artifactProjectReply{}
			c.AuthorKind = artifactProjectAuthorKind(c.Author)
			out = append(out, c)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		slices.Reverse(out) // newest 500, listed oldest first
		for i := range out {
			replies, e := tx.QueryContext(ctx, `SELECT text,version,needs_clarification,author,created_at FROM artifact_project_comment_replies WHERE comment_id=? AND (?=0 OR version=?) ORDER BY created_at,id`, out[i].ID, sharedOnly, shared)
			if e != nil {
				return e
			}
			for replies.Next() {
				var p artifactProjectReply
				if e = replies.Scan(&p.Text, &p.Version, &p.NeedsClarification, &p.Author, &p.CreatedAt); e != nil {
					replies.Close()
					return e
				}
				p.AuthorKind = artifactProjectAuthorKind(p.Author)
				out[i].Replies = append(out[i].Replies, p)
				if !p.NeedsClarification && p.Version > out[i].AnsweredVersion {
					out[i].AnsweredVersion = p.Version
				}
			}
			e = replies.Err()
			replies.Close()
			if e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		designError(w, err)
		return
	}
	writeJSON(w, 200, designCommentsResponse{Comments: out})
}
func (h *PageHandler) CreateDesignComment(w http.ResponseWriter, r *http.Request) {
	k, r, ok := h.designKey(w, r)
	if !ok {
		return
	}
	var p struct {
		Version     int64  `json:"version"`
		Path        string `json:"path"`
		Selector    string `json:"selector"`
		Quote       string `json:"quote"`
		Text        string `json:"text"`
		SendToAgent bool   `json:"send_to_agent"`
	}
	if !designDecode(w, r, &p, 20000) {
		return
	}
	if p.Version < 1 || p.Path != "design.json" || p.Selector == "" || len(p.Selector) > 128 || len(p.Quote) > 2000 || len(p.Text) > 10000 || strings.TrimSpace(p.Text) == "" {
		designError(w, artifacts.ErrVersionInvalid)
		return
	}
	out := artifactProjectComment{ID: generateCUID(), Version: p.Version, Path: p.Path, Selector: p.Selector, Quote: p.Quote, Text: p.Text, Status: "comment", CreatedAt: tsformat.Format(time.Now()), Replies: []artifactProjectReply{}}
	room := h.designRoom(k)
	room.mu.Lock()
	defer room.mu.Unlock()
	err := h.designAuthorizedTx(r, k, h.designCommentAuthority(r, k), func(ctx context.Context, tx *sql.Tx, actor string) error {
		var latest, shared int64
		if e := tx.QueryRowContext(ctx, `SELECT latest_version,shared_version FROM artifact_version_heads WHERE workspace_id=? AND page_id=?`, k.WorkspaceID, k.PageID).Scan(&latest, &shared); e != nil {
			return e
		}
		if _, err := h.designAuthority(r, k, true)(ctx, tx); err != nil {
			latest = shared
		}
		if latest != p.Version {
			return artifacts.ErrVersionConflict
		}
		// Anchors refer to an immutable normal version, never unsaved live state.
		var manifestJSON string
		if e := tx.QueryRowContext(ctx, `SELECT manifest_json FROM artifact_versions WHERE workspace_id=? AND page_id=? AND version=?`, k.WorkspaceID, k.PageID, p.Version).Scan(&manifestJSON); e != nil {
			return e
		}
		var manifest struct {
			Files map[string]struct {
				Hash string `json:"hash"`
			} `json:"files"`
		}
		if json.Unmarshal([]byte(manifestJSON), &manifest) != nil || manifest.Files["design.yjs"].Hash == "" {
			return artifacts.ErrVersionInvalid
		}
		var state []byte
		if e := tx.QueryRowContext(ctx, `SELECT content FROM artifact_version_blobs WHERE workspace_id=? AND page_id=? AND hash=?`, k.WorkspaceID, k.PageID, manifest.Files["design.yjs"].Hash).Scan(&state); e != nil {
			return e
		}
		if p.SendToAgent {
			if _, e := h.designAuthority(r, k, true)(ctx, tx); e != nil {
				return e
			}
		}
		result, e := design.Run(ctx, design.Request{State: state})
		if e != nil {
			return e
		}
		matches := false
		for _, raw := range result.Nodes {
			var n struct {
				ID   string `json:"id"`
				Name string `json:"name"`
				Text string `json:"text"`
			}
			_ = json.Unmarshal(raw, &n)
			if n.ID == p.Selector && (p.Quote == "" || p.Quote == n.Text || p.Quote == n.Name) {
				matches = true
			}
		}
		if !matches {
			return artifacts.ErrVersionConflict
		}
		out.Author = actor
		out.AuthorKind = artifactProjectAuthorKind(actor)
		if p.SendToAgent {
			out.TaskID = generateCUID()
			out.Status = "pending"
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO artifact_project_comments(id,workspace_id,page_id,version,path,selector,quote,text,author,task_id,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, out.ID, k.WorkspaceID, k.PageID, p.Version, p.Path, p.Selector, p.Quote, p.Text, actor, nullIfEmpty(out.TaskID), out.CreatedAt)
		if e != nil {
			return e
		}
		if out.TaskID != "" {
			_, e = tx.ExecContext(ctx, `INSERT INTO artifact_project_comment_tasks(id,comment_id,workspace_id,page_id,base_version,created_at) VALUES(?,?,?,?,?,?)`, out.TaskID, out.ID, k.WorkspaceID, k.PageID, p.Version, out.CreatedAt)
		}
		return e
	})
	if err != nil {
		designError(w, err)
		return
	}
	writeJSON(w, 201, out)
}
func (h *PageHandler) ShareDesign(w http.ResponseWriter, r *http.Request) {
	k, r, ok := h.designKey(w, r)
	if !ok {
		return
	}
	var p struct {
		Version int64 `json:"version"`
	}
	if !designDecode(w, r, &p, 8192) {
		return
	}
	// Sharing is retained by the Page owner. An Edit grant permits collaboration,
	// but never confers control over the immutable shared pin.
	editAuth := h.designAuthority(r, k, true)
	auth := func(ctx context.Context, tx *sql.Tx) (string, error) {
		a, e := editAuth(ctx, tx)
		if e != nil {
			return "", e
		}
		var owner string
		if e = tx.QueryRowContext(ctx, `SELECT owner_user_id FROM pages WHERE id=? AND workspace_id=?`, k.PageID, k.WorkspaceID).Scan(&owner); e != nil {
			return "", e
		}
		if a != owner {
			return "", errDesignAccess
		}
		return a, nil
	}
	room := h.designRoom(k)
	room.mu.Lock()
	defer room.mu.Unlock()
	var latest int64
	err := h.designTx(r, k, true, func(ctx context.Context, tx *sql.Tx, _ string) error {
		return tx.QueryRowContext(ctx, `SELECT latest_version FROM artifact_version_heads WHERE workspace_id=? AND page_id=?`, k.WorkspaceID, k.PageID).Scan(&latest)
	})
	if err == nil {
		err = h.designStore(r.Context()).Share(r.Context(), k, latest, p.Version, auth)
	}
	if err != nil {
		designError(w, err)
		return
	}
	writeJSON(w, 200, designShareResponse{Version: p.Version, SharedVersion: p.Version})
}
func (h *PageHandler) ConnectDesignAgent(w http.ResponseWriter, r *http.Request) {
	k, r, ok := h.designKey(w, r)
	if !ok {
		return
	}
	var p struct {
		ChatID string `json:"chat_id"`
	}
	if !designDecode(w, r, &p, 8192) {
		return
	}
	if p.ChatID == "" {
		designError(w, artifacts.ErrVersionInvalid)
		return
	}
	var agent string
	err := h.designTx(r, k, true, func(ctx context.Context, tx *sql.Tx, actor string) error {
		var owner string
		if e := tx.QueryRowContext(ctx, `SELECT owner_user_id FROM pages WHERE id=? AND workspace_id=?`, k.PageID, k.WorkspaceID).Scan(&owner); e != nil {
			return e
		}
		if owner != actor || ctx.Value(artifactRunKey{}) != nil {
			return errDesignAccess
		}
		if e := tx.QueryRowContext(ctx, `SELECT agent_id FROM chats WHERE id=? AND workspace_id=? AND created_by=? AND visibility='private' AND agent_id IS NOT NULL`, p.ChatID, k.WorkspaceID, actor).Scan(&agent); e != nil {
			return errDesignAccess
		}
		// Historical origin is one exact owner's chosen private chat, never bearer
		// authority. A fresh message intent and run principal are still required.
		_, e := tx.ExecContext(ctx, `INSERT INTO artifact_project_origins(page_id,workspace_id,human_user_id,chat_id,agent_id,message_id,created_at) VALUES(?,?,?,?,?,'',?) ON CONFLICT(page_id) DO UPDATE SET chat_id=excluded.chat_id,agent_id=excluded.agent_id WHERE artifact_project_origins.human_user_id=excluded.human_user_id`, k.PageID, k.WorkspaceID, actor, p.ChatID, agent, tsformat.Format(time.Now()))
		return e
	})
	if err != nil {
		designError(w, err)
		return
	}
	writeJSON(w, 200, designAgentResponse{Origin: designOriginResponse{ChatID: p.ChatID, AgentID: agent}})
}
