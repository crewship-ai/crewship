package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
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

// artifactProjectRunPage reads the retained scope without replacing it with a
// caller-provided slug. A NULL binding authorizes creation, not another Page.
func (h *PageHandler) artifactProjectRunPage(ctx context.Context, a *artifactRunAuthority) (sql.NullString, error) {
	var page sql.NullString
	if a == nil {
		return page, errArtifactProjectAccess
	}
	err := h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT result_page_id FROM artifact_project_run_consents WHERE run_id=? AND workspace_id=?`, a.RunID, a.WorkspaceID).Scan(&page)
	return page, err
}

// GrantArtifactProjectRunConsent is a human-only deliberate authorization. The
// client may select a run, but cannot nominate its human/chat/message identity.
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
				if err := walk(depth + 1); err != nil {
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
