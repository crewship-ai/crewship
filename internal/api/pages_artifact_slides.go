package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/artifacts/slidespdf"
	"github.com/crewship-ai/crewship/internal/featureflags"
)

const artifactSlidesProjectionBytes = 16 << 20
const artifactSlidesMaxVersion = int64(9007199254740991)

var errArtifactSlidesLimit = errors.New("Slides response limit")

type artifactSlidesProcess func(context.Context, artifacts.ArtifactSlidesProjection) ([]byte, error)

type artifactSlidesSelection struct {
	actor   string
	owner   bool
	version int64
	source  [sha256.Size]byte
}

func (h *PageHandler) artifactSlidesEnabled(ctx context.Context) bool {
	if !h.artifactProjectEnabled(ctx) {
		return false
	}
	if os.Getenv("CREWSHIP_ARTIFACT_SLIDES_ENABLED") == "true" {
		return true
	}
	enabled, err := featureflags.IsEnabledWith(ctx, h.pageAuthReader(ctx), WorkspaceIDFromContext(ctx), "artifact_slides")
	return err == nil && enabled
}

// The closure is shared by both View transactions. It pins selection mode and
// actual source bytes, not just the actor identity. The writer fence makes its
// per-transaction source digest cache valid through View's final authorization.
func (h *PageHandler) artifactSlidesAuthority(r *http.Request, k artifacts.VersionKey, version int64) artifacts.VersionViewAuthorize {
	var selected artifactSlidesSelection
	checked := false
	var lastTx *sql.Tx
	var source [sha256.Size]byte
	return func(ctx context.Context, tx *sql.Tx) (string, artifacts.VersionSelection, error) {
		ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
		if !h.artifactSlidesEnabled(ctx) {
			return "", artifacts.VersionSelection{}, artifacts.ErrDisabled
		}
		actor, err := h.artifactProjectAuthority(r, k, false, false)(ctx, tx)
		if err != nil {
			return "", artifacts.VersionSelection{}, err
		}
		var owner string
		var shared int64
		err = tx.QueryRowContext(ctx, `SELECT p.owner_user_id,h.shared_version FROM pages p JOIN artifact_version_heads h ON h.workspace_id=p.workspace_id AND h.page_id=p.id WHERE p.workspace_id=? AND p.id=?`, k.WorkspaceID, k.PageID).Scan(&owner, &shared)
		if err != nil {
			return "", artifacts.VersionSelection{}, err
		}
		user := UserFromContext(ctx)
		isOwner := user != nil && user.ID == owner
		if run, _ := ctx.Value(artifactRunKey{}).(*artifactRunAuthority); run != nil {
			isOwner = run.HumanUserID == owner
		}
		if !isOwner && shared != version {
			return "", artifacts.VersionSelection{}, artifacts.ErrVersionNotFound
		}
		if lastTx != tx {
			source, err = artifactSlidesSourceDigest(ctx, tx, k, version)
			if err != nil {
				return "", artifacts.VersionSelection{}, err
			}
			lastTx = tx
		}
		current := artifactSlidesSelection{actor: actor, owner: isOwner, version: version, source: source}
		if checked && selected != current {
			return "", artifacts.VersionSelection{}, artifacts.ErrVersionAuthority
		}
		selected = current
		checked = true
		return actor, artifacts.VersionSelection{Version: version, SharedOnly: !isOwner}, nil
	}
}

// Bounds source material before View loads any file bodies. The digest also
// checks content-address integrity and detects lifecycle replacement with the
// same Page/version number between the two transactions.
func artifactSlidesSourceDigest(ctx context.Context, tx *sql.Tx, k artifacts.VersionKey, version int64) ([sha256.Size]byte, error) {
	var out [sha256.Size]byte
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(manifest_json AS BLOB))<=131072 THEN CAST(manifest_json AS BLOB) END FROM artifact_versions WHERE workspace_id=? AND page_id=? AND version=?`, k.WorkspaceID, k.PageID, version).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return out, artifacts.ErrVersionNotFound
	}
	if err != nil {
		return out, err
	}
	if !artifacts.ValidateSlidesArtifactManifest(raw) {
		return out, artifacts.ErrSlidesArtifactUnsupported
	}
	var count, total, largest, missing int64
	err = tx.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(length(b.content)),0),coalesce(max(length(b.content)),0),coalesce(sum(b.hash IS NULL),0) FROM artifact_versions v,json_each(v.manifest_json,'$.files') f LEFT JOIN artifact_version_blobs b ON b.workspace_id=v.workspace_id AND b.page_id=v.page_id AND b.hash=json_extract(f.value,'$.hash') WHERE v.workspace_id=? AND v.page_id=? AND v.version=?`, k.WorkspaceID, k.PageID, version).Scan(&count, &total, &largest, &missing)
	if err != nil {
		return out, err
	}
	if count < 2 || count > 101 || total > 2<<20 || largest > 256<<10 || missing != 0 {
		return out, artifacts.ErrSlidesArtifactUnsupported
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte("crewship-artifact-slides-source/v1\n"))
	_, _ = digest.Write(raw)
	rows, err := tx.QueryContext(ctx, `SELECT f.key,json_extract(f.value,'$.media_type'),json_extract(f.value,'$.hash'),b.content FROM artifact_versions v,json_each(v.manifest_json,'$.files') f JOIN artifact_version_blobs b ON b.workspace_id=v.workspace_id AND b.page_id=v.page_id AND b.hash=json_extract(f.value,'$.hash') WHERE v.workspace_id=? AND v.page_id=? AND v.version=? ORDER BY f.key`, k.WorkspaceID, k.PageID, version)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		var path, media, hash string
		var content []byte
		if err := rows.Scan(&path, &media, &hash, &content); err != nil {
			return out, err
		}
		sum := sha256.Sum256(content)
		if hex.EncodeToString(sum[:]) != hash {
			return out, artifacts.ErrSlidesArtifactUnsupported
		}
		for _, part := range [][]byte{[]byte(path), []byte(media), content} {
			var size [8]byte
			binary.BigEndian.PutUint64(size[:], uint64(len(part)))
			_, _ = digest.Write(size[:])
			_, _ = digest.Write(part)
		}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	copy(out[:], digest.Sum(nil))
	return out, nil
}

func (h *PageHandler) GetArtifactSlides(w http.ResponseWriter, r *http.Request) {
	h.serveArtifactSlides(w, r, false, func(_ context.Context, p artifacts.ArtifactSlidesProjection) ([]byte, error) { return json.Marshal(p) })
}
func (h *PageHandler) ExportArtifactSlidesPDF(w http.ResponseWriter, r *http.Request) {
	h.serveArtifactSlides(w, r, true, func(ctx context.Context, p artifacts.ArtifactSlidesProjection) ([]byte, error) {
		return slidespdf.Render(ctx, p.Deck)
	})
}

func artifactSlidesError(w http.ResponseWriter, err error) {
	artifactCommentHeaders(w)
	switch {
	case errors.Is(err, artifacts.ErrSlidesArtifactUnsupported), errors.Is(err, slidespdf.ErrUnsupported), errors.Is(err, slidespdf.ErrInvalidDeck):
		writeJSON(w, 422, map[string]string{"error": "This content is outside the supported Slides profile.", "code": "artifact_slides_unsupported"})
	case errors.Is(err, slidespdf.ErrOverflow):
		writeJSON(w, 422, map[string]string{"error": "Slide text does not fit its frame. Adjust the authored layout before exporting.", "code": "artifact_slides_overflow"})
	case errors.Is(err, slidespdf.ErrLimit), errors.Is(err, errArtifactSlidesLimit):
		writeJSON(w, 413, map[string]string{"error": "Slides export exceeds the bounded resource limit.", "code": "artifact_slides_limit"})
	default:
		artifactProjectError(w, err)
	}
}

func (h *PageHandler) serveArtifactSlides(w http.ResponseWriter, r *http.Request, pdf bool, process artifactSlidesProcess) {
	if !h.artifactSlidesEnabled(r.Context()) {
		artifactSlidesError(w, artifacts.ErrDisabled)
		return
	}
	query := r.URL.Query()
	versions := query["version"]
	var version int64
	var err error
	if len(versions) == 1 {
		version, err = strconv.ParseInt(versions[0], 10, 64)
	}
	if len(versions) != 1 || err != nil || version < 1 || version > artifactSlidesMaxVersion || strconv.FormatInt(version, 10) != versions[0] {
		artifactSlidesError(w, artifacts.ErrVersionInvalid)
		return
	}
	for key, values := range query {
		if key != "version" && key != "workspace_id" || len(values) != 1 {
			artifactSlidesError(w, artifacts.ErrVersionInvalid)
			return
		}
	}
	k := artifacts.VersionKey{WorkspaceID: WorkspaceIDFromContext(r.Context())}
	if workspace := query.Get("workspace_id"); workspace != "" && workspace != k.WorkspaceID {
		artifactSlidesError(w, artifacts.ErrVersionInvalid)
		return
	}
	// Resolve a reference without emitting response-specific headers. Every access
	// decision below uses the immutable Page ID in the fenced transactions.
	ref, column := r.PathValue("slug"), "slug"
	if strings.HasPrefix(ref, "@") {
		ref = strings.TrimPrefix(ref, "@")
		column = "id"
	}
	if err = h.db.QueryRowContext(r.Context(), "SELECT id FROM pages WHERE workspace_id=? AND "+column+"=?", k.WorkspaceID, ref).Scan(&k.PageID); err != nil {
		artifactSlidesError(w, artifacts.ErrVersionNotFound)
		return
	}
	auth := h.artifactSlidesAuthority(r, k, version)
	store := h.artifactProjectStore(r.Context())
	view, err := store.View(r.Context(), k, auth)
	if err != nil {
		artifactSlidesError(w, err)
		return
	}
	projection, err := artifacts.ParseSlidesArtifact(k.PageID, view.State.Manifest.Files)
	if err != nil {
		artifactSlidesError(w, err)
		return
	}
	data, err := process(r.Context(), projection)
	if err != nil {
		artifactSlidesError(w, err)
		return
	}
	limit := artifactSlidesProjectionBytes
	if pdf {
		limit = slidespdf.MaxOutputBytes
	}
	if len(data) == 0 || len(data) > limit {
		artifactSlidesError(w, errArtifactSlidesLimit)
		return
	}
	// Rendering and JSON encoding happen outside the writer transaction. No
	// bytes or PDF headers escape until current authority and source are checked.
	if _, err = store.View(r.Context(), k, auth); err != nil {
		artifactSlidesError(w, err)
		return
	}
	if r.Context().Err() != nil {
		return
	}
	artifactCommentHeaders(w)
	w.Header().Set("Content-Type", "application/json")
	if pdf {
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="artifact-%s-v%d.pdf"`, k.PageID, version))
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
