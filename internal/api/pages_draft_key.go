package api

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/encryption"
)

const localDraftKeyDomain = "crewship-local-draft-key-v1"

var errLocalDraftReadDenied = errors.New("local draft read denied")

type localDraftKeyEnvelope struct {
	Domain    string `json:"domain"`
	Human     string `json:"human"`
	Workspace string `json:"workspace"`
	Page      string `json:"page"`
	Key       string `json:"key"`
}

type localDraftKeyRequest struct {
	Human     string `json:"human"`
	Operation string `json:"operation"`
	Key       string `json:"key,omitempty"`
	Envelope  string `json:"envelope,omitempty"`
}

func validLocalDraftKey(key string) bool {
	raw, err := base64.StdEncoding.DecodeString(key)
	return err == nil && len(raw) == 32 && base64.StdEncoding.EncodeToString(raw) == key
}

// LocalDraftKey wraps only a browser-generated data key, never draft content.
// The server stores neither. Recovery requires the original immutable human,
// workspace and Page, with current read authority checked under a writer fence.
func (h *PageHandler) LocalDraftKey(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	human := UserFromContext(r.Context())
	agent, _ := pageGrantCallerIsAgent(r)
	if human == nil || agent || projectAgentFrom(r.Context()) != nil || InternalTokenWorkspaceFromContext(r.Context()) != "" {
		replyError(w, 403, "human draft recovery required")
		return
	}
	ref := r.PathValue("slug")
	if !strings.HasPrefix(ref, "@") || !immutablePageReferenceID.MatchString(strings.TrimPrefix(ref, "@")) {
		replyError(w, 400, "immutable Page reference required")
		return
	}
	key := artifacts.VersionKey{WorkspaceID: WorkspaceIDFromContext(r.Context()), PageID: strings.TrimPrefix(ref, "@")}
	var input localDraftKeyRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		replyError(w, 400, "invalid local draft key request")
		return
	}
	if input.Human == "" {
		replyError(w, 400, "expected draft identity required")
		return
	}
	if input.Human != human.ID {
		writeJSON(w, 403, map[string]string{"error": "draft identity changed; sign in as the original owner", "code": "draft_identity_mismatch"})
		return
	}
	payload := localDraftKeyEnvelope{Domain: localDraftKeyDomain, Human: human.ID, Workspace: key.WorkspaceID, Page: key.PageID, Key: input.Key}
	switch input.Operation {
	case "wrap":
		if !validLocalDraftKey(input.Key) || input.Envelope != "" {
			replyError(w, 400, "invalid local draft key")
			return
		}
	case "unwrap":
		if input.Key != "" || len(input.Envelope) == 0 || len(input.Envelope) > 2048 {
			replyError(w, 400, "invalid local draft envelope")
			return
		}
		payload = localDraftKeyEnvelope{}
		plain, err := encryption.Decrypt(input.Envelope)
		if err != nil || json.Unmarshal([]byte(plain), &payload) != nil || payload.Domain != localDraftKeyDomain || payload.Human == "" || payload.Workspace == "" || payload.Page == "" || !validLocalDraftKey(payload.Key) {
			replyError(w, 400, "local draft envelope unavailable")
			return
		}
		if payload.Human != human.ID {
			writeJSON(w, 403, map[string]string{"error": "sign in as the original draft owner", "code": "draft_identity_mismatch"})
			return
		}
		if payload.Workspace != key.WorkspaceID || payload.Page != key.PageID {
			replyError(w, 400, "local draft scope mismatch")
			return
		}
	default:
		replyError(w, 400, "invalid local draft key operation")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "open local draft read fence", err)
		return
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(r.Context(), `UPDATE document_writer_fence SET seq=seq WHERE id=1`); err != nil {
		replyInternalError(w, h.logger, "fence local draft recovery", err)
		return
	}
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	authorize := func() error {
		if err := validateContentHumanCredential(r.WithContext(ctx), tx, "pages:read"); err != nil {
			return err
		}
		return h.localDraftRead(ctx, r, key, tx)
	}
	fail := func(err error) {
		switch {
		case errors.Is(err, errContentCredentialUnverified):
			replyError(w, 503, "authentication could not be checked; draft retained")
		case errors.Is(err, errContentCredentialScope):
			replyError(w, 403, "current read credential scope required")
		case errors.Is(err, errContentCredentialNotCurrent), errors.Is(err, errInvalidCLIToken):
			replyError(w, 401, "fresh human authentication required")
		case errors.Is(err, errLocalDraftReadDenied):
			writeJSON(w, 403, map[string]string{"error": "current Page read access is required", "code": "draft_read_revoked"})
		case errors.Is(err, artifacts.ErrDisabled):
			replyError(w, 503, "Page application is unavailable; draft retained")
		default:
			replyInternalError(w, h.logger, "authorize local draft recovery", err)
		}
	}
	if err = authorize(); err != nil {
		fail(err)
		return
	}
	var response any
	if input.Operation == "wrap" {
		raw, _ := json.Marshal(payload)
		envelope, wrapErr := encryption.Encrypt(string(raw))
		if wrapErr != nil {
			replyError(w, 503, "local draft protection is unavailable; existing drafts retained")
			return
		}
		response = map[string]string{"envelope": envelope}
	} else {
		response = map[string]string{"key": payload.Key}
	}
	if err = authorize(); err != nil {
		fail(err)
		return
	}
	if err = tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "confirm local draft read fence", err)
		return
	}
	writeJSON(w, 200, response)
}

func (h *PageHandler) localDraftRead(ctx context.Context, r *http.Request, key artifacts.VersionKey, tx *sql.Tx) error {
	var family string
	err := tx.QueryRowContext(ctx, `SELECT content_type FROM artifact_version_heads WHERE workspace_id=? AND page_id=?`, key.WorkspaceID, key.PageID).Scan(&family)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	// Use the canonical Page ACL, without Design's outward-facing error
	// masking. A failed lookup must never authorize browser data deletion.
	human := UserFromContext(ctx)
	viewer, err := h.livePrivatePageReader(ctx, key.WorkspaceID, human.ID)
	if err != nil {
		return err
	}
	if viewer == nil {
		return errLocalDraftReadDenied
	}
	if family == "design" && !h.designEnabled(ctx) {
		return artifacts.ErrDisabled
	}
	// Owner decision is local to Design. livePrivatePageReader suppresses
	// manager power for any restricted membership; other Page families keep
	// their existing private-custody boundary.
	if family == "design" && canRole(viewer.Role, "manage") {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pages WHERE workspace_id=? AND id=?)`, key.WorkspaceID, key.PageID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return errLocalDraftReadDenied
		}
		return nil
	}
	rec, err := h.loadPageForRead(ctx, key.WorkspaceID, "@"+key.PageID)
	if errors.Is(err, sql.ErrNoRows) {
		return errLocalDraftReadDenied
	}
	if err != nil {
		return err
	}
	var restricted bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workspace_members WHERE user_id=? AND access_mode='restricted')`, human.ID).Scan(&restricted); err != nil {
		return err
	}
	if restricted {
		if !rec.PrivateAudience || rec.OwnerUserID != human.ID {
			return errLocalDraftReadDenied
		}
		return nil
	}
	fullViewer, err := h.loadViewer(ctx, key.WorkspaceID, human.ID)
	if err != nil {
		return err
	}
	fullViewer.Role = viewer.Role
	visible, err := h.canSeePage(ctx, key.WorkspaceID, rec, nil, fullViewer)
	if err != nil {
		return err
	}
	if !visible {
		return errLocalDraftReadDenied
	}
	return nil
}

// Resolve scope without deciding membership. Missing membership is checked
// after expected identity and current credential validation, in the same SQL
// transaction as Page read. Operational errors retain the browser draft.
func (m *AuthMiddleware) localDraftWorkspace(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if UserFromContext(r.Context()) == nil {
			writeAuthError(w, 401, reasonNoCredentials)
			return
		}
		workspace := r.URL.Query().Get("workspace_id")
		if workspace == "" {
			workspace = r.Header.Get("X-Workspace-ID")
		}
		if workspace == "" {
			replyError(w, 400, "workspace_id is required")
			return
		}
		var canonical string
		err := m.db.QueryRowContext(r.Context(), `SELECT id FROM workspaces WHERE (id=? OR slug=?) AND deleted_at IS NULL LIMIT 1`, workspace, workspace).Scan(&canonical)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			replyInternalError(w, m.logger, "resolve local draft workspace", err)
			return
		}
		if canonical == "" {
			canonical = workspace
		}
		ctx := context.WithValue(r.Context(), ctxWorkspaceID, canonical)
		ctx = context.WithValue(ctx, ctxRole, "")
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
