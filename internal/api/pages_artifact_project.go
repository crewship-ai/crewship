package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/featureflags"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pages"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

const journalArtifactProjectShared journal.EntryType = "artifact.project_shared"

var errArtifactProjectAccess = errors.New("artifact project access denied")
var errArtifactProjectAnchor = errors.New("artifact anchor no longer matches")

type artifactProjectFile struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	MediaType string `json:"media_type"`
}

// Presence is significant: JSON null or a missing content field is not an
// intentional empty file. The outer decoder also rejects duplicate fields.
func (f *artifactProjectFile) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := decodeArtifactProjectJSONBytes(raw, &fields); err != nil {
		return err
	}
	if len(fields) != 3 || fields["path"] == nil || fields["content"] == nil || fields["media_type"] == nil {
		return fmt.Errorf("file requires path, content and media_type strings")
	}
	type wire artifactProjectFile
	var value wire
	if err := decodeArtifactProjectJSONBytes(raw, &value); err != nil {
		return err
	}
	*f = artifactProjectFile(value)
	return nil
}

type artifactProjectPublish struct {
	BaseVersion *int64                `json:"base_version"`
	EntryPoint  string                `json:"entrypoint"`
	Files       []artifactProjectFile `json:"files"`
	Message     string                `json:"message"`
}
type artifactProjectCreate struct {
	Slug        string                `json:"slug"`
	Name        string                `json:"name"`
	BaseVersion *int64                `json:"base_version,omitempty"`
	EntryPoint  string                `json:"entrypoint"`
	Files       []artifactProjectFile `json:"files"`
	Message     string                `json:"message"`
}

func artifactProjectsEnabled() bool { return os.Getenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED") == "true" }
func (h *PageHandler) artifactProjectEnabled(ctx context.Context) bool {
	if artifactProjectsEnabled() {
		return true
	}
	enabled, err := featureflags.IsEnabledWith(ctx, h.pageAuthReader(ctx), WorkspaceIDFromContext(ctx), "artifact_projects")
	return err == nil && enabled
}
func (h *PageHandler) ArtifactProjectAvailability(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	if validateContentHumanCredential(r, h.pageAuthReader(r.Context()), "pages:read") != nil {
		replyError(w, http.StatusForbidden, "current artifact project read authority required")
		return
	}
	writeJSON(w, 200, artifactProjectAvailabilityResponse{Enabled: h.artifactProjectEnabled(r.Context())})
}
func (h *PageHandler) artifactProjectStore(ctx context.Context) *artifacts.VersionStore {
	return artifacts.NewVersionStore(h.db, artifacts.VersionOptions{Enabled: h.artifactProjectEnabled(ctx)})
}

func artifactProjectDecode(w http.ResponseWriter, r *http.Request, v any) bool {
	return decodeArtifactProjectToolJSON(w, r, v, 36<<20)
}

func artifactProjectError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, artifacts.ErrDisabled):
		replyError(w, 404, "artifact projects are not enabled")
	case errors.Is(err, artifacts.ErrVersionDurability):
		replyError(w, 503, "durable artifact storage is unavailable; keep your draft and contact the operator")
	case errors.Is(err, errArtifactProjectAccess), errors.Is(err, artifacts.ErrVersionAuthority):
		replyError(w, 403, "current artifact project write authority required")
	case errors.Is(err, artifacts.ErrVersionNotFound), errors.Is(err, artifacts.ErrVersionContentType), errors.Is(err, sql.ErrNoRows):
		replyError(w, 404, "artifact project or selected version not found")
	case errors.Is(err, errArtifactProjectAnchor):
		writeJSON(w, 409, map[string]any{"error": "The selected element no longer matches this version. Select it again using a stable element ID or ask the agent for clarification. Your comment draft is preserved.", "code": "artifact_anchor_mismatch"})
	case errors.Is(err, artifacts.ErrVersionConflict):
		writeJSON(w, 409, map[string]any{"error": "This project changed. Read the current version and compare your changes before publishing again. Your draft has not been discarded.", "code": "artifact_version_conflict"})
	case errors.Is(err, artifacts.ErrVersionQuota):
		writeJSON(w, 413, map[string]any{"error": "Project history quota is full. Existing versions are retained. Keep your local draft and ask for more storage or save a copy.", "code": "artifact_history_quota"})
	case errors.Is(err, artifacts.ErrVersionInvalid):
		replyError(w, 400, err.Error())
	default:
		replyError(w, 500, "artifact project operation failed; changes are not acknowledged")
	}
}
func (h *PageHandler) artifactProjectStart(w http.ResponseWriter, r *http.Request) (artifacts.VersionKey, bool) {
	artifactCommentHeaders(w)
	k := artifacts.VersionKey{WorkspaceID: WorkspaceIDFromContext(r.Context())}
	if !h.artifactProjectEnabled(r.Context()) {
		artifactProjectError(w, artifacts.ErrDisabled)
		return k, false
	}
	ref := r.PathValue("slug")
	column := "slug"
	if strings.HasPrefix(ref, "@") {
		column = "id"
		ref = strings.TrimPrefix(ref, "@")
	}
	if h.db.QueryRowContext(r.Context(), "SELECT id FROM pages WHERE workspace_id=? AND "+column+"=?", k.WorkspaceID, ref).Scan(&k.PageID) != nil {
		artifactProjectError(w, artifacts.ErrVersionNotFound)
		return k, false
	}
	return k, true
}

// Every path checks live credentials, membership and Page ACL under the store's
// writer fence. A retained chat principal alone never grants a Page mutation.
func (h *PageHandler) artifactProjectAuthority(r *http.Request, k artifacts.VersionKey, write, share bool) artifacts.VersionAuthorize {
	return func(ctx context.Context, tx *sql.Tx) (string, error) {
		ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
		if !h.artifactProjectEnabled(ctx) {
			return "", artifacts.ErrDisabled
		}
		request := r.WithContext(ctx)
		var attached bool
		if tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artifact_version_heads WHERE workspace_id=? AND page_id=? AND content_type='artifact_project')`, k.WorkspaceID, k.PageID).Scan(&attached) != nil || !attached {
			return "", artifacts.ErrVersionNotFound
		}
		user := UserFromContext(ctx)
		if user == nil {
			return "", artifacts.ErrVersionNotFound
		}
		rec, err := h.loadPageForRead(ctx, k.WorkspaceID, "@"+k.PageID)
		if err != nil || rec.OwnerUserID == "" || rec.OwnerCrewID != "" || rec.FolderID != "" {
			return "", artifacts.ErrVersionNotFound
		}
		if run, _ := ctx.Value(artifactRunKey{}).(*artifactRunAuthority); run != nil {
			// The authenticated run may not enumerate other projects belonging to its
			// human. Recheck the retained Page binding in this same fenced transaction.
			bound, scopeErr := h.artifactProjectRunPage(ctx, run)
			if scopeErr != nil || !bound.Valid || bound.String != k.PageID {
				return "", artifacts.ErrVersionNotFound
			}
			if share || run.WorkspaceID != k.WorkspaceID || run.HumanUserID != rec.OwnerUserID || h.checkOriginalUserArtifactRunPrincipal(ctx, run) != nil || !h.artifactAgentPolicyAllows(ctx, k.WorkspaceID, run.CrewID) {
				return "", errArtifactProjectAccess
			}
			if h.checkArtifactProjectRunConsent(ctx, run, k.PageID) != nil {
				return "", errArtifactProjectAccess
			}
			return "agent:" + run.AgentID + ":" + run.RunID, nil
		}
		agent, _ := pageGrantCallerIsAgent(request)
		if agent || projectAgentFrom(ctx) != nil || InternalTokenWorkspaceFromContext(ctx) != "" {
			return "", artifacts.ErrVersionNotFound
		}
		scope := "pages:read"
		if write || share {
			scope = "pages:write"
		}
		if validateContentHumanCredential(request, tx, scope) != nil {
			return "", errArtifactProjectAccess
		}
		role, err := h.liveContentHumanRole(ctx, k.WorkspaceID, user.ID)
		if err != nil {
			return "", artifacts.ErrVersionNotFound
		}
		if rec.OwnerUserID == user.ID {
			return user.ID, nil
		}
		// A reader sees only the selected shared version. Private history stays with
		// its owner, even for an administrator or a holder of an ordinary write grant.
		if rec.PrivateAudience {
			return "", artifacts.ErrVersionNotFound
		}
		viewer, err := h.loadViewer(ctx, k.WorkspaceID, user.ID)
		if err != nil {
			return "", err
		}
		viewer.Role = role
		allowed, err := h.canSeePage(ctx, k.WorkspaceID, rec, nil, viewer)
		if err != nil || !allowed {
			return "", artifacts.ErrVersionNotFound
		}
		var pin int64
		if tx.QueryRowContext(ctx, `SELECT shared_version FROM artifact_version_heads WHERE workspace_id=? AND page_id=?`, k.WorkspaceID, k.PageID).Scan(&pin) != nil || pin == 0 {
			return "", artifacts.ErrVersionNotFound
		}
		// Only a reader entitled to this shared pin may learn that a write
		// was denied. An ungranted Page remains indistinguishable from absent.
		if write || share {
			return "", errArtifactProjectAccess
		}
		return user.ID, nil
	}
}
func artifactProjectRequest(p artifactProjectPublish) (artifacts.VersionRequest, error) {
	if p.BaseVersion == nil || *p.BaseVersion < 0 {
		return artifacts.VersionRequest{}, fmt.Errorf("%w: base_version required", artifacts.ErrVersionInvalid)
	}
	files := map[string]artifacts.VersionFile{}
	for _, f := range p.Files {
		if _, ok := files[f.Path]; ok {
			return artifacts.VersionRequest{}, fmt.Errorf("%w: duplicate file path", artifacts.ErrVersionInvalid)
		}
		if !utf8.ValidString(f.Content) {
			return artifacts.VersionRequest{}, artifacts.ErrVersionInvalid
		}
		files[f.Path] = artifacts.VersionFile{Content: []byte(f.Content), MediaType: f.MediaType}
	}
	return artifacts.VersionRequest{BaseVersion: *p.BaseVersion, EntryPoint: p.EntryPoint, Files: files, Message: p.Message}, nil
}
func (h *PageHandler) CreateArtifactProject(w http.ResponseWriter, r *http.Request) {
	artifactCommentHeaders(w)
	if !h.artifactProjectEnabled(r.Context()) {
		artifactProjectError(w, artifacts.ErrDisabled)
		return
	}
	var p artifactProjectCreate
	if !artifactProjectDecode(w, r, &p) {
		return
	}
	spec := pages.Document{APIVersion: pages.DocumentAPIVersion, Kind: pages.DocumentKind, Metadata: pages.Metadata{Slug: p.Slug, Name: p.Name}}
	if spec.ValidateEmpty() != nil || len(p.Name) > 200 || p.BaseVersion != nil && *p.BaseVersion != 0 {
		artifactProjectError(w, artifacts.ErrVersionInvalid)
		return
	}
	base := int64(0)
	req, err := artifactProjectRequest(artifactProjectPublish{&base, p.EntryPoint, p.Files, p.Message})
	if err != nil {
		artifactProjectError(w, err)
		return
	}
	user := UserFromContext(r.Context())
	if user == nil {
		artifactProjectError(w, errArtifactProjectAccess)
		return
	}
	k := artifacts.VersionKey{WorkspaceID: WorkspaceIDFromContext(r.Context()), PageID: generateCUID()}
	raw, _ := json.Marshal(spec)
	staged := false
	auth := func(ctx context.Context, tx *sql.Tx) (string, error) {
		ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
		if !h.artifactProjectEnabled(ctx) {
			return "", artifacts.ErrDisabled
		}
		r = r.WithContext(ctx)
		actor := user.ID
		if a, _ := ctx.Value(artifactRunKey{}).(*artifactRunAuthority); a != nil {
			if a.WorkspaceID != k.WorkspaceID || a.HumanUserID != user.ID || h.checkOriginalUserArtifactRunPrincipal(ctx, a) != nil || !h.artifactAgentPolicyAllows(ctx, a.WorkspaceID, a.CrewID) {
				return "", errArtifactProjectAccess
			}
			expectedPage := ""
			if staged {
				expectedPage = k.PageID
			}
			if h.checkArtifactProjectRunConsent(ctx, a, expectedPage) != nil {
				return "", errArtifactProjectAccess
			}
			actor = "agent:" + a.AgentID + ":" + a.RunID
		} else {
			agent, _ := pageGrantCallerIsAgent(r)
			if agent || projectAgentFrom(ctx) != nil || InternalTokenWorkspaceFromContext(ctx) != "" || validateContentHumanCredential(r, tx, "pages:write") != nil {
				return "", errArtifactProjectAccess
			}
		}
		role, e := h.liveContentHumanRole(ctx, k.WorkspaceID, user.ID)
		if e != nil {
			return "", errArtifactProjectAccess
		}
		var caps sql.NullString
		if tx.QueryRowContext(ctx, `SELECT capabilities FROM workspace_members WHERE workspace_id=? AND user_id=?`, k.WorkspaceID, user.ID).Scan(&caps) != nil {
			return "", errArtifactProjectAccess
		}
		if !canRole(role, "create") && !HasCapability(resolveCapabilitiesFromRow(caps, role), CapabilityPageCreate) {
			return "", errArtifactProjectAccess
		}
		var n int
		if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM pages WHERE workspace_id=?`, k.WorkspaceID).Scan(&n); e != nil {
			return "", e
		}
		if !staged && n >= pages.MaxPagesPerWorkspace {
			return "", artifacts.ErrVersionQuota
		}
		if staged {
			var valid int
			if tx.QueryRowContext(ctx, `SELECT count(*) FROM pages WHERE id=? AND workspace_id=? AND owner_user_id=? AND slug=? AND name=? AND spec_json=? AND private_audience=1 AND owner_crew_id IS NULL AND folder_id IS NULL`, k.PageID, k.WorkspaceID, user.ID, p.Slug, p.Name, string(raw)).Scan(&valid) != nil || valid != 1 {
				return "", errArtifactProjectAccess
			}
			return actor, nil
		}
		stamp := tsformat.Format(h.clock.Now())
		if _, e = tx.ExecContext(ctx, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,created_at,updated_at,private_audience) VALUES(?,?,?,?,?,?,?,?,1)`, k.PageID, k.WorkspaceID, p.Slug, p.Name, user.ID, string(raw), stamp, stamp); e != nil {
			if isUniqueViolation(e) {
				return "", artifacts.ErrVersionConflict
			}
			return "", e
		}
		if e = insertPageVersion(ctx, tx, k.PageID, 1, string(raw), user.ID, stamp); e != nil {
			return "", e
		}
		if a, _ := ctx.Value(artifactRunKey{}).(*artifactRunAuthority); a != nil {
			sealed, e := tx.ExecContext(ctx, `UPDATE chats SET private_artifact_author_user_id=? WHERE id=? AND workspace_id=? AND created_by=? AND visibility='private' AND (private_artifact_author_user_id IS NULL OR private_artifact_author_user_id=?)`, user.ID, a.ChatID, k.WorkspaceID, user.ID, user.ID)
			if e != nil {
				return "", e
			}
			if n, _ := sealed.RowsAffected(); n != 1 {
				return "", errArtifactProjectAccess
			}
			if _, e = tx.ExecContext(ctx, `INSERT INTO artifact_project_origins(page_id,workspace_id,human_user_id,chat_id,agent_id,message_id,created_at) VALUES(?,?,?,?,?,?,?)`, k.PageID, k.WorkspaceID, user.ID, a.ChatID, a.AgentID, a.MessageID, stamp); e != nil {
				return "", e
			}
			result, e := tx.ExecContext(ctx, `UPDATE artifact_project_run_consents SET result_page_id=? WHERE run_id=? AND result_page_id IS NULL AND revoked_at IS NULL`, k.PageID, a.RunID)
			if e != nil {
				return "", e
			}
			if n, _ := result.RowsAffected(); n != 1 {
				return "", errArtifactProjectAccess
			}
		}
		staged = true
		return actor, nil
	}
	receipt, err := h.artifactProjectStore(r.Context()).Apply(r.Context(), k, req, auth)
	if err != nil {
		artifactProjectError(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"page_id": k.PageID, "slug": p.Slug, "version": receipt.Version, "url": "/pages/" + p.Slug + "?artifact=1&workspace_id=" + k.WorkspaceID})
}
func (h *PageHandler) PublishArtifactProject(w http.ResponseWriter, r *http.Request) {
	k, ok := h.artifactProjectStart(w, r)
	if !ok {
		return
	}
	// Reject readers before decoding their payloads.
	if err := h.artifactProjectCheck(r, k, true); err != nil {
		artifactProjectError(w, err)
		return
	}
	var p artifactProjectPublish
	if !artifactProjectDecode(w, r, &p) {
		return
	}
	req, err := artifactProjectRequest(p)
	if err != nil {
		artifactProjectError(w, err)
		return
	}
	out, err := h.artifactProjectStore(r.Context()).Apply(r.Context(), k, req, h.artifactProjectMutationAuthority(r, k, req.BaseVersion))
	if err != nil {
		artifactProjectError(w, err)
		return
	}
	writeJSON(w, 200, h.artifactProjectReceipt(r, k, out))
}
func (h *PageHandler) artifactProjectCheck(r *http.Request, k artifacts.VersionKey, write bool) error {
	_, err := h.artifactProjectStore(r.Context()).Quota(r.Context(), k, h.artifactProjectAuthority(r, k, write, false))
	return err
}
func (h *PageHandler) RestoreArtifactProject(w http.ResponseWriter, r *http.Request) {
	k, ok := h.artifactProjectStart(w, r)
	if !ok {
		return
	}
	if err := h.artifactProjectCheck(r, k, true); err != nil {
		artifactProjectError(w, err)
		return
	}
	var p struct {
		BaseVersion *int64 `json:"base_version"`
		Version     int64  `json:"version"`
	}
	if !artifactProjectDecode(w, r, &p) {
		return
	}
	if p.BaseVersion == nil || p.Version < 1 {
		artifactProjectError(w, artifacts.ErrVersionInvalid)
		return
	}
	out, err := h.artifactProjectStore(r.Context()).Restore(r.Context(), k, *p.BaseVersion, p.Version, h.artifactProjectMutationAuthority(r, k, *p.BaseVersion))
	if err != nil {
		artifactProjectError(w, err)
		return
	}
	writeJSON(w, 200, h.artifactProjectReceipt(r, k, out))
}
func (h *PageHandler) ShareArtifactProject(w http.ResponseWriter, r *http.Request) {
	k, ok := h.artifactProjectStart(w, r)
	if !ok {
		return
	}
	if err := h.artifactProjectCheck(r, k, true); err != nil {
		artifactProjectError(w, err)
		return
	}
	var p struct {
		BaseVersion *int64 `json:"base_version"`
		Version     int64  `json:"version"`
	}
	if !artifactProjectDecode(w, r, &p) {
		return
	}
	if p.BaseVersion == nil || p.Version < 1 {
		artifactProjectError(w, artifacts.ErrVersionInvalid)
		return
	}
	auth := h.artifactProjectAuthority(r, k, true, true)
	sharedOnce := false
	var notify func()
	err := h.artifactProjectStore(r.Context()).Share(r.Context(), k, *p.BaseVersion, p.Version, func(ctx context.Context, tx *sql.Tx) (string, error) {
		actor, e := auth(ctx, tx)
		if e != nil {
			return "", e
		}
		if sharedOnce {
			return actor, nil
		}
		stamp := tsformat.Format(h.clock.Now())
		if _, e = tx.ExecContext(ctx, `INSERT INTO audit_logs(id,workspace_id,user_id,action,entity_type,entity_id,metadata,created_at) VALUES(?,?,?,?,?,?,?,?)`, generateCUID(), k.WorkspaceID, actor, "artifact.project_shared", "page", k.PageID, fmt.Sprintf(`{"version":%d}`, p.Version), stamp); e != nil {
			return "", e
		}
		notify, e = appendAuthorizationAudit(ctx, tx, h.journal, journal.Entry{
			WorkspaceID: k.WorkspaceID, Type: journalArtifactProjectShared, ActorType: journal.ActorUser, ActorID: actor,
			Summary: "Artifact project shared version selected", Payload: map[string]any{"page_id": k.PageID, "version": p.Version},
		})
		if e != nil {
			return "", e
		}
		_, e = tx.ExecContext(ctx, `UPDATE pages SET private_audience=0,updated_at=? WHERE id=? AND workspace_id=?`, stamp, k.PageID, k.WorkspaceID)
		if e == nil {
			sharedOnce = true
		}
		return actor, e
	})
	if err != nil {
		artifactProjectError(w, err)
		return
	}
	if notify != nil {
		notify()
	}
	writeJSON(w, 200, map[string]any{"shared_version": p.Version, "url": "/pages/" + r.PathValue("slug") + "?artifact=1&version=" + strconv.FormatInt(p.Version, 10) + "&workspace_id=" + k.WorkspaceID})
}
func artifactProjectManifest(m artifacts.VersionManifest) map[string]any {
	names := make([]string, 0, len(m.Files))
	for name := range m.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	files := make([]artifactProjectFile, 0, len(names))
	for _, name := range names {
		f := m.Files[name]
		files = append(files, artifactProjectFile{name, string(f.Content), f.MediaType})
	}
	return map[string]any{"version": m.Version, "entrypoint": m.EntryPoint, "files": files, "created_at": m.CreatedAt, "message": m.Message, "restored_from": m.RestoredFrom}
}
func (h *PageHandler) GetArtifactProject(w http.ResponseWriter, r *http.Request) {
	k, ok := h.artifactProjectStart(w, r)
	if !ok {
		return
	}
	var selected int64
	if v := r.URL.Query().Get("version"); v != "" {
		n, e := strconv.ParseInt(v, 10, 64)
		if e != nil || n < 1 {
			artifactProjectError(w, artifacts.ErrVersionInvalid)
			return
		}
		selected = n
	}
	auth := h.artifactProjectAuthority(r, k, false, false)
	owner := false
	canWrite := false
	var origin any
	var name, slug string
	view, err := h.artifactProjectStore(r.Context()).View(r.Context(), k, func(ctx context.Context, tx *sql.Tx) (string, artifacts.VersionSelection, error) {
		actor, e := auth(ctx, tx)
		if e != nil {
			return "", artifacts.VersionSelection{}, e
		}
		user := UserFromContext(ctx)
		if user == nil {
			return "", artifacts.VersionSelection{}, errArtifactProjectAccess
		}
		if e = tx.QueryRowContext(ctx, `SELECT owner_user_id=?,name,slug FROM pages WHERE id=? AND workspace_id=?`, user.ID, k.PageID, k.WorkspaceID).Scan(&owner, &name, &slug); e != nil {
			return "", artifacts.VersionSelection{}, e
		}
		// Private provenance uses the same fresh owner check and fenced transaction
		// as the version selection, including the final authorization recheck.
		origin = nil
		canWrite = false
		if owner {
			_, writeErr := h.artifactProjectAuthority(r, k, true, false)(ctx, tx)
			if writeErr != nil && !errors.Is(writeErr, errArtifactProjectAccess) {
				return "", artifacts.VersionSelection{}, writeErr
			}
			canWrite = writeErr == nil
			var chat, agent sql.NullString
			e = tx.QueryRowContext(ctx, `SELECT chat_id,agent_id FROM artifact_project_origins WHERE workspace_id=? AND page_id=?`, k.WorkspaceID, k.PageID).Scan(&chat, &agent)
			if e == nil && chat.Valid && agent.Valid {
				origin = map[string]string{"chat_id": chat.String, "agent_id": agent.String}
			}
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return "", artifacts.VersionSelection{}, e
			}
		}
		return actor, artifacts.VersionSelection{Version: selected, SharedOnly: !owner, IncludeHistory: owner}, nil
	})
	if err != nil {
		artifactProjectError(w, err)
		return
	}
	comments, err := h.artifactProjectComments(r, k, view.State.Manifest.Version, !owner)
	if err != nil {
		artifactProjectError(w, err)
		return
	}
	writeJSON(w, 200, artifactProjectSnapshotResponse{PageID: k.PageID, Name: name, Slug: slug, Origin: origin, Enabled: true, CanWrite: canWrite, LatestVersion: view.State.LatestVersion, SharedVersion: view.State.SharedVersion, Manifest: artifactProjectManifest(view.State.Manifest), Versions: artifactProjectVersionInfos(view.Versions), Quota: view.State.Quota, Comments: comments})
}

func (h *PageHandler) artifactProjectAttached(ctx context.Context, page string) (bool, error) {
	var found bool
	err := h.pageAuthReader(ctx).QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artifact_version_heads WHERE page_id=? AND content_type='artifact_project')`, page).Scan(&found)
	return found, err
}

// A task begins at the exact version the owner selected. Only this run's own
// successful writes advance its cursor; observing somebody else's new head
// cannot silently expand the original mandate.
func (h *PageHandler) artifactProjectMutationAuthority(r *http.Request, k artifacts.VersionKey, base int64) artifacts.VersionAuthorize {
	authority := h.artifactProjectAuthority(r, k, true, false)
	checked := false
	return func(ctx context.Context, tx *sql.Tx) (string, error) {
		actor, err := authority(ctx, tx)
		if err != nil {
			return "", err
		}
		run, _ := r.Context().Value(artifactRunKey{}).(*artifactRunAuthority)
		if run == nil {
			return actor, nil
		}
		var original int64
		var revoked sql.NullString
		err = tx.QueryRowContext(ctx, `SELECT base_version,revoked_at FROM artifact_project_message_intents WHERE run_id=? AND workspace_id=? AND (page_id=? OR page_id IS NULL)`, run.RunID, k.WorkspaceID, k.PageID).Scan(&original, &revoked)
		if errors.Is(err, sql.ErrNoRows) {
			return actor, nil
		}
		if err != nil {
			return "", err
		}
		if revoked.Valid {
			return "", errArtifactProjectAccess
		}
		if checked {
			return actor, nil
		}
		var expected int64
		if err = tx.QueryRowContext(ctx, `SELECT coalesce(max(version),?) FROM artifact_versions WHERE workspace_id=? AND page_id=? AND actor=?`, original, k.WorkspaceID, k.PageID, actor).Scan(&expected); err != nil {
			return "", err
		}
		if base != expected {
			return "", artifacts.ErrVersionConflict
		}
		checked = true
		return actor, nil
	}
}

func (h *PageHandler) artifactProjectReceipt(r *http.Request, k artifacts.VersionKey, receipt artifacts.VersionReceipt) artifactProjectMutationResponse {
	var slug string
	_ = h.db.QueryRowContext(r.Context(), `SELECT slug FROM pages WHERE id=? AND workspace_id=?`, k.PageID, k.WorkspaceID).Scan(&slug)
	return artifactProjectMutationResponse{PageID: k.PageID, Slug: slug, Version: receipt.Version, Actor: receipt.Actor, URL: "/pages/" + slug + "?artifact=1&workspace_id=" + k.WorkspaceID}
}
func artifactProjectVersionInfos(infos []artifacts.VersionInfo) []map[string]any {
	out := make([]map[string]any, 0, len(infos))
	for _, v := range infos {
		out = append(out, map[string]any{"version": v.Version, "entrypoint": v.EntryPoint, "message": v.Message, "actor": v.Actor, "created_at": v.CreatedAt, "restored_from": v.RestoredFrom})
	}
	return out
}
