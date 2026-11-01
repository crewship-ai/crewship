package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/design"
)

// designExportMaxBytes bounds one exported HTML file. A Design state is at
// most 4 MiB, so ordinary exports stay well below it; the bound still refuses
// a file the browser and CLI would not be able to handle safely.
var designExportMaxBytes = 8 << 20

var errDesignExportRequest = errors.New("invalid Design export request")

var designExportSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// designHistoryReader is the canonical Design read rule for retained versions:
// a person with current Edit access (owner, Edit grant, admitted agent run) or
// a workspace manager reads any retained version; everyone else whom the Page
// read check admits sees only the shared version. GetDesign, DesignVersions
// and ExportDesign all decide through this one predicate.
func (h *PageHandler) designHistoryReader(ctx context.Context, tx *sql.Tx, r *http.Request, k artifacts.VersionKey) (canEdit, history bool, err error) {
	_, writeErr := h.designAuthority(r, k, true)(ctx, tx)
	if writeErr == nil {
		return true, true, nil
	}
	history, err = h.designManagerRead(ctx, tx, k)
	return false, history, err
}

// designReadableVersion maps a requested version (0 = default) to the version
// this caller may read. Shared-only callers get the shared pin or not-found,
// so an unshared version is indistinguishable from a missing one.
func designReadableVersion(requested, latest, shared int64, history bool) (int64, error) {
	if !history {
		if shared < 1 || requested > 0 && requested != shared {
			return 0, artifacts.ErrVersionNotFound
		}
		return shared, nil
	}
	if requested > 0 {
		return requested, nil
	}
	return latest, nil
}

// designVersionState reads the immutable design.yjs of one retained version.
func designVersionState(ctx context.Context, tx *sql.Tx, k artifacts.VersionKey, version int64) ([]byte, error) {
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT manifest_json FROM artifact_versions WHERE workspace_id=? AND page_id=? AND version=?`, k.WorkspaceID, k.PageID, version).Scan(&raw); err != nil {
		return nil, err
	}
	var m struct {
		Files map[string]struct {
			Hash string `json:"hash"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, err
	}
	var state []byte
	err := tx.QueryRowContext(ctx, `SELECT content FROM artifact_version_blobs WHERE workspace_id=? AND page_id=? AND hash=?`, k.WorkspaceID, k.PageID, m.Files["design.yjs"].Hash).Scan(&state)
	return state, err
}

// ExportDesign renders one immutable Design version as a self-contained HTML
// file (#4449). It never reads the live document: without `version` an editor
// gets the latest retained version and a shared-only reader the shared one.
// Authorization, the Design flag and the not-found shape are those of
// GetDesign; a workspace manager's foreign export is audited as "export".
func (h *PageHandler) ExportDesign(w http.ResponseWriter, r *http.Request) {
	k, r, ok := h.designKey(w, newDesignAdminAuditRequest(r, "export"))
	if !ok {
		return
	}
	// Parameters are judged only after the canonical read check admitted the
	// caller, so a malformed request cannot tell an invisible Design apart
	// from a missing one.
	var requested int64
	var paramErr error
	query := r.URL.Query()
	for key, values := range query {
		if key != "format" && key != "version" && key != "workspace_id" || len(values) != 1 {
			paramErr = errDesignExportRequest
		}
	}
	if format := query.Get("format"); format != "" && format != "html" {
		paramErr = errDesignExportRequest
	}
	if value := query.Get("version"); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed < 1 || strconv.FormatInt(parsed, 10) != value {
			paramErr = errDesignExportRequest
		}
		requested = parsed
	}
	var output []byte
	var version int64
	var slug string
	err := h.designTx(r, k, false, func(ctx context.Context, tx *sql.Tx, _ string) error {
		if paramErr != nil {
			return paramErr
		}
		var latest, shared int64
		var name string
		if e := tx.QueryRowContext(ctx, `SELECT h.latest_version,h.shared_version,p.slug,p.name FROM artifact_version_heads h JOIN pages p ON p.workspace_id=h.workspace_id AND p.id=h.page_id WHERE h.workspace_id=? AND h.page_id=? AND h.content_type='design'`, k.WorkspaceID, k.PageID).Scan(&latest, &shared, &slug, &name); e != nil {
			return e
		}
		_, history, e := h.designHistoryReader(ctx, tx, r, k)
		if e != nil {
			return e
		}
		if version, e = designReadableVersion(requested, latest, shared, history); e != nil {
			return e
		}
		state, e := designVersionState(ctx, tx, k, version)
		if e != nil {
			return e
		}
		result, e := design.Run(ctx, design.Request{State: state})
		if e != nil {
			return e
		}
		output, e = design.RenderHTML(design.ExportInput{Name: name, Version: version, Nodes: result.Nodes, Order: result.Order}, designExportMaxBytes)
		return e
	})
	if errors.Is(err, errDesignExportRequest) {
		replyError(w, 400, "invalid Design export request: use format=html and an optional positive version")
		return
	}
	if errors.Is(err, design.ErrExportTooLarge) {
		writeJSON(w, 413, map[string]string{"error": fmt.Sprintf("Design export exceeds the %d MiB file limit. Reduce embedded images or split the Design.", designExportMaxBytes>>20), "code": "design_export_limit"})
		return
	}
	if err != nil {
		designError(w, err)
		return
	}
	notifyDesignAdminAudit(r)
	if !designExportSlug.MatchString(slug) {
		slug = "design"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-design-v%d.html"`, slug, version))
	// The file is inert when opened from this origin as well as from disk.
	w.Header().Set("Content-Security-Policy", design.ExportCSP+"; sandbox")
	w.Header().Set("X-Crewship-Design-Version", strconv.FormatInt(version, 10))
	w.Header().Set("Content-Length", strconv.Itoa(len(output)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(output)
}
