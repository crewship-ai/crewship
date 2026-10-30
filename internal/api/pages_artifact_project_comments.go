package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"golang.org/x/net/html"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

type artifactProjectReply struct {
	Text               string `json:"text"`
	Version            int64  `json:"version"`
	NeedsClarification bool   `json:"needs_clarification,omitempty"`
	Author             string `json:"author"`
	AuthorKind         string `json:"author_kind,omitempty"`
	CreatedAt          string `json:"created_at"`
}
type artifactProjectComment struct {
	DeliveryRetryable bool                   `json:"delivery_retryable"`
	AnsweredVersion   int64                  `json:"answered_version,omitempty"`
	Status            string                 `json:"status"`
	ID                string                 `json:"id"`
	Version           int64                  `json:"version"`
	Path              string                 `json:"path"`
	Selector          string                 `json:"selector"`
	Quote             string                 `json:"quote"`
	Text              string                 `json:"text"`
	Author            string                 `json:"author"`
	AuthorKind        string                 `json:"author_kind,omitempty"`
	TaskID            string                 `json:"task_id,omitempty"`
	CreatedAt         string                 `json:"created_at"`
	Replies           []artifactProjectReply `json:"replies"`
}

// Attribution comes from the authenticated actor, never from request fields.
// Erased identities have no remaining human/agent attribution to display.
func artifactProjectAuthorKind(actor string) string {
	if actor == "" || actor == "erased" {
		return ""
	}
	if strings.HasPrefix(actor, "agent:") {
		return "agent"
	}
	return "human"
}

// The first pilot accepts stable element IDs. A positional or ambiguous anchor
// requires clarification instead of guessing at a different element.
func artifactProjectAnchor(content []byte, selector, quote string) bool {
	if !strings.HasPrefix(selector, "#") || len(selector) < 2 || strings.ContainsAny(selector[1:], " \\>+~[:.,") {
		return false
	}
	root, err := html.Parse(strings.NewReader(string(content)))
	if err != nil {
		return false
	}
	var selected *html.Node
	count := 0
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n.Type == html.ElementNode {
			for _, a := range n.Attr {
				if a.Key == "id" && a.Val == selector[1:] {
					selected = n
					count++
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(root)
	if count != 1 {
		return false
	}
	var text strings.Builder
	var collect func(*html.Node)
	collect = func(n *html.Node) {
		if n.Type == html.TextNode {
			text.WriteString(n.Data)
		}
		if n.Type == html.ElementNode && (n.Data == "script" || n.Data == "style") {
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			collect(c)
		}
	}
	collect(selected)
	normalized := strings.Join(strings.Fields(text.String()), " ")
	want := strings.Join(strings.Fields(quote), " ")
	if want == "" {
		return false
	}
	if normalized == want {
		return true
	}
	// The trusted browser quotes 500 Unicode codepoints after normalization.
	// Normalize the canonical prefix again when its last codepoint is a space.
	// Stop at that boundary rather than allocating runes for the entire file.
	prefixRunes := 0
	for offset := range normalized {
		if prefixRunes == 500 {
			return want == strings.TrimSpace(normalized[:offset])
		}
		prefixRunes++
	}
	return false
}
func (h *PageHandler) artifactProjectCommentTx(r *http.Request, k artifacts.VersionKey, write bool, fn func(context.Context, *sql.Tx, string) error) error {
	authority := h.artifactProjectAuthority(r, k, false, false)
	if write {
		authority = h.artifactProjectOwnerAuthority(r, k)
	}
	return h.artifactProjectCommentTxAuthorized(r, k, authority, fn)
}

func (h *PageHandler) artifactProjectCommentTxAuthorized(r *http.Request, k artifacts.VersionKey, authorize artifacts.VersionAuthorize, fn func(context.Context, *sql.Tx, string) error) error {
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(r.Context(), `UPDATE document_writer_fence SET seq=seq WHERE id=1`); err != nil {
		return err
	}
	actor, err := authorize(r.Context(), tx)
	if err != nil {
		return err
	}
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	if err = fn(ctx, tx, actor); err != nil {
		return err
	}
	finalActor, err := authorize(ctx, tx)
	if err != nil {
		return err
	}
	if actor != finalActor {
		return errArtifactProjectAccess
	}
	return tx.Commit()
}

// Retry eligibility is delivery metadata, never an authorization grant. One
// Page-scoped origin check and one batched task query serve every comment. The
// committing chat/run guards remain authoritative when the human clicks Retry.
func (h *PageHandler) artifactProjectRetryableComments(r *http.Request, ctx context.Context, tx *sql.Tx, k artifacts.VersionKey, selectedVersion int64) (map[string]bool, error) {
	result := make(map[string]bool)
	user := UserFromContext(ctx)
	if user == nil || validateContentHumanCredential(r.WithContext(ctx), tx, "pages:write") != nil {
		return result, nil
	}
	if _, err := h.artifactProjectAuthority(r.WithContext(ctx), k, true, false)(ctx, tx); err != nil {
		return result, nil
	}
	var chat, agent, crew string
	err := tx.QueryRowContext(ctx, `SELECT o.chat_id,o.agent_id,a.crew_id
 FROM artifact_project_origins o JOIN chats c ON c.id=o.chat_id AND c.workspace_id=o.workspace_id AND c.agent_id=o.agent_id
 JOIN agents a ON a.id=c.agent_id AND a.workspace_id=c.workspace_id AND a.deleted_at IS NULL
 JOIN pages p ON p.id=o.page_id AND p.workspace_id=o.workspace_id AND p.owner_user_id=o.human_user_id
 WHERE o.workspace_id=? AND o.page_id=? AND o.human_user_id=? AND c.created_by=o.human_user_id AND c.visibility='private'`, k.WorkspaceID, k.PageID, user.ID).Scan(&chat, &agent, &crew)
	if errors.Is(err, sql.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	audience, err := chataudience.CaptureTrustedQuery(ctx, tx, chat, user.ID, nil)
	if err != nil {
		return nil, err
	}
	if audience == nil || audience.WorkspaceID != k.WorkspaceID || audience.AgentID != agent || !h.artifactAgentPolicyAllows(ctx, k.WorkspaceID, crew) {
		return result, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT c.id FROM artifact_project_comments c
 JOIN artifact_project_comment_tasks t ON t.id=c.task_id AND t.comment_id=c.id AND t.workspace_id=c.workspace_id AND t.page_id=c.page_id AND t.base_version=c.version
 JOIN artifact_version_heads h ON h.workspace_id=c.workspace_id AND h.page_id=c.page_id AND h.latest_version=c.version
 WHERE c.workspace_id=? AND c.page_id=? AND c.author=? AND c.version=? AND t.status='pending' AND t.active_run_id IS NULL
 AND NOT EXISTS(SELECT 1 FROM artifact_project_comment_replies reply WHERE reply.comment_id=c.id)
 AND NOT EXISTS(SELECT 1 FROM artifact_project_message_intents i WHERE i.workspace_id=c.workspace_id AND i.page_id=c.page_id AND i.comment_id=c.id AND i.base_version=c.version)
 ORDER BY c.created_at,c.id LIMIT 500`, k.WorkspaceID, k.PageID, user.ID, selectedVersion)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result[id] = true
	}
	return result, rows.Err()
}

func (h *PageHandler) artifactProjectComments(r *http.Request, k artifacts.VersionKey, version int64, sharedOnly bool) (out []artifactProjectComment, err error) {
	out = []artifactProjectComment{}
	err = h.artifactProjectCommentTx(r, k, false, func(ctx context.Context, tx *sql.Tx, _ string) error {
		user := UserFromContext(ctx)
		var owner string
		var pin int64
		if e := tx.QueryRowContext(ctx, `SELECT p.owner_user_id,v.shared_version FROM pages p JOIN artifact_version_heads v ON v.page_id=p.id AND v.workspace_id=p.workspace_id WHERE p.id=? AND p.workspace_id=?`, k.PageID, k.WorkspaceID).Scan(&owner, &pin); e != nil {
			return e
		}
		if user == nil {
			return errArtifactProjectAccess
		}
		if owner != user.ID {
			sharedOnly = true
			rec, e := h.loadPageForRead(ctx, k.WorkspaceID, "@"+k.PageID)
			if e != nil {
				return e
			}
			viewer, e := h.loadViewer(ctx, k.WorkspaceID, user.ID)
			if e != nil {
				return e
			}
			latestEditor := h.artifactProjectHumanEditor(ctx, k, rec, viewer)
			var latest int64
			if e := tx.QueryRowContext(ctx, `SELECT latest_version FROM artifact_version_heads WHERE workspace_id=? AND page_id=?`, k.WorkspaceID, k.PageID).Scan(&latest); e != nil {
				return e
			}
			if (!latestEditor && version != pin) || (latestEditor && version != latest) {
				return artifacts.ErrVersionNotFound
			}
		}
		rows, e := tx.QueryContext(ctx, `SELECT id,version,path,selector,quote,text,author,coalesce(task_id,''),created_at,coalesce((SELECT status FROM artifact_project_comment_tasks t WHERE t.comment_id=artifact_project_comments.id),'comment'),coalesce((SELECT max(version) FROM artifact_project_comment_replies reply WHERE reply.comment_id=artifact_project_comments.id AND reply.needs_clarification=0),0) FROM artifact_project_comments WHERE workspace_id=? AND page_id=? AND (?=0 OR version=?) ORDER BY created_at,id LIMIT 500`, k.WorkspaceID, k.PageID, sharedOnly, version)
		if e != nil {
			return e
		}
		for rows.Next() {
			var c artifactProjectComment
			c.Replies = []artifactProjectReply{}
			if e = rows.Scan(&c.ID, &c.Version, &c.Path, &c.Selector, &c.Quote, &c.Text, &c.Author, &c.TaskID, &c.CreatedAt, &c.Status, &c.AnsweredVersion); e != nil {
				rows.Close()
				return e
			}
			c.AuthorKind = artifactProjectAuthorKind(c.Author)
			out = append(out, c)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		retryable := map[string]bool{}
		if !sharedOnly && owner == user.ID && len(out) > 0 {
			retryable, e = h.artifactProjectRetryableComments(r, ctx, tx, k, version)
			if e != nil {
				return e
			}
		}
		for i := range out {
			out[i].DeliveryRetryable = retryable[out[i].ID]
			replies, e := tx.QueryContext(ctx, `SELECT text,version,needs_clarification,author,created_at FROM artifact_project_comment_replies WHERE comment_id=? AND (?=0 OR version=?) ORDER BY created_at,id`, out[i].ID, sharedOnly, version)
			if e != nil {
				return e
			}
			for replies.Next() {
				var reply artifactProjectReply
				if e = replies.Scan(&reply.Text, &reply.Version, &reply.NeedsClarification, &reply.Author, &reply.CreatedAt); e != nil {
					replies.Close()
					return e
				}
				reply.AuthorKind = artifactProjectAuthorKind(reply.Author)
				out[i].Replies = append(out[i].Replies, reply)
			}
			e = replies.Err()
			replies.Close()
			if e != nil {
				return e
			}
			if sharedOnly {
				// A private answer cannot reveal a newer version or queue state
				// to somebody entitled only to the selected shared pin.
				out[i].AnsweredVersion = 0
				out[i].TaskID = ""
				out[i].Status = "comment"
				for _, reply := range out[i].Replies {
					if reply.NeedsClarification {
						if out[i].AnsweredVersion == 0 {
							out[i].Status = "needs_clarification"
						}
						continue
					}
					out[i].Status = "answered"
					if reply.Version > out[i].AnsweredVersion {
						out[i].AnsweredVersion = reply.Version
					}
				}
			}
		}
		return nil
	})
	return
}
func (h *PageHandler) GetArtifactProjectComments(w http.ResponseWriter, r *http.Request) {
	// Return the same authorized, pinned snapshot as the project read, then select
	// comments client-side. This keeps comments from becoming a history backdoor.
	h.GetArtifactProject(w, r)
}
func (h *PageHandler) CreateArtifactProjectComment(w http.ResponseWriter, r *http.Request) {
	k, ok := h.artifactProjectStart(w, r)
	if !ok {
		return
	}
	if _, err := h.artifactProjectStore(r.Context()).Quota(r.Context(), k, h.artifactProjectCommentAuthority(r, k)); err != nil {
		artifactProjectError(w, err)
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
	if !artifactProjectDecode(w, r, &p) {
		return
	}
	if p.Version < 1 || len(p.Selector) > 2000 || len(p.Quote) > 2000 || strings.TrimSpace(p.Text) == "" || len(p.Text) > 10000 {
		artifactProjectError(w, artifacts.ErrVersionInvalid)
		return
	}
	out := artifactProjectComment{ID: generateCUID(), Version: p.Version, Path: p.Path, Selector: p.Selector, Quote: p.Quote, Text: p.Text, Replies: []artifactProjectReply{}, CreatedAt: tsformat.Format(h.clock.Now()), Status: "comment"}
	err := h.artifactProjectCommentTxAuthorized(r, k, h.artifactProjectCommentAuthority(r, k), func(ctx context.Context, tx *sql.Tx, actor string) error {
		// A Comment grant is feedback only. Queuing an agent task needs Edit in
		// this same fenced transaction, as Design comments do.
		if p.SendToAgent {
			if _, err := h.artifactProjectAuthority(r, k, true, false)(ctx, tx); err != nil {
				if errors.Is(err, artifacts.ErrVersionNotFound) {
					return errArtifactProjectAccess
				}
				return err
			}
		}
		head, err := h.artifactCommentVersion(ctx, tx, k, actor)
		if err != nil {
			return err
		}
		if head != p.Version {
			return artifacts.ErrVersionConflict
		}
		var manifest struct {
			Files map[string]struct {
				Hash string `json:"hash"`
			} `json:"files"`
		}
		var raw string
		if err := tx.QueryRowContext(ctx, `SELECT manifest_json FROM artifact_versions WHERE workspace_id=? AND page_id=? AND version=?`, k.WorkspaceID, k.PageID, p.Version).Scan(&raw); err != nil {
			return err
		}
		if json.Unmarshal([]byte(raw), &manifest) != nil {
			return artifacts.ErrVersionInvalid
		}
		file, exists := manifest.Files[p.Path]
		if !exists {
			return artifacts.ErrVersionConflict
		}
		var content []byte
		if err := tx.QueryRowContext(ctx, `SELECT content FROM artifact_version_blobs WHERE workspace_id=? AND page_id=? AND hash=?`, k.WorkspaceID, k.PageID, file.Hash).Scan(&content); err != nil {
			return err
		}
		if !artifactProjectAnchor(content, p.Selector, p.Quote) {
			return errArtifactProjectAnchor
		}
		out.Author = actor
		out.AuthorKind = artifactProjectAuthorKind(actor)
		if p.SendToAgent {
			out.TaskID = generateCUID()
			out.Status = "pending"
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO artifact_project_comments(id,workspace_id,page_id,version,path,selector,quote,text,author,task_id,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, out.ID, k.WorkspaceID, k.PageID, p.Version, p.Path, p.Selector, p.Quote, p.Text, actor, nullIfEmpty(out.TaskID), out.CreatedAt)
		if err != nil {
			return err
		}
		if out.TaskID != "" {
			_, err = tx.ExecContext(ctx, `INSERT INTO artifact_project_comment_tasks(id,comment_id,workspace_id,page_id,base_version,created_at) VALUES(?,?,?,?,?,?)`, out.TaskID, out.ID, k.WorkspaceID, k.PageID, p.Version, out.CreatedAt)
		}
		return err
	})
	if err != nil {
		artifactProjectError(w, err)
		return
	}
	writeJSON(w, 201, out)
}
func (h *PageHandler) ReplyArtifactProjectComment(w http.ResponseWriter, r *http.Request) {
	k, ok := h.artifactProjectStart(w, r)
	if !ok {
		return
	}
	if _, err := h.artifactProjectStore(r.Context()).Quota(r.Context(), k, h.artifactProjectOwnerAuthority(r, k)); err != nil {
		artifactProjectError(w, err)
		return
	}
	var req struct {
		Text               string `json:"text"`
		Version            int64  `json:"version"`
		NeedsClarification bool   `json:"needs_clarification,omitempty"`
	}
	if !artifactProjectDecode(w, r, &req) {
		return
	}
	p := artifactProjectReply{Text: req.Text, Version: req.Version, NeedsClarification: req.NeedsClarification}
	if p.Version < 1 || strings.TrimSpace(p.Text) == "" || len(p.Text) > 10000 {
		artifactProjectError(w, artifacts.ErrVersionInvalid)
		return
	}
	err := h.artifactProjectCommentTx(r, k, true, func(ctx context.Context, tx *sql.Tx, actor string) error {
		var exists int
		if tx.QueryRowContext(ctx, `SELECT count(*) FROM artifact_project_comments c JOIN artifact_versions v ON v.page_id=c.page_id AND v.workspace_id=c.workspace_id WHERE c.id=? AND c.workspace_id=? AND c.page_id=? AND v.version=? AND (v.version>c.version OR (?=1 AND v.version=c.version))`, r.PathValue("commentId"), k.WorkspaceID, k.PageID, p.Version, p.NeedsClarification).Scan(&exists) != nil || exists != 1 {
			return artifacts.ErrVersionNotFound
		}
		if a, _ := r.Context().Value(artifactRunKey{}).(*artifactRunAuthority); a != nil {
			var target string
			e := tx.QueryRowContext(ctx, `SELECT coalesce(comment_id,'') FROM artifact_project_message_intents WHERE run_id=? AND workspace_id=? AND page_id=?`, a.RunID, k.WorkspaceID, k.PageID).Scan(&target)
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return e
			}
			if target != "" && target != r.PathValue("commentId") {
				return errArtifactProjectAccess
			}
			// A run with no comment target may answer only with a version it
			// published below; it cannot close feedback as needs_clarification.
			if target == "" && p.NeedsClarification {
				return errArtifactProjectAccess
			}
			// No run answers a task another run holds.
			var heldByOther bool
			if e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artifact_project_comment_tasks WHERE comment_id=? AND active_run_id IS NOT NULL AND active_run_id<>?)`, r.PathValue("commentId"), a.RunID).Scan(&heldByOther); e != nil {
				return e
			}
			if heldByOther {
				return errArtifactProjectAccess
			}
			if !p.NeedsClarification {
				var author string
				if tx.QueryRowContext(ctx, `SELECT actor FROM artifact_versions WHERE workspace_id=? AND page_id=? AND version=?`, k.WorkspaceID, k.PageID, p.Version).Scan(&author) != nil || author != actor {
					return errArtifactProjectAccess
				}
			}
		}
		p.Author = actor
		p.AuthorKind = artifactProjectAuthorKind(actor)
		p.CreatedAt = tsformat.Format(h.clock.Now())
		_, err := tx.ExecContext(ctx, `INSERT INTO artifact_project_comment_replies(id,comment_id,version,text,author,created_at,needs_clarification) VALUES(?,?,?,?,?,?,?)`, generateCUID(), r.PathValue("commentId"), p.Version, p.Text, p.Author, p.CreatedAt, p.NeedsClarification)
		if err != nil {
			return err
		}
		status := "answered"
		if p.NeedsClarification {
			status = "needs_clarification"
		}
		_, err = tx.ExecContext(ctx, `UPDATE artifact_project_comment_tasks SET status=? WHERE comment_id=?`, status, r.PathValue("commentId"))
		return err
	})
	if err != nil {
		artifactProjectError(w, err)
		return
	}
	writeJSON(w, 200, p)
}
