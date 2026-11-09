package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// Library publication and consumer pins (#5189).
//
// Publishing is a separate right from editing: only the library's owner, as a
// human (never an agent run), moves the publication pointer. Workspace owners
// and admins keep their audited read-only path and do not publish other
// people's libraries. Editors edit drafts.
//
// A consumer project pins the published design-system.json as a
// self-contained copy. Pinning needs consumer write authority and live read
// authority over the exact published version; reading the pin needs only
// consumer read authority, so losing access to the library never breaks the
// consumer but does block update and re-pin.
const (
	journalArtifactLibraryPublished journal.EntryType = "artifact.library_published"
	journalArtifactLibraryPinned    journal.EntryType = "artifact.library_pinned"
)

type artifactLibraryResponse struct {
	PublishedVersion int64  `json:"published_version"`
	PublishedBy      string `json:"published_by,omitempty"`
	PublishedAt      string `json:"published_at,omitempty"`
	CanPublish       bool   `json:"can_publish"`
}

type artifactLibraryPinResponse struct {
	artifacts.LibraryPin
	// Tokens is the pinned design-system.json, byte for byte.
	Tokens json.RawMessage `json:"tokens"`
}

// artifactLibraryPinResult answers a pin or update; Changed is false for a
// retry of the version already pinned.
type artifactLibraryPinResult struct {
	artifactLibraryPinResponse
	Changed bool `json:"changed"`
}

func artifactLibraryPinBody(p artifacts.LibraryPin) artifactLibraryPinResponse {
	return artifactLibraryPinResponse{LibraryPin: p, Tokens: json.RawMessage(p.Content)}
}

// rejectArtifactAgentActor refuses an agent-run actor ("agent:…"): publishing
// and pinning are human decisions.
func rejectArtifactAgentActor(authorize artifacts.VersionAuthorize) artifacts.VersionAuthorize {
	return func(ctx context.Context, tx *sql.Tx) (string, error) {
		actor, err := authorize(ctx, tx)
		if err != nil {
			return "", err
		}
		if strings.HasPrefix(actor, "agent:") {
			return "", errArtifactProjectAccess
		}
		return actor, nil
	}
}

// artifactLibraryPublisher admits only the library's human owner.
func (h *PageHandler) artifactLibraryPublisher(r *http.Request, k artifacts.VersionKey) artifacts.VersionAuthorize {
	return rejectArtifactAgentActor(h.artifactProjectAuthority(r, k, true, true))
}

// artifactLibraryVersionAuthority reads one library version with the coverage
// the caller holds in their own standing: the owner any version, an editor the
// latest, any other reader the shared version. The workspace owner/admin read
// path is deliberately excluded: a pin is a durable copy that every reader of
// the consumer sees, so an audited administrator read must never become one.
// Coverage is decided before the base authority runs, so a refused admin
// leaves no audit record. Every denial is ErrLibraryUnreadable, so a pin never
// tells apart an absent, private or merely unpublished library.
func (h *PageHandler) artifactLibraryVersionAuthority(r *http.Request, lib artifacts.VersionKey) artifacts.LibraryVersionAuthorize {
	r = ensureDesignAdminAuditRequest(r, false)
	base := h.artifactProjectAuthority(r, lib, false, false)
	return func(ctx context.Context, tx *sql.Tx, version int64) (string, error) {
		user := UserFromContext(ctx)
		if user == nil {
			return "", artifacts.ErrLibraryUnreadable
		}
		err := h.artifactLibraryCovers(ctx, tx, lib, user.ID, version)
		var actor string
		if err == nil {
			actor, err = base(ctx, tx)
		}
		if err == nil && (strings.HasPrefix(actor, "agent:") || actor != user.ID) {
			err = errArtifactProjectAccess
		}
		if errors.Is(err, errArtifactProjectAccess) || errors.Is(err, artifacts.ErrVersionNotFound) || errors.Is(err, sql.ErrNoRows) {
			return "", artifacts.ErrLibraryUnreadable
		}
		return actor, err
	}
}

// artifactLibraryCovers reports whether the caller's own (non-administrator)
// standing reads this exact library version. It never records an audit.
func (h *PageHandler) artifactLibraryCovers(ctx context.Context, tx *sql.Tx, lib artifacts.VersionKey, user string, version int64) error {
	readCtx := context.WithValue(ctx, pageAuthReaderKey{}, tx)
	rec, err := h.loadPageForRead(readCtx, lib.WorkspaceID, "@"+lib.PageID)
	if err != nil {
		return err
	}
	owner := rec.OwnerUserID == user
	viewer, err := h.loadViewer(readCtx, lib.WorkspaceID, user)
	if err != nil {
		return err
	}
	editor := !owner && h.artifactProjectHumanEditor(readCtx, lib, rec, viewer)
	var latest, shared int64
	if err = tx.QueryRowContext(ctx, `SELECT latest_version,shared_version FROM artifact_version_heads WHERE workspace_id=? AND page_id=?`, lib.WorkspaceID, lib.PageID).Scan(&latest, &shared); err != nil {
		return err
	}
	covered, err := h.artifactVersionCoveredWithoutAdmin(readCtx, lib, rec, user, owner, editor, version, latest, shared)
	if err != nil {
		return err
	}
	if !covered {
		return artifacts.ErrVersionNotFound
	}
	return nil
}

// artifactLibraryPinner is consumer write authority for a human, never an
// agent run, with the pin journal entry written once in the same transaction.
func (h *PageHandler) artifactLibraryPinner(r *http.Request, k artifacts.VersionKey, library string, notify *func()) artifacts.VersionAuthorize {
	write := rejectArtifactAgentActor(h.artifactProjectAuthority(r, k, true, false))
	recorded := false
	return func(ctx context.Context, tx *sql.Tx) (string, error) {
		actor, err := write(ctx, tx)
		if err != nil || recorded {
			return actor, err
		}
		*notify, err = appendAuthorizationAudit(ctx, tx, h.journal, journal.Entry{
			WorkspaceID: k.WorkspaceID, Type: journalArtifactLibraryPinned, ActorType: journal.ActorUser, ActorID: actor,
			Summary: "Library copy pinned in an artifact project", Payload: map[string]any{"page_id": k.PageID, "library_page_id": library},
		})
		if err != nil {
			return "", err
		}
		recorded = true
		return actor, nil
	}
}

func (h *PageHandler) artifactLibraryCanPublish(r *http.Request, k artifacts.VersionKey) bool {
	_, err := h.artifactProjectStore(r.Context()).Quota(r.Context(), k, h.artifactLibraryPublisher(r, k))
	return err == nil
}

// GetArtifactLibrary reports the library's published version to any reader.
func (h *PageHandler) GetArtifactLibrary(w http.ResponseWriter, r *http.Request) {
	k, ok := h.artifactProjectStart(w, r)
	if !ok {
		return
	}
	r = ensureDesignAdminAuditRequest(r, false)
	pub, err := h.artifactProjectStore(r.Context()).ReadLibraryPublication(r.Context(), k, h.artifactProjectAuthority(r, k, false, false))
	if err != nil {
		artifactProjectError(w, err)
		return
	}
	notifyDesignAdminAudit(r)
	writeJSON(w, 200, artifactLibraryResponse{pub.Version, pub.PublishedBy, pub.PublishedAt, h.artifactLibraryCanPublish(r, k)})
}

// PublishArtifactLibrary moves the publication pointer. Owner only.
func (h *PageHandler) PublishArtifactLibrary(w http.ResponseWriter, r *http.Request) {
	k, ok := h.artifactProjectStart(w, r)
	if !ok {
		return
	}
	var p struct {
		Version int64 `json:"version"`
	}
	if !artifactProjectDecode(w, r, &p) {
		return
	}
	if p.Version < 1 {
		artifactProjectError(w, fmt.Errorf("%w: version required", artifacts.ErrVersionInvalid))
		return
	}
	publisher := h.artifactLibraryPublisher(r, k)
	recorded := false
	var notify func()
	pub, err := h.artifactProjectStore(r.Context()).PublishLibrary(r.Context(), k, p.Version, func(ctx context.Context, tx *sql.Tx) (string, error) {
		actor, e := publisher(ctx, tx)
		if e != nil || recorded {
			return actor, e
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO audit_logs(id,workspace_id,user_id,action,entity_type,entity_id,metadata,created_at) VALUES(?,?,?,?,?,?,?,?)`, generateCUID(), k.WorkspaceID, actor, string(journalArtifactLibraryPublished), "page", k.PageID, fmt.Sprintf(`{"version":%d}`, p.Version), tsformat.Format(h.clock.Now())); e != nil {
			return "", e
		}
		notify, e = appendAuthorizationAudit(ctx, tx, h.journal, journal.Entry{
			WorkspaceID: k.WorkspaceID, Type: journalArtifactLibraryPublished, ActorType: journal.ActorUser, ActorID: actor,
			Summary: "Library version published", Payload: map[string]any{"page_id": k.PageID, "version": p.Version},
		})
		if e != nil {
			return "", e
		}
		recorded = true
		return actor, nil
	})
	if err != nil {
		artifactProjectError(w, err)
		return
	}
	if notify != nil {
		notify()
	}
	writeJSON(w, 200, artifactLibraryResponse{pub.Version, pub.PublishedBy, pub.PublishedAt, true})
}

// resolveArtifactLibrary maps a slug or @id inside the caller's workspace to a
// Page id. An unknown reference is the same 404 as an unreadable library.
func (h *PageHandler) resolveArtifactLibrary(ctx context.Context, workspace, ref string) (string, error) {
	column := "slug"
	if strings.HasPrefix(ref, "@") {
		column, ref = "id", strings.TrimPrefix(ref, "@")
	}
	if ref == "" {
		return "", fmt.Errorf("%w: library required", artifacts.ErrVersionInvalid)
	}
	var id string
	if err := h.db.QueryRowContext(ctx, "SELECT id FROM pages WHERE workspace_id=? AND "+column+"=?", workspace, ref).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", artifacts.ErrLibraryUnreadable
		}
		return "", err
	}
	return id, nil
}

// PutArtifactLibraryPin pins, or explicitly updates, the consumer's copy.
func (h *PageHandler) PutArtifactLibraryPin(w http.ResponseWriter, r *http.Request) {
	k, ok := h.artifactProjectStart(w, r)
	if !ok {
		return
	}
	var p struct {
		Library         string `json:"library"`
		Version         int64  `json:"version"`
		IfPinnedLibrary string `json:"if_pinned_library"`
		IfPinnedVersion *int64 `json:"if_pinned_version"`
	}
	if !artifactProjectDecode(w, r, &p) {
		return
	}
	if p.IfPinnedVersion == nil || *p.IfPinnedVersion < 0 || p.Version < 1 || (*p.IfPinnedVersion == 0) != (p.IfPinnedLibrary == "") {
		artifactProjectError(w, fmt.Errorf("%w: version (the reviewed published version) and if_pinned_version are required; if_pinned_library is required exactly when if_pinned_version is not 0", artifacts.ErrVersionInvalid))
		return
	}
	library, err := h.resolveArtifactLibrary(r.Context(), k.WorkspaceID, p.Library)
	if err != nil {
		artifactProjectError(w, err)
		return
	}
	// The reviewed pinned library is compared, not authorized: an @id is taken
	// as is (the copy may outlive its library) and an unknown slug is a stale
	// review.
	pinnedLibrary := strings.TrimPrefix(p.IfPinnedLibrary, "@")
	if p.IfPinnedLibrary != "" && !strings.HasPrefix(p.IfPinnedLibrary, "@") {
		if pinnedLibrary, err = h.resolveArtifactLibrary(r.Context(), k.WorkspaceID, p.IfPinnedLibrary); err != nil {
			artifactProjectError(w, artifacts.ErrVersionConflict)
			return
		}
	}
	r = ensureDesignAdminAuditRequest(r, false)
	var notify func()
	expect := artifacts.LibraryPinExpect{Version: p.Version, PinnedLibrary: pinnedLibrary, PinnedVersion: *p.IfPinnedVersion}
	pin, changed, err := h.artifactProjectStore(r.Context()).PinLibrary(r.Context(), k, library, expect, h.artifactLibraryPinner(r, k, library, &notify), h.artifactLibraryVersionAuthority(r, artifacts.VersionKey{WorkspaceID: k.WorkspaceID, PageID: library}))
	if err != nil {
		artifactProjectError(w, err)
		return
	}
	notifyDesignAdminAudit(r)
	if notify != nil {
		notify()
	}
	writeJSON(w, 200, artifactLibraryPinResult{artifactLibraryPinBody(pin), changed})
}

// GetArtifactLibraryPin returns the pinned copy; the library is not read.
func (h *PageHandler) GetArtifactLibraryPin(w http.ResponseWriter, r *http.Request) {
	k, ok := h.artifactProjectStart(w, r)
	if !ok {
		return
	}
	r = ensureDesignAdminAuditRequest(r, false)
	pin, err := h.artifactProjectStore(r.Context()).ReadLibraryPin(r.Context(), k, h.artifactProjectAuthority(r, k, false, false))
	if err != nil {
		artifactProjectError(w, err)
		return
	}
	notifyDesignAdminAudit(r)
	writeJSON(w, 200, artifactLibraryPinBody(pin))
}

// GetArtifactLibraryPinUpdate reviews whether the library published a
// different version than the pin, with a token diff summary.
func (h *PageHandler) GetArtifactLibraryPinUpdate(w http.ResponseWriter, r *http.Request) {
	k, ok := h.artifactProjectStart(w, r)
	if !ok {
		return
	}
	r = ensureDesignAdminAuditRequest(r, false)
	up, err := h.artifactProjectStore(r.Context()).CheckLibraryUpdate(r.Context(), k, h.artifactProjectAuthority(r, k, false, false), func(library string) artifacts.LibraryVersionAuthorize {
		return h.artifactLibraryVersionAuthority(r, artifacts.VersionKey{WorkspaceID: k.WorkspaceID, PageID: library})
	})
	if err != nil {
		artifactProjectError(w, err)
		return
	}
	notifyDesignAdminAudit(r)
	writeJSON(w, 200, up)
}
