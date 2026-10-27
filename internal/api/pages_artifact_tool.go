package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// The exact Page mandate is checked again in VersionStore's writer transaction.
// Captured chat provenance and an administrative policy alone grant no access.
func (h *PageHandler) checkArtifactProjectRunConsent(ctx context.Context, a *artifactRunAuthority, page string) error {
	if a == nil || !h.artifactProjectEnabled(ctx) || h.checkOriginalUserArtifactRunPrincipal(ctx, a) != nil || !h.artifactAgentPolicyAllows(ctx, a.WorkspaceID, a.CrewID) {
		return errors.New("original live artifact authority required")
	}
	if _, err := h.validateArtifactPrincipalOriginalCredential(ctx, a, "pages:write"); err != nil {
		return err
	}
	var expiry, sourceKind string
	var result sql.NullString
	err := h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT s.expires_at,s.result_page_id,s.source_kind FROM artifact_project_run_consents s JOIN chats c ON c.id=s.chat_id AND c.workspace_id=s.workspace_id AND c.agent_id=? AND c.created_by=s.human_user_id AND c.visibility='private' JOIN conversation_messages m ON m.id=s.message_id AND m.session_id=s.chat_id AND m.author_user_id=s.human_user_id AND m.agent_id=c.agent_id AND m.role='user' WHERE s.run_id=? AND s.workspace_id=? AND s.human_user_id=? AND s.chat_id=? AND s.message_id=? AND s.revoked_at IS NULL`, a.AgentID, a.RunID, a.WorkspaceID, a.HumanUserID, a.ChatID, a.MessageID).Scan(&expiry, &result, &sourceKind)
	limit, parseErr := time.Parse(time.RFC3339Nano, expiry)
	if err != nil || parseErr != nil || !time.Now().Before(limit) || (page == "" && result.Valid) || (page != "" && (!result.Valid || result.String != page)) {
		return errors.New("explicit exact-run artifact permission required")
	}
	// Manual permission has no typed row. Once a trusted message intent exists,
	// absence, revocation or changed binding must never degrade to that broader
	// manual-grant path. Also inspect message_id to catch a foreign run binding.
	var typed, valid int
	err = h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT count(*),coalesce(sum(CASE WHEN run_id=? AND workspace_id=? AND human_user_id=? AND chat_id=? AND message_id=? AND revoked_at IS NULL AND (page_id IS NULL OR page_id=?) THEN 1 ELSE 0 END),0) FROM artifact_project_message_intents WHERE run_id=? OR message_id=?`, a.RunID, a.WorkspaceID, a.HumanUserID, a.ChatID, a.MessageID, result.String, a.RunID, a.MessageID).Scan(&typed, &valid)
	if err != nil || typed > 1 || typed != valid || sourceKind == "chat" && typed != 1 {
		return errors.New("original artifact message intent changed or was revoked")
	}
	return nil
}

// GrantArtifactProjectRunConsent is a human-only deliberate authorization. The
// client may select a run, but cannot nominate its human/chat/message identity.
func (h *PageHandler) GrantArtifactProjectRunConsent(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	if !h.artifactProjectEnabled(r.Context()) {
		replyError(w, 404, "artifact projects are disabled")
		return
	}
	user := UserFromContext(r.Context())
	agent, _ := pageGrantCallerIsAgent(r)
	if user == nil || agent || projectAgentFrom(r.Context()) != nil || r.Context().Value(artifactRunKey{}) != nil || InternalTokenWorkspaceFromContext(r.Context()) != "" || InternalTokenCrewFromContext(r.Context()) != "" {
		replyError(w, 403, "human authentication required")
		return
	}
	var body struct {
		RunID  string `json:"run_id"`
		PageID string `json:"page_id,omitempty"`
	}
	if !decodeArtifactProjectToolJSON(w, r, &body, 8192) {
		return
	}
	if body.RunID == "" {
		replyError(w, 400, "run_id is required")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyError(w, 500, "cannot begin artifact permission")
		return
	}
	defer tx.Rollback()
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	if _, err = tx.ExecContext(ctx, `UPDATE workspace_members SET role=role WHERE workspace_id=? AND user_id=?`, WorkspaceIDFromContext(ctx), user.ID); err != nil {
		replyError(w, 500, "cannot fence artifact permission")
		return
	}
	if err = validateContentHumanCredential(r.WithContext(ctx), tx, "pages:write"); err != nil {
		replyError(w, 401, "current human credential required")
		return
	}
	a := &artifactRunAuthority{RunID: body.RunID, WorkspaceID: WorkspaceIDFromContext(ctx), HumanUserID: user.ID}
	var expiry string
	err = tx.QueryRowContext(ctx, `SELECT agent_id,crew_id,chat_id,message_id,expires_at FROM artifact_run_principals WHERE run_id=? AND workspace_id=? AND human_user_id=?`, a.RunID, a.WorkspaceID, a.HumanUserID).Scan(&a.AgentID, &a.CrewID, &a.ChatID, &a.MessageID, &expiry)
	if err != nil || h.checkOriginalUserArtifactRunPrincipal(ctx, a) != nil || !h.artifactAgentPolicyAllows(ctx, a.WorkspaceID, a.CrewID) || body.PageID == "" && !h.artifactPrincipalMayCreate(ctx, a.WorkspaceID, a.HumanUserID) {
		replyError(w, 403, "live owned chat run and current create permission required")
		return
	}
	var private bool
	err = tx.QueryRowContext(ctx, `SELECT visibility='private' AND created_by=? FROM chats WHERE id=? AND workspace_id=? AND agent_id=?`, user.ID, a.ChatID, a.WorkspaceID, a.AgentID).Scan(&private)
	if err != nil || !private {
		replyError(w, 403, "private chat author required")
		return
	}
	sealed, sealErr := tx.ExecContext(ctx, `UPDATE chats SET private_artifact_author_user_id=? WHERE id=? AND workspace_id=? AND agent_id=? AND created_by=? AND visibility='private' AND (private_artifact_author_user_id IS NULL OR private_artifact_author_user_id=?)`, user.ID, a.ChatID, a.WorkspaceID, a.AgentID, user.ID, user.ID)
	if sealErr != nil {
		replyError(w, 403, "private artifact chat classification changed")
		return
	}
	if n, e := sealed.RowsAffected(); e != nil || n != 1 {
		replyError(w, 403, "private artifact chat classification changed")
		return
	}
	var page any
	if body.PageID != "" {
		var allowed int
		if tx.QueryRowContext(ctx, `SELECT count(*) FROM pages p JOIN artifact_version_heads v ON v.workspace_id=p.workspace_id AND v.page_id=p.id AND v.content_type='artifact_project' WHERE p.workspace_id=? AND p.id=? AND p.owner_user_id=? AND p.owner_crew_id IS NULL`, a.WorkspaceID, body.PageID, user.ID).Scan(&allowed) != nil || allowed != 1 {
			replyError(w, 403, "exact owned artifact project required")
			return
		}
		page = body.PageID
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO artifact_project_run_consents(run_id,workspace_id,human_user_id,chat_id,message_id,expires_at,created_at,result_page_id) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(run_id) DO NOTHING`, a.RunID, a.WorkspaceID, a.HumanUserID, a.ChatID, a.MessageID, expiry, tsformat.Format(time.Now()), page)
	if err != nil {
		replyError(w, 500, "cannot retain artifact permission")
		return
	}
	var revoked, existingPage sql.NullString
	if tx.QueryRowContext(ctx, `SELECT revoked_at,result_page_id FROM artifact_project_run_consents WHERE run_id=?`, a.RunID).Scan(&revoked, &existingPage) != nil || revoked.Valid || body.PageID != "" && existingPage.String != body.PageID {
		replyError(w, 403, "artifact permission was revoked")
		return
	}
	if err = h.checkArtifactProjectRunConsent(ctx, a, existingPage.String); err != nil {
		replyError(w, 403, "original artifact permission changed; send a new explicit request")
		return
	}
	if err = tx.Commit(); err != nil {
		replyError(w, 500, "artifact permission outcome unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"run_id": a.RunID, "expires_at": expiry, "authorized": true, "page_id": existingPage.String})
}

func decodeArtifactProjectToolJSON(w http.ResponseWriter, r *http.Request, target any, limit int64) bool {
	raw, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil || int64(len(raw)) > limit {
		replyError(w, 413, "artifact request exceeds limit")
		return false
	}
	// This audited decoder rejects duplicate keys at every depth and unknown
	// struct fields, preserving closed transport identity and operation fields.
	if err = decodeArtifactProjectJSONBytes(raw, target); err != nil {
		replyError(w, 400, "invalid closed artifact request")
		return false
	}
	return true
}

func (h *PageHandler) ArtifactProjectTool(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	checked, ok := h.artifactAgentRequest(w, r)
	if !ok {
		return
	}
	if !h.artifactProjectEnabled(r.Context()) {
		replyError(w, 404, "artifact projects are disabled")
		return
	}
	r = checked
	var body struct {
		Operation string          `json:"operation"`
		Slug      string          `json:"slug"`
		Payload   json.RawMessage `json:"payload"`
	}
	if !decodeArtifactProjectToolJSON(w, r, &body, (9<<20)+8192) {
		return
	}
	a, _ := r.Context().Value(artifactRunKey{}).(*artifactRunAuthority)
	if h.checkOriginalUserArtifactRunPrincipal(r.Context(), a) != nil || !h.artifactAgentPolicyAllows(r.Context(), a.WorkspaceID, a.CrewID) {
		replyError(w, 403, "current artifact run authority required")
		return
	}
	forward := r.Clone(r.Context())
	forward.Body = io.NopCloser(bytes.NewReader(body.Payload))
	forward.ContentLength = int64(len(body.Payload))
	forward.SetPathValue("slug", body.Slug)
	forward.Header = r.Header.Clone()
	forward.Header.Set("Content-Type", "application/json")
	switch body.Operation {
	case "artifact_create":
		var payload map[string]json.RawMessage
		if decodeArtifactProjectJSONBytes(body.Payload, &payload) != nil {
			replyError(w, 400, "invalid artifact create payload")
			return
		}
		payload["slug"], _ = json.Marshal(body.Slug)
		raw, _ := json.Marshal(payload)
		forward.Body, forward.ContentLength = io.NopCloser(bytes.NewReader(raw)), int64(len(raw))
		h.CreateArtifactProject(w, forward)
	case "artifact_publish":
		h.PublishArtifactProject(w, forward)
	case "artifact_restore":
		h.RestoreArtifactProject(w, forward)
	case "artifact_versions", "artifact_comments":
		var payload struct {
			Version int64 `json:"version"`
		}
		if decodeArtifactProjectJSONBytes(body.Payload, &payload) != nil || payload.Version < 0 {
			replyError(w, 400, "invalid artifact read")
			return
		}
		query := forward.URL.Query()
		if payload.Version > 0 {
			query.Set("version", strconv.FormatInt(payload.Version, 10))
		}
		forward.URL.RawQuery, forward.Method = query.Encode(), http.MethodGet
		if body.Operation == "artifact_comments" {
			h.GetArtifactProjectComments(w, forward)
		} else {
			h.GetArtifactProject(w, forward)
		}
	case "artifact_reply":
		var payload struct {
			CommentID          string `json:"comment_id"`
			Text               string `json:"text"`
			Version            int64  `json:"version"`
			NeedsClarification bool   `json:"needs_clarification,omitempty"`
		}
		if decodeArtifactProjectJSONBytes(body.Payload, &payload) != nil || payload.CommentID == "" {
			replyError(w, 400, "invalid artifact reply")
			return
		}
		forward.SetPathValue("commentId", payload.CommentID)
		raw, _ := json.Marshal(map[string]any{"text": payload.Text, "version": payload.Version, "needs_clarification": payload.NeedsClarification})
		forward.Body, forward.ContentLength = io.NopCloser(bytes.NewReader(raw)), int64(len(raw))
		h.ReplyArtifactProjectComment(w, forward)
	default:
		replyError(w, 400, "unknown artifact operation")
	}
}

func decodeArtifactProjectJSONBytes(raw []byte, out any) error {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' || len(raw) > 36<<20 || !utf8.Valid(raw) {
		return errors.New("artifact JSON must fit 1 MiB of valid UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 128 {
			return errors.New("artifact JSON nesting exceeds limit")
		}
		tok, err := d.Token()
		if err != nil {
			return err
		}
		if tok == nil {
			return errors.New("null artifact fields are not accepted")
		}
		switch tok {
		case json.Delim('{'):
			seen := map[string]bool{}
			for d.More() {
				k, err := d.Token()
				if err != nil {
					return err
				}
				key, ok := k.(string)
				if !ok || key != strings.ToLower(key) || seen[key] {
					return errors.New("duplicate artifact field")
				}
				seen[key] = true
				if err = walk(depth + 1); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		case json.Delim('['):
			for d.More() {
				if err := walk(0); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		}
		return nil
	}
	if err := walk(0); err != nil {
		return errors.New("one closed artifact object required")
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing artifact JSON")
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return errors.New("invalid or unknown artifact field")
	}
	return nil
}
