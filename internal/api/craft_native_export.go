package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/craftsession"
	"github.com/crewship-ai/crewship/internal/mooring"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// auditActionCraftSheetExported records a Sheet handed out as a file, next to
// artifact.sheet_saved: who, which version, the format, size and digest prefix — never a value.
const auditActionCraftSheetExported = "artifact.sheet_exported"

const (
	// craftNativeExportDeadline bounds one conversion; the import takes the same order of time.
	craftNativeExportDeadline = 3 * time.Minute
	// craftNativeExportMaxActive engines run exports at once on this server; more answer 503.
	craftNativeExportMaxActive = 2
)

// exportXLSX is GET /api/v1/pages/{slug}/craft/document/export.xlsx[?version=N]: the Sheet's
// confirmed head (or version N, behind the version-history flag like the version read) as an
// xlsx written by the Sheet engine. It has exactly the document read's admission and authority
// (bind, the read's own authorization, the administrator read audit); the issue is audited as
// artifact.sheet_exported. Nothing is handed out unless the conversion and both audits succeeded.
func (d *craftNativeDocumentsHTTP) exportXLSX(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	r, entry, s, _, err := d.bind(r)
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	file, version, ok := d.exportSource(w, r, entry, s)
	if !ok {
		return
	}
	if entry.application != "gridcraft" {
		replyError(w, 422, "xlsx export is available for Sheets")
		return
	}
	if file.Format == "xlsx" || file.Format == "xlsm" {
		// A workbook Sheet's version is the file itself: hand it out as stored.
		d.sendExport(w, r, entry, version, file.Content, file.Format, file.MediaType)
		return
	}
	native := file.Content
	convert, err := d.exporter()
	if err != nil {
		craftNativeDocumentError(w, mooring.ErrNativeDisabled)
		return
	}
	im := d.importer()
	im.mu.Lock()
	if im.exporting >= craftNativeExportMaxActive {
		im.mu.Unlock()
		w.Header().Set("Retry-After", "30")
		replyError(w, 503, "too many exports are running; try again shortly")
		return
	}
	im.exporting++
	im.mu.Unlock()
	defer func() { im.mu.Lock(); im.exporting--; im.mu.Unlock() }()
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(craftNativeExportDeadline + time.Minute))
	ctx, cancel := context.WithTimeout(r.Context(), craftNativeExportDeadline)
	data, err := convert(ctx, native)
	cancel()
	if err != nil {
		if errors.Is(err, craftsession.ErrTooLarge) {
			replyError(w, 413, "the exported workbook is larger than the export limit")
			return
		}
		replyError(w, 500, "the workbook could not be exported")
		return
	}
	d.sendExport(w, r, entry, version, data, "xlsx", mooring.WorkbookMediaTypeXLSX)
}

// sendExport audits and sends an exported workbook. Nothing leaves without both audits.
func (d *craftNativeDocumentsHTTP) sendExport(w http.ResponseWriter, r *http.Request, entry *craftNativeSession, version int64, data []byte, ext, mediaType string) {
	if !d.auditRead(w, r, entry) {
		return
	}
	capture, _ := r.Context().Value(craftNativeReadCaptureKey{}).(*craftNativeReadCapture)
	source, err := craftNativeHuman(r.Context())
	if err != nil || capture == nil || capture.record == nil {
		craftNativeDocumentError(w, errCraftNativeAccess)
		return
	}
	if err = recordCraftSheetExported(r.Context(), d, entry, source.UserID, version, data); err != nil {
		replyError(w, 500, "the export could not be audited")
		return
	}
	h := w.Header()
	h.Set("Content-Type", mediaType)
	h.Set("Content-Disposition", craftExportDisposition(capture.record.Name, ext))
	h.Set("Content-Length", strconv.Itoa(len(data)))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Craft-Version", strconv.FormatInt(version, 10))
	w.WriteHeader(200)
	_, _ = w.Write(data)
}

// exportSource reads the native bytes the export converts: the head, or ?version=N with the
// version read's gate and answers (404 history off or version absent, 400 malformed).
func (d *craftNativeDocumentsHTTP) exportSource(w http.ResponseWriter, r *http.Request, entry *craftNativeSession, s *mooring.NativeStore) (mooring.NativeFile, int64, bool) {
	var m artifacts.VersionManifest
	if raw, asked := r.URL.Query()["version"]; asked {
		if !d.craftVersionHistoryReadable(w, r, entry) {
			return mooring.NativeFile{}, 0, false
		}
		v, err := craftNativeVersion(strings.Join(raw, ","))
		if err != nil {
			replyError(w, 400, "invalid version")
			return mooring.NativeFile{}, 0, false
		}
		got, _, err := s.ReadVersion(r.Context(), entry.scope, v)
		if errors.Is(err, artifacts.ErrVersionNotFound) {
			replyError(w, 404, "native version not found")
			return mooring.NativeFile{}, 0, false
		}
		if err != nil {
			craftNativeDocumentError(w, err)
			return mooring.NativeFile{}, 0, false
		}
		m = got
	} else {
		state, err := s.Read(r.Context(), entry.scope)
		if err != nil {
			craftNativeDocumentError(w, err)
			return mooring.NativeFile{}, 0, false
		}
		m = state.Manifest
		m.Version = state.LatestVersion
	}
	// A native version left from before workbook files is converted like any native Sheet.
	file, err := s.DocumentFile(m)
	if err != nil && !errors.Is(err, mooring.ErrNativeLegacy) {
		craftNativeDocumentError(w, err)
		return mooring.NativeFile{}, 0, false
	}
	return file, m.Version, true
}

// exporter is the one-shot Sheet engine conversion (the import's engine module).
func (d *craftNativeDocumentsHTTP) exporter() (func(context.Context, []byte) ([]byte, error), error) {
	im := d.importer()
	if im.export != nil {
		return im.export, nil
	}
	profile, ok := d.profile("gridcraft")
	if !ok {
		return nil, mooring.ErrNativeDisabled
	}
	launch, err := im.launch(profile)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, native []byte) ([]byte, error) {
		return craftsession.ExportXLSX(ctx, launch, im.tempRoot, native, 0)
	}, nil
}

// faithfulExporter is exporter with the engine's fidelity check: a workbook that would lose parts
// (pivot fields, charts, …) is a *craftsession.FidelityLossError, never bytes.
func (d *craftNativeDocumentsHTTP) faithfulExporter() (func(context.Context, []byte) ([]byte, error), error) {
	im := d.importer()
	if im.exportFaithful != nil {
		return im.exportFaithful, nil
	}
	profile, ok := d.profile("gridcraft")
	if !ok {
		return nil, mooring.ErrNativeDisabled
	}
	launch, err := im.launch(profile)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, native []byte) ([]byte, error) {
		return craftsession.ExportXLSXFaithful(ctx, launch, im.tempRoot, native, mooring.MaxWorkbookBytes)
	}, nil
}

func recordCraftSheetExported(ctx context.Context, d *craftNativeDocumentsHTTP, entry *craftNativeSession, userID string, version int64, data []byte) error {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	meta, err := json.Marshal(map[string]any{"version": strconv.FormatInt(version, 10), "format": "xlsx", "bytes": len(data), "sha256": hex.EncodeToString(sum[:8])})
	if err != nil {
		return err
	}
	_, err = d.human.pages.db.ExecContext(ctx, `INSERT INTO audit_logs(id,workspace_id,user_id,action,entity_type,entity_id,metadata,created_at) VALUES(?,?,?,?,'page',?,?,?)`,
		"sheet-exported-"+hex.EncodeToString(id[:]), entry.scope.WorkspaceID, userID, auditActionCraftSheetExported, entry.scope.ArtifactID, string(meta), tsformat.Format(time.Now().UTC()))
	return err
}

// craftExportDisposition names the download after the page: an ASCII filename with anything a
// header or a file system could misread replaced by "_", and the exact name as filename*.
func craftExportDisposition(name, ext string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "sheet"
	}
	if r := []rune(name); len(r) > 120 {
		name = string(r[:120])
	}
	ascii := strings.Map(func(c rune) rune {
		if c < 0x20 || c > 0x7e || strings.ContainsRune(`"\/:*?<>|;`, c) {
			return '_'
		}
		return c
	}, name)
	exact := strings.ReplaceAll(url.QueryEscape(name+"."+ext), "+", "%20")
	return `attachment; filename="` + ascii + "." + ext + `"; filename*=UTF-8''` + exact
}
