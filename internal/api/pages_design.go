package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/design"
	"github.com/crewship-ai/crewship/internal/featureflags"
	"github.com/crewship-ai/crewship/internal/pages"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

var errDesignAccess = errors.New("design write access denied")

type designHTTP struct {
	mu    sync.Mutex
	rooms map[artifacts.VersionKey]*designRoom
}
type designRoom struct {
	mu      sync.Mutex
	clients map[*designClient]bool
}

func (h *PageHandler) designRoom(k artifacts.VersionKey) *designRoom {
	h.designOnce.Do(func() { h.design = &designHTTP{rooms: map[artifacts.VersionKey]*designRoom{}} })
	h.design.mu.Lock()
	defer h.design.mu.Unlock()
	if room := h.design.rooms[k]; room != nil {
		return room
	}
	room := &designRoom{clients: map[*designClient]bool{}}
	h.design.rooms[k] = room
	return room
}
func (h *PageHandler) designEnabled(ctx context.Context) bool {
	if os.Getenv("CREWSHIP_ARTIFACTS_DESIGN_ENABLED") == "true" {
		return true
	}
	enabled, err := featureflags.IsEnabledWith(ctx, h.pageAuthReader(ctx), WorkspaceIDFromContext(ctx), "artifacts_design")
	return err == nil && enabled
}
func (h *PageHandler) designStore(ctx context.Context) *artifacts.VersionStore {
	return artifacts.NewVersionStore(h.db, artifacts.VersionOptions{Enabled: h.designEnabled(ctx), ContentType: "design"})
}
func designError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, artifacts.ErrDisabled), errors.Is(err, sql.ErrNoRows), errors.Is(err, artifacts.ErrVersionNotFound):
		replyError(w, 404, "design not found")
	case errors.Is(err, errDesignAccess), errors.Is(err, artifacts.ErrVersionAuthority):
		replyError(w, 403, "current Design edit permission required")
	case errors.Is(err, design.ErrUnavailable):
		replyError(w, 503, "Design engine is unavailable; changes are not acknowledged")
	case errors.Is(err, design.ErrInvalid), errors.Is(err, artifacts.ErrVersionInvalid):
		replyError(w, 400, "invalid Design update")
	case errors.Is(err, artifacts.ErrVersionConflict):
		replyError(w, 409, "Design changed; reload and reapply")
	case errors.Is(err, artifacts.ErrVersionQuota):
		replyError(w, 413, "Design capacity is full; existing work is retained")
	default:
		replyError(w, 500, "Design operation failed; changes are not acknowledged")
	}
}
func (h *PageHandler) designKey(w http.ResponseWriter, r *http.Request) (artifacts.VersionKey, bool) {
	artifactCommentHeaders(w)
	k := artifacts.VersionKey{WorkspaceID: WorkspaceIDFromContext(r.Context())}
	if !h.designEnabled(r.Context()) {
		designError(w, artifacts.ErrDisabled)
		return k, false
	}
	if r.Context().Value(artifactRunKey{}) == nil {
		required := "pages:read"
		if r.Method != "GET" {
			required = "pages:write"
		}
		if validateContentHumanCredential(r, h.pageAuthReader(r.Context()), required) != nil {
			designError(w, errDesignAccess)
			return k, false
		}
	}
	ref, col := r.PathValue("slug"), "slug"
	if strings.HasPrefix(ref, "@") {
		ref, col = strings.TrimPrefix(ref, "@"), "id"
	}
	if h.db.QueryRowContext(r.Context(), `SELECT p.id FROM pages p JOIN artifact_design_documents d ON d.page_id=p.id AND d.workspace_id=p.workspace_id WHERE p.workspace_id=? AND p.`+col+`=?`, k.WorkspaceID, ref).Scan(&k.PageID) != nil {
		designError(w, sql.ErrNoRows)
		return k, false
	}
	return k, true
}

// A websocket retains the authority epoch admitted under the writer fence.
// Regranting an identical grant does not revive an older connection.
type designAdmissionKey struct{}
type designAdmission struct{ WorkspaceID, Revision string }
type designAuthorityStamp struct {
	actor, revision string
	initialized     bool
}

func (s *designAuthorityStamp) admit(actor, revision string) (string, error) {
	if s.initialized && (s.actor != actor || s.revision != revision) {
		return "", artifacts.ErrVersionAuthority
	}
	s.actor, s.revision, s.initialized = actor, revision, true
	return actor, nil
}
func designAuthorityRevision(ctx context.Context, tx *sql.Tx, workspace string) (string, error) {
	var revision string
	if err := tx.QueryRowContext(ctx, `SELECT CAST(authority_revision AS TEXT) FROM workspaces WHERE id=?`, workspace).Scan(&revision); err != nil {
		return "", err
	}
	if admitted, ok := ctx.Value(designAdmissionKey{}).(designAdmission); ok && (admitted.WorkspaceID != workspace || admitted.Revision != revision) {
		return "", artifacts.ErrVersionAuthority
	}
	return revision, nil
}

// Canonical Page ACLs, live credentials and membership are checked under the
// same SQLite writer fence as every persisted update. Read/Comment never writes.
func (h *PageHandler) designAuthority(r *http.Request, k artifacts.VersionKey, write bool) artifacts.VersionAuthorize {
	var stamp designAuthorityStamp
	return func(ctx context.Context, tx *sql.Tx) (string, error) {
		ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
		revision, err := designAuthorityRevision(ctx, tx, k.WorkspaceID)
		if err != nil {
			return "", err
		}
		if !h.designEnabled(ctx) {
			return "", artifacts.ErrDisabled
		}
		u := UserFromContext(ctx)
		if u == nil {
			return "", sql.ErrNoRows
		}
		request := r.WithContext(ctx)
		agent, _ := ctx.Value(artifactRunKey{}).(*artifactRunAuthority)
		if agent != nil {
			if agent.WorkspaceID != k.WorkspaceID || agent.HumanUserID != u.ID || h.checkOriginalUserArtifactRunPrincipal(ctx, agent) != nil || !h.artifactAgentPolicyAllows(ctx, k.WorkspaceID, agent.CrewID) {
				return "", errDesignAccess
			}
			if h.checkArtifactProjectRunConsent(ctx, agent, k.PageID) != nil {
				return "", errDesignAccess
			}
			required := "pages:read"
			if write {
				required = "pages:write"
			}
			if _, e := h.validateArtifactPrincipalOriginalCredential(ctx, agent, required); e != nil {
				return "", errDesignAccess
			}
		} else {
			isAgent, _ := pageGrantCallerIsAgent(request)
			if isAgent || projectAgentFrom(ctx) != nil || InternalTokenWorkspaceFromContext(ctx) != "" {
				return "", sql.ErrNoRows
			}
			scope := "pages:read"
			if write {
				scope = "pages:write"
			}
			if validateContentHumanCredential(request, tx, scope) != nil {
				return "", errDesignAccess
			}
		}
		role, err := h.liveContentHumanRole(ctx, k.WorkspaceID, u.ID)
		if err != nil {
			return "", sql.ErrNoRows
		}
		ctx = context.WithValue(ctx, ctxRole, role)
		rec, err := h.loadPageForRead(ctx, k.WorkspaceID, "@"+k.PageID)
		if err != nil {
			return "", sql.ErrNoRows
		}
		viewer, err := h.loadViewer(ctx, k.WorkspaceID, u.ID)
		if err != nil {
			return "", err
		}
		viewer.Role = role
		visible, err := h.canSeePage(ctx, k.WorkspaceID, rec, nil, viewer)
		if err != nil || !visible {
			return "", sql.ErrNoRows
		}
		if write && !h.mayEditSpec(ctx, k.WorkspaceID, u.ID, role, rec) {
			return "", errDesignAccess
		}
		if agent != nil {
			return stamp.admit("agent:"+agent.AgentID+":"+agent.RunID, revision)
		}
		return stamp.admit(u.ID, revision)
	}
}
func (h *PageHandler) designTx(r *http.Request, k artifacts.VersionKey, write bool, fn func(context.Context, *sql.Tx, string) error) error {
	return h.designAuthorizedTx(r, k, h.designAuthority(r, k, write), fn)
}
func (h *PageHandler) designAuthorizedTx(r *http.Request, k artifacts.VersionKey, auth artifacts.VersionAuthorize, fn func(context.Context, *sql.Tx, string) error) error {
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(r.Context(), `UPDATE document_writer_fence SET seq=seq WHERE id=1`); err != nil {
		return err
	}
	ctx := context.WithValue(r.Context(), pageAuthReaderKey{}, tx)
	actor, err := auth(ctx, tx)
	if err != nil {
		return err
	}
	if err = fn(ctx, tx, actor); err != nil {
		return err
	}
	final, err := auth(ctx, tx)
	if err != nil {
		return err
	}
	if final != actor {
		return artifacts.ErrVersionAuthority
	}
	return tx.Commit()
}
func designVersionRequest(state []byte, base int64, message string) artifacts.VersionRequest {
	return artifacts.VersionRequest{BaseVersion: base, EntryPoint: "design.yjs", Files: map[string]artifacts.VersionFile{"design.yjs": {Content: state, MediaType: "application/vnd.yjs-update"}}, Message: message}
}
func (h *PageHandler) CreateDesign(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	if !h.designEnabled(r.Context()) {
		designError(w, artifacts.ErrDisabled)
		return
	}
	var p struct {
		Slug          string `json:"slug"`
		Name          string `json:"name"`
		CreatedByTest bool   `json:"created_by_test"`
	}
	if !designDecode(w, r, &p, 8192) {
		return
	}
	spec := pages.Document{APIVersion: pages.DocumentAPIVersion, Kind: pages.DocumentKind, Metadata: pages.Metadata{Slug: p.Slug, Name: p.Name}}
	if spec.ValidateEmpty() != nil || len(p.Name) > 200 {
		designError(w, artifacts.ErrVersionInvalid)
		return
	}
	user := UserFromContext(r.Context())
	if user == nil {
		designError(w, errDesignAccess)
		return
	}
	if r.Context().Value(artifactRunKey{}) != nil {
		designError(w, errDesignAccess)
		return
	} // creation is deliberate human action
	result, err := design.Run(r.Context(), design.Request{})
	if err != nil {
		designError(w, err)
		return
	}
	k := artifacts.VersionKey{WorkspaceID: WorkspaceIDFromContext(r.Context()), PageID: generateCUID()}
	raw, _ := json.Marshal(spec)
	staged := false
	auth := func(ctx context.Context, tx *sql.Tx) (string, error) {
		ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
		if !h.designEnabled(ctx) || validateContentHumanCredential(r.WithContext(ctx), tx, "pages:write") != nil {
			return "", errDesignAccess
		}
		role, e := h.liveContentHumanRole(ctx, k.WorkspaceID, user.ID)
		if e != nil {
			return "", errDesignAccess
		}
		var caps sql.NullString
		if tx.QueryRowContext(ctx, `SELECT capabilities FROM workspace_members WHERE workspace_id=? AND user_id=?`, k.WorkspaceID, user.ID).Scan(&caps) != nil {
			return "", errDesignAccess
		}
		if !canRole(role, "create") && !HasCapability(resolveCapabilitiesFromRow(caps, role), CapabilityPageCreate) {
			return "", errDesignAccess
		}
		if staged {
			var unchanged bool
			if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pages WHERE id=? AND workspace_id=? AND owner_user_id=? AND owner_crew_id IS NULL AND folder_id IS NULL AND slug=? AND name=? AND spec_json=? AND created_by_test=?)`, k.PageID, k.WorkspaceID, user.ID, p.Slug, p.Name, string(raw), p.CreatedByTest).Scan(&unchanged); e != nil {
				return "", e
			}
			if !unchanged {
				return "", errDesignAccess
			}
			_, e = tx.ExecContext(ctx, `INSERT INTO artifact_design_documents(workspace_id,page_id,state,updated_by,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(workspace_id,page_id) DO NOTHING`, k.WorkspaceID, k.PageID, result.State, user.ID, tsformat.Format(time.Now()))
			return user.ID, e
		}
		var n int
		if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM pages WHERE workspace_id=?`, k.WorkspaceID).Scan(&n); e != nil {
			return "", e
		}
		if n >= pages.MaxPagesPerWorkspace {
			return "", artifacts.ErrVersionQuota
		}
		stamp := tsformat.Format(time.Now())
		if _, e = tx.ExecContext(ctx, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,created_at,updated_at,private_audience,created_by_test) VALUES(?,?,?,?,?,?,?,?,0,?)`, k.PageID, k.WorkspaceID, p.Slug, p.Name, user.ID, string(raw), stamp, stamp, p.CreatedByTest); e != nil {
			if isUniqueViolation(e) {
				return "", artifacts.ErrVersionConflict
			}
			return "", e
		}
		if e = insertPageVersion(ctx, tx, k.PageID, 1, string(raw), user.ID, stamp); e != nil {
			return "", e
		}
		staged = true
		return user.ID, nil
	}
	receipt, err := h.designStore(r.Context()).Apply(r.Context(), k, designVersionRequest(result.State, 0, "Create Design"), auth)
	if err != nil {
		designError(w, err)
		return
	}
	writeJSON(w, 201, designCreateResponse{PageID: k.PageID, Slug: p.Slug, Version: receipt.Version})
}
func (h *PageHandler) GetDesign(w http.ResponseWriter, r *http.Request) {
	k, ok := h.designKey(w, r)
	if !ok {
		return
	}
	room := h.designRoom(k)
	room.mu.Lock()
	defer room.mu.Unlock()
	var result design.Result
	var version, shared int64
	canEdit := false
	canComment := false
	actor := ""
	displayName := ""
	authorityRevision := ""
	var origin *designOriginResponse
	var selected int64
	if value := r.URL.Query().Get("version"); value != "" {
		var e error
		selected, e = strconv.ParseInt(value, 10, 64)
		if e != nil || selected < 1 {
			designError(w, artifacts.ErrVersionInvalid)
			return
		}
	}
	err := h.designTx(r, k, false, func(ctx context.Context, tx *sql.Tx, a string) error {
		actor = a
		var epochErr error
		authorityRevision, epochErr = designAuthorityRevision(ctx, tx, k.WorkspaceID)
		if epochErr != nil {
			return epochErr
		}
		if e := tx.QueryRowContext(ctx, `SELECT coalesce(nullif(full_name,''),email,id) FROM users WHERE id=?`, UserFromContext(ctx).ID).Scan(&displayName); e != nil {
			return e
		}
		var state []byte
		if e := tx.QueryRowContext(ctx, `SELECT state FROM artifact_design_documents WHERE workspace_id=? AND page_id=?`, k.WorkspaceID, k.PageID).Scan(&state); e != nil {
			return e
		}
		if e := tx.QueryRowContext(ctx, `SELECT latest_version,shared_version FROM artifact_version_heads WHERE workspace_id=? AND page_id=?`, k.WorkspaceID, k.PageID).Scan(&version, &shared); e != nil {
			return e
		}
		_, e := h.designAuthority(r, k, true)(ctx, tx)
		canEdit = e == nil
		_, commentErr := h.designCommentAuthority(r, k)(ctx, tx)
		canComment = commentErr == nil
		if selected > 0 {
			if !canEdit && selected != shared {
				return artifacts.ErrVersionNotFound
			}
			var raw string
			if e = tx.QueryRowContext(ctx, `SELECT manifest_json FROM artifact_versions WHERE workspace_id=? AND page_id=? AND version=?`, k.WorkspaceID, k.PageID, selected).Scan(&raw); e != nil {
				return e
			}
			var m struct {
				Files map[string]struct {
					Hash string `json:"hash"`
				} `json:"files"`
			}
			if e = json.Unmarshal([]byte(raw), &m); e != nil {
				return e
			}
			if e = tx.QueryRowContext(ctx, `SELECT content FROM artifact_version_blobs WHERE workspace_id=? AND page_id=? AND hash=?`, k.WorkspaceID, k.PageID, m.Files["design.yjs"].Hash).Scan(&state); e != nil {
				return e
			}
			version = selected
		}
		if canEdit {
			var chat, agent sql.NullString
			e = tx.QueryRowContext(ctx, `SELECT chat_id,agent_id FROM artifact_project_origins WHERE workspace_id=? AND page_id=? AND human_user_id=?`, k.WorkspaceID, k.PageID, a).Scan(&chat, &agent)
			if e == nil && chat.Valid && agent.Valid {
				origin = &designOriginResponse{ChatID: chat.String, AgentID: agent.String}
			}
		}
		var err error
		result, err = design.Run(ctx, design.Request{State: state})
		return err
	})
	if err != nil {
		designError(w, err)
		return
	}
	writeJSON(w, 200, designSnapshotResponse{AuthorityRevision: authorityRevision, PageID: k.PageID, Origin: origin, CanComment: canComment, Nodes: result.Nodes, Order: result.Order, CanEdit: canEdit, Version: version, LatestVersion: version, SharedVersion: shared, Actor: designActorResponse{ID: actor, Name: displayName}})
}
func (h *PageHandler) designApplyLocked(r *http.Request, k artifacts.VersionKey, input design.Request) (design.Result, error) {
	var result design.Result
	err := h.designTx(r, k, true, func(ctx context.Context, tx *sql.Tx, actor string) error {
		if e := tx.QueryRowContext(ctx, `SELECT state FROM artifact_design_documents WHERE workspace_id=? AND page_id=?`, k.WorkspaceID, k.PageID).Scan(&input.State); e != nil {
			return e
		}
		var e error
		result, e = design.Run(ctx, input)
		if e != nil {
			return e
		}
		if bytes.Equal(input.State, result.State) {
			return nil
		}
		_, e = tx.ExecContext(ctx, `UPDATE artifact_design_documents SET state=?,revision=revision+1,updated_by=?,updated_at=? WHERE workspace_id=? AND page_id=?`, result.State, actor, tsformat.Format(time.Now()), k.WorkspaceID, k.PageID)
		return e
	})
	return result, err
}
func (h *PageHandler) ApplyDesign(w http.ResponseWriter, r *http.Request) {
	k, ok := h.designKey(w, r)
	if !ok {
		return
	}
	var p struct {
		Operations json.RawMessage `json:"operations"`
		Message    string          `json:"message,omitempty"`
	}
	if !designDecode(w, r, &p, 4<<20) {
		return
	}
	if len(p.Operations) == 0 {
		designError(w, artifacts.ErrVersionInvalid)
		return
	}
	room := h.designRoom(k)
	room.mu.Lock()
	defer room.mu.Unlock()
	result, receipt, err := h.designVersionedMutationLocked(r, k, design.Request{Operations: p.Operations}, p.Message)
	if err != nil {
		designError(w, err)
		return
	}
	room.broadcast(designUpdateFrame(result.Update), nil)
	if err = h.designCompleteTask(r, k, receipt.Version); err != nil {
		designError(w, err)
		return
	}
	writeJSON(w, 200, designOperationsResponse{Nodes: result.Nodes, Order: result.Order, Version: receipt.Version})
}
func (h *PageHandler) designSnapshotLocked(r *http.Request, k artifacts.VersionKey, message string) (artifacts.VersionReceipt, error) {
	var state []byte
	var base, revision int64
	if err := h.designTx(r, k, true, func(ctx context.Context, tx *sql.Tx, _ string) error {
		return tx.QueryRowContext(ctx, `SELECT d.state,h.latest_version,d.revision FROM artifact_design_documents d JOIN artifact_version_heads h ON h.workspace_id=d.workspace_id AND h.page_id=d.page_id WHERE d.workspace_id=? AND d.page_id=?`, k.WorkspaceID, k.PageID).Scan(&state, &base, &revision)
	}); err != nil {
		return artifacts.VersionReceipt{}, err
	}
	receipt, err := h.designStore(r.Context()).Apply(r.Context(), k, designVersionRequest(state, base, message), h.designAuthority(r, k, true))
	if err != nil {
		return receipt, err
	}
	_, err = h.db.ExecContext(r.Context(), `UPDATE artifact_design_documents SET saved_revision=? WHERE workspace_id=? AND page_id=? AND revision=?`, revision, k.WorkspaceID, k.PageID, revision)
	return receipt, err
}
func (h *PageHandler) SnapshotDesign(w http.ResponseWriter, r *http.Request) {
	k, ok := h.designKey(w, r)
	if !ok {
		return
	}
	var p struct {
		Message string `json:"message,omitempty"`
	}
	if !designDecode(w, r, &p, 8192) {
		return
	}
	room := h.designRoom(k)
	room.mu.Lock()
	defer room.mu.Unlock()
	receipt, err := h.designSnapshotLocked(r, k, p.Message)
	if err != nil {
		designError(w, err)
		return
	}
	writeJSON(w, 200, receipt)
}
func (h *PageHandler) DesignVersions(w http.ResponseWriter, r *http.Request) {
	k, ok := h.designKey(w, r)
	if !ok {
		return
	}
	readAuth := h.designAuthority(r, k, false)
	view, err := h.designStore(r.Context()).View(r.Context(), k, func(ctx context.Context, tx *sql.Tx) (string, artifacts.VersionSelection, error) {
		actor, e := readAuth(ctx, tx)
		if e != nil {
			return "", artifacts.VersionSelection{}, e
		}
		_, writeErr := h.designAuthority(r, k, true)(ctx, tx)
		return actor, artifacts.VersionSelection{SharedOnly: writeErr != nil, IncludeHistory: writeErr == nil}, nil
	})
	if err != nil {
		designError(w, err)
		return
	}
	writeJSON(w, 200, designVersionListResponse{Versions: view.Versions, LatestVersion: view.State.LatestVersion, SharedVersion: view.State.SharedVersion})
}

func (h *PageHandler) RestoreDesign(w http.ResponseWriter, r *http.Request) {
	k, ok := h.designKey(w, r)
	if !ok {
		return
	}
	var p struct {
		Version int64 `json:"version"`
	}
	if !designDecode(w, r, &p, 8192) {
		return
	}
	room := h.designRoom(k)
	room.mu.Lock()
	defer room.mu.Unlock()
	manifest, err := h.designStore(r.Context()).ReadVersion(r.Context(), k, p.Version, h.designAuthority(r, k, true))
	if err != nil {
		designError(w, err)
		return
	}
	target, ok := manifest.Files["design.yjs"]
	if !ok {
		designError(w, artifacts.ErrVersionInvalid)
		return
	}
	result, receipt, err := h.designVersionedMutationLocked(r, k, design.Request{Replace: target.Content}, "Restore Design version")
	if err != nil {
		designError(w, err)
		return
	}
	room.broadcast(designUpdateFrame(result.Update), nil)
	writeJSON(w, 200, receipt)
}

// REST mutations and restores publish their normal version and collaborative
// head in one VersionStore transaction. A quota or authorization failure leaves
// both previous heads untouched; connected browsers see only committed results.
func (h *PageHandler) designVersionedMutationLocked(r *http.Request, k artifacts.VersionKey, input design.Request, message string) (design.Result, artifacts.VersionReceipt, error) {
	var state []byte
	var base, revision int64
	err := h.designTx(r, k, true, func(ctx context.Context, tx *sql.Tx, _ string) error {
		return tx.QueryRowContext(ctx, `SELECT d.state,d.revision,h.latest_version FROM artifact_design_documents d JOIN artifact_version_heads h ON h.workspace_id=d.workspace_id AND h.page_id=d.page_id WHERE d.workspace_id=? AND d.page_id=?`, k.WorkspaceID, k.PageID).Scan(&state, &revision, &base)
	})
	if err != nil {
		return design.Result{}, artifacts.VersionReceipt{}, err
	}
	input.State = state
	result, err := design.Run(r.Context(), input)
	if err != nil {
		return result, artifacts.VersionReceipt{}, err
	}
	calls := 0
	editAuth := h.designAuthority(r, k, true)
	auth := func(ctx context.Context, tx *sql.Tx) (string, error) {
		actor, e := editAuth(ctx, tx)
		if e != nil {
			return "", e
		}
		var current int64
		if e = tx.QueryRowContext(ctx, `SELECT revision FROM artifact_design_documents WHERE workspace_id=? AND page_id=?`, k.WorkspaceID, k.PageID).Scan(&current); e != nil {
			return "", e
		}
		if current != revision {
			return "", artifacts.ErrVersionConflict
		}
		calls++
		if calls == 2 {
			_, e = tx.ExecContext(ctx, `UPDATE artifact_design_documents SET state=?,revision=revision+1,saved_revision=revision+1,updated_by=?,updated_at=? WHERE workspace_id=? AND page_id=? AND revision=?`, result.State, actor, tsformat.Format(time.Now()), k.WorkspaceID, k.PageID, revision)
			if e != nil {
				return "", e
			}
		}
		return actor, nil
	}
	receipt, err := h.designStore(r.Context()).Apply(r.Context(), k, designVersionRequest(result.State, base, message), auth)
	return result, receipt, err
}
