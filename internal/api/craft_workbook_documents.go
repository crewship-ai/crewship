package api

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"github.com/crewship-ai/crewship/internal/tsformat"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/craftsession"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// craftWorkbookLimits bound the archive of a stored workbook. They protect the server from an
// archive that unpacks without end; what the editor can open is the editor's limit.
var craftWorkbookLimits = craftsession.ArchiveLimits{
	MaxEntries:      10_000,
	MaxWorksheets:   10_000,
	MaxPartBytes:    512 << 20,
	MaxTotalBytes:   512 << 20,
	MaxRatio:        200,
	MaxEntryRatio:   200,
	MaxCellsPerFile: 1 << 40,
	MaxPartElements: 1 << 40,
}

// craftWorkbookValidate is the workbook store's Validate: the archive check, without the engine.
// The VBA project is counted, never read.
func craftWorkbookValidate(_ context.Context, b []byte) error {
	_, err := craftsession.CheckArchive(b, craftWorkbookLimits)
	return err
}

func replyCode(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"error": message, "code": code})
}

// craftWorkbookError answers the refusals that name a workbook problem.
func craftWorkbookError(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, mooring.ErrWorkbookTooLarge):
		replyCode(w, 413, "too_large", "the workbook file is larger than 64 MiB")
	case errors.Is(err, craftsession.ErrTooLarge):
		replyCode(w, 413, "unpacked_too_large", "the workbook unpacks past the server's limit")
	case errors.Is(err, mooring.ErrWorkbookEncrypted):
		replyCode(w, 415, "encrypted", "the workbook is password protected")
	case errors.Is(err, mooring.ErrWorkbookUnsupported):
		replyCode(w, 415, "unsupported", "only xlsx and xlsm workbooks are kept")
	case errors.Is(err, mooring.ErrWorkbookNotWorkbook), errors.Is(err, craftsession.ErrNotXLSX):
		replyCode(w, 415, "not_workbook", "the file is not an xlsx or xlsm workbook")
	case errors.Is(err, mooring.ErrNativeLegacy):
		replyCode(w, 409, "legacy_native", "this version predates workbook files and cannot be used as the document")
	default:
		return false
	}
	return true
}

func craftContentHash(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// saveWorkbook publishes an already decoded JSON snapshot with the workbook
// audit and the touched ranges used by Undo.
func (d *craftNativeDocumentsHTTP) saveWorkbook(w http.ResponseWriter, r *http.Request, entry *craftNativeSession, s *mooring.NativeStore, lease mooring.EditLease, base int64, snapshot craftNativeSnapshotBody, source string) {
	ctx := r.Context()
	body := snapshot.Bytes
	if craftContentHash(body) != snapshot.ContentHash {
		replyError(w, 400, "the workbook bytes do not match contentHash")
		return
	}
	touched, touchedOK := craftWorkbookTouched(r.Header.Values("X-Craft-Touched"))
	if !touchedOK {
		replyError(w, 400, "invalid X-Craft-Touched")
		return
	}
	// Read the exact base, even for a retry after the head advanced. The
	// publication CAS still decides whether a new version can be written.
	manifest, _, err := s.ReadVersion(r.Context(), entry.scope, base)
	if err != nil {
		craftWorkbookFidelityRefusal(w, []craftsession.FidelityPart{{Code: "package", Count: 1}})
		return
	}
	file, err := s.DocumentFile(manifest)
	if err != nil {
		craftWorkbookFidelityRefusal(w, []craftsession.FidelityPart{{Code: "package", Count: 1}})
		return
	}
	baseFile := file.Content
	// Validate the format and archive before inspecting preserved parts.
	if _, err := mooring.DetectWorkbook(body); err != nil {
		craftWorkbookError(w, err)
		return
	}
	if err := craftWorkbookValidate(ctx, body); err != nil {
		if !craftWorkbookError(w, err) {
			craftNativeDocumentError(w, err)
		}
		return
	}
	// An address on an unrecognized sheet is still logged as unknown for Undo.
	touched = craftWorkbookTouchedSheets(touched, baseFile, body)
	if lost := craftWorkbookPartsLost(baseFile, body, craftWorkbookHumanEdits(touched)); lost != nil {
		craftWorkbookFidelityRefusal(w, lost)
		return
	}
	// An optional marker only: the save is the same CAS over the head with or without it.
	if source == craftSheetSavedByEditor && r.Header.Get("X-Craft-Rebase") == "keep-local" {
		source = craftSheetSavedByKeepLocal
	}
	// A change of a signed workbook keeps the signature but no longer matches it: said, and recorded.
	signed := craftWorkbookSigned(baseFile)
	sheetRecord := craftSheetRecord(entry.scope, entry.application, entry.source.UserID, source)
	record := func(ctx context.Context, tx *sql.Tx, rc artifacts.VersionReceipt, native []byte) error {
		if err := sheetRecord(ctx, tx, rc, native); err != nil {
			return err
		}
		if signed {
			if err := recordCraftSignatureBreak(ctx, tx, entry.scope.WorkspaceID, entry.scope.ArtifactID, rc.Version); err != nil {
				return err
			}
		}
		if touched == nil {
			return nil
		}
		// The cells the person changed, addresses only, for undo; never replayed.
		return recordCraftVersionOps(ctx, tx, craftVersionOps{WorkspaceID: entry.scope.WorkspaceID, PageID: entry.scope.ArtifactID, Version: rc.Version, BaseVersion: base,
			ActorKind: "human", ActorID: entry.source.UserID, OperationID: "touched:" + strconv.FormatInt(rc.Version, 10), EnginePin: s.EnginePin(), Calculate: "saved", Replayable: false, Ops: touched})
	}
	receipt, err := s.SaveAttemptRecorded(ctx, lease, base, snapshot.EditorRevision, snapshot.ContentHash, body, snapshot.AttemptID, record)
	if err != nil {
		if errors.Is(err, artifacts.ErrVersionConflict) && d.craftWorkbookMoved(w, r, s, entry, base) {
			return
		}
		if !craftWorkbookError(w, err) {
			craftNativeDocumentError(w, err)
		}
		return
	}
	writeJSON(w, 200, craftWorkbookSaveReceipt{NativeSaveReceipt: receipt, SignatureInvalidated: signed})
}

func craftWorkbookFidelityRefusal(w http.ResponseWriter, lost []craftsession.FidelityPart) {
	message := "the workbook changes could not be verified and nothing was saved"
	for _, part := range lost {
		if part.Code == "vba_signature" || part.Code == "xml_signatures" {
			message = "a change to this signed workbook was refused because its signature validity could not be verified"
			break
		}
	}
	writeJSON(w, 422, map[string]any{
		"error": message,
		"code":  "fidelity_loss", "reason": CraftSheetReasonFidelityLoss, "parts": lost,
	})
}

func craftWorkbookHumanEdits(touched json.RawMessage) craftWorkbookEdits {
	edits := craftWorkbookEdits{allowScalarValues: touched == nil}
	var rows []struct {
		Params struct {
			Range string `json:"range"`
		} `json:"params"`
	}
	if json.Unmarshal(touched, &rows) == nil {
		for _, row := range rows {
			if row.Params.Range == "*" {
				edits.allowScalarValues = true
				continue
			}
			edits.add(row.Params.Range)
		}
	}
	return edits
}

// workbookSheets: this server keeps Sheets as workbook files.
func (d *craftNativeDocumentsHTTP) workbookSheets() bool {
	p, ok := d.application("gridcraft")
	return ok && p.Store.Format == mooring.NativeFormatWorkbook
}

// craftVersionOps is one row of craft_version_ops: the operations an agent change of a workbook
// Sheet applied, with the version they made. Ops, Inverse and Digest carry cell content: never
// logged or audited.
type craftVersionOps struct {
	WorkspaceID, PageID                                   string
	Version, BaseVersion                                  int64
	ActorKind, ActorID, ActorName, OperationID, EnginePin string
	Calculate                                             string
	Replayable                                            bool
	Ops, Inverse                                          json.RawMessage
	Digest                                                string
}

// craftVersionOpsMaxBytes bounds one version's operations; a larger change is not replayable.
const craftVersionOpsMaxBytes = 2 << 20

func craftVersionOpsFit(ops, inverse json.RawMessage) bool {
	return len(ops) > 0 && len(ops)+len(inverse) <= craftVersionOpsMaxBytes
}

// recordCraftVersionOps stores o in the transaction that stores its version. An agent's display
// name is taken from the agent row when not given.
func recordCraftVersionOps(ctx context.Context, tx *sql.Tx, o craftVersionOps) error {
	if o.ActorKind == "agent" && o.ActorName == "" {
		_ = tx.QueryRowContext(ctx, `SELECT name FROM agents WHERE id=? AND workspace_id=?`, o.ActorID, o.WorkspaceID).Scan(&o.ActorName)
	}
	ops, inverse := o.Ops, o.Inverse
	if !craftVersionOpsFit(ops, inverse) {
		ops, inverse, o.Replayable = json.RawMessage("[]"), json.RawMessage("[]"), false
	}
	if len(inverse) == 0 {
		inverse = json.RawMessage("[]")
	}
	var digest any
	if o.Digest != "" {
		digest = o.Digest
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO craft_version_ops(workspace_id,page_id,version,base_version,actor_kind,actor_id,actor_name,operation_id,engine_pin,calculate,replayable,ops,inverse,digest,created_at)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, o.WorkspaceID, o.PageID, o.Version, o.BaseVersion, o.ActorKind, o.ActorID, o.ActorName, o.OperationID, o.EnginePin, o.Calculate, o.Replayable, string(ops), string(inverse), digest, tsformat.Format(time.Now().UTC()))
	return err
}

// craftOperationsMaxVersions bounds one operations answer; more versions behind means reload.
const craftOperationsMaxVersions = 50

type craftOperationsVersion struct {
	Version     string          `json:"version"`
	BaseVersion string          `json:"base_version"`
	ActorKind   string          `json:"actor_kind"`
	ActorName   string          `json:"actor_name"`
	OperationID string          `json:"operation_id,omitempty"`
	EnginePin   string          `json:"engine_pin,omitempty"`
	Calculate   string          `json:"calculate,omitempty"`
	Replayable  bool            `json:"replayable"`
	Ops         json.RawMessage `json:"ops,omitempty"`
	Digest      string          `json:"digest,omitempty"`
	// SignatureInvalidated: this version no longer matches the workbook's XML package signature.
	SignatureInvalidated bool `json:"signature_invalidated,omitempty"`
}

// craftWorkbookSaveReceipt answers a person's save of a workbook Sheet.
type craftWorkbookSaveReceipt struct {
	mooring.NativeSaveReceipt
	// SignatureInvalidated: the workbook's XML package signature was kept but no longer matches.
	SignatureInvalidated bool `json:"signature_invalidated,omitempty"`
}

type craftOperationsResponse struct {
	From            string                   `json:"from"`
	To              string                   `json:"to"`
	Head            string                   `json:"head"`
	HeadContentHash string                   `json:"head_content_hash"`
	Replayable      bool                     `json:"replayable"`
	Versions        []craftOperationsVersion `json:"versions"`
}

// listOperations answers GET craft/document/operations?from=<base>: every version after base up to
// the head, with the operations of those an agent change made, so an editor holding unsaved work
// replays them over its own state; replayable only when every one of them has its operations.
func (d *craftNativeDocumentsHTTP) listOperations(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	r, entry, s, _, err := d.bind(r)
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	if !s.Workbook() {
		replyCode(w, 404, "not_workbook", "operations are kept for workbook Sheets only")
		return
	}
	if !d.craftNativeHistoryReadable(w, r, entry) {
		return
	}
	from, err := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
	if err != nil || from < 1 {
		replyCode(w, 400, "from_invalid", "from must be a version")
		return
	}
	recent, _, err := s.Versions(r.Context(), entry.scope, craftOperationsMaxVersions+1)
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	if len(recent) == 0 || from > recent[0].Version {
		replyCode(w, 400, "from_invalid", "from is not a version of this Sheet")
		return
	}
	head := recent[0]
	if head.Version-from > craftOperationsMaxVersions {
		replyCode(w, 413, "too_many", "too many versions behind; reload the Sheet")
		return
	}
	rows, err := d.human.pages.db.QueryContext(r.Context(), `SELECT version,base_version,actor_kind,actor_name,operation_id,engine_pin,calculate,replayable,ops,COALESCE(digest,'') FROM craft_version_ops
 WHERE workspace_id=? AND page_id=? AND version>? AND version<=?`, entry.scope.WorkspaceID, entry.scope.ArtifactID, from, head.Version)
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	logged := map[int64]craftOperationsVersion{}
	for rows.Next() {
		var v craftOperationsVersion
		var version, base int64
		var ops string
		if err = rows.Scan(&version, &base, &v.ActorKind, &v.ActorName, &v.OperationID, &v.EnginePin, &v.Calculate, &v.Replayable, &ops, &v.Digest); err != nil {
			break
		}
		v.Version, v.BaseVersion = strconv.FormatInt(version, 10), strconv.FormatInt(base, 10)
		if v.Replayable {
			v.Ops = json.RawMessage(ops)
		}
		logged[version] = v
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	broken := map[int64]bool{}
	if rows, err = d.human.pages.db.QueryContext(r.Context(), `SELECT version FROM craft_version_signature_breaks WHERE workspace_id=? AND page_id=? AND version>? AND version<=?`, entry.scope.WorkspaceID, entry.scope.ArtifactID, from, head.Version); err == nil {
		for rows.Next() {
			var v int64
			if err = rows.Scan(&v); err != nil {
				break
			}
			broken[v] = true
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
	}
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	if !d.auditRead(w, r, entry) {
		return
	}
	out := craftOperationsResponse{From: strconv.FormatInt(from, 10), To: strconv.FormatInt(head.Version, 10), Head: strconv.FormatInt(head.Version, 10), HeadContentHash: "sha256:" + head.EntryHash, Replayable: true, Versions: []craftOperationsVersion{}}
	for i := len(recent) - 1; i >= 0; i-- {
		info := recent[i]
		if info.Version <= from {
			continue
		}
		v, ok := logged[info.Version]
		if !ok {
			v = craftOperationsVersion{Version: strconv.FormatInt(info.Version, 10), BaseVersion: strconv.FormatInt(info.Version-1, 10), ActorKind: craftActorKind(info.Actor)}
		}
		v.SignatureInvalidated = broken[info.Version]
		out.Replayable = out.Replayable && v.Replayable
		out.Versions = append(out.Versions, v)
	}
	writeJSON(w, 200, out)
}

func craftActorKind(actor string) string {
	switch {
	case strings.HasPrefix(actor, "user:"):
		return "human"
	case strings.HasPrefix(actor, "agent:"):
		return "agent"
	}
	return "system"
}

// craftWorkbookMoved answers a save whose base is no longer the head: where the head is and where
// the operations since the base are, so the editor replays them over its own work.
func (d *craftNativeDocumentsHTTP) craftWorkbookMoved(w http.ResponseWriter, r *http.Request, s *mooring.NativeStore, entry *craftNativeSession, base int64) bool {
	recent, _, err := s.Versions(r.Context(), entry.scope, 1)
	if err != nil || len(recent) == 0 || recent[0].Version == base {
		return false
	}
	writeJSON(w, 409, map[string]string{"error": "the Sheet has a newer version; replay its operations over your work", "code": "moved",
		"head": strconv.FormatInt(recent[0].Version, 10), "operations": "/api/v1/pages/" + url.PathEscape(r.PathValue("slug")) + "/craft/document/operations?from=" + strconv.FormatInt(base, 10)})
	return true
}

// craftWorkbookTouched reads X-Craft-Touched: the cells a person's save changed since the editor's
// saved version, as a JSON list of sheet-qualified addresses ("'L'!B2", "'L'!A1:C9"), at most 2000
// and 64 KiB, or "unknown". It answers the operations row to store, nil without the header.
func craftWorkbookTouched(values []string) (json.RawMessage, bool) {
	if len(values) == 0 {
		return nil, true
	}
	type touch struct {
		Command string            `json:"command"`
		Params  map[string]string `json:"params"`
	}
	if len(values) != 1 || len(values[0]) > 64<<10 {
		return nil, false
	}
	if values[0] == "unknown" {
		raw, _ := json.Marshal([]touch{{Command: "touched", Params: map[string]string{"range": "*"}}})
		return raw, true
	}
	var list []string
	if json.Unmarshal([]byte(values[0]), &list) != nil || list == nil || len(list) > 2000 {
		return nil, false
	}
	out := make([]touch, 0, len(list))
	for _, address := range list {
		if _, _, ok := craftSheetAddress(address); !ok {
			return nil, false
		}
		out = append(out, touch{Command: "touched", Params: map[string]string{"range": address}})
	}
	if len(out) == 0 {
		raw, _ := json.Marshal([]touch{})
		return raw, true
	}
	raw, _ := json.Marshal(out)
	return raw, true
}

// craftWorkbookTouchedSheets keeps touched when every address names a sheet of the saved or the
// new workbook, and answers the unknown row ("*") otherwise.
func craftWorkbookTouchedSheets(touched json.RawMessage, base, body []byte) json.RawMessage {
	if touched == nil {
		return nil
	}
	var rows []struct {
		Params map[string]string `json:"params"`
	}
	if json.Unmarshal(touched, &rows) != nil {
		return craftWorkbookTouchedUnknown()
	}
	names, ok := craftWorkbookSheetNames(body)
	if !ok {
		return craftWorkbookTouchedUnknown()
	}
	if len(base) > 0 {
		if old, ok := craftWorkbookSheetNames(base); ok {
			for n := range old {
				names[n] = true
			}
		}
	}
	for _, row := range rows {
		address := row.Params["range"]
		if address == "*" {
			continue
		}
		sheet, _, ok := craftSheetAddress(address)
		if !ok || !names[sheet] {
			return craftWorkbookTouchedUnknown()
		}
	}
	return touched
}

func craftWorkbookTouchedUnknown() json.RawMessage {
	return json.RawMessage(`[{"command":"touched","params":{"range":"*"}}]`)
}

// craftWorkbookSheetNames reads the sheet names of xl/workbook.xml (at most 1 MiB).
func craftWorkbookSheetNames(raw []byte) (map[string]bool, bool) {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, false
	}
	for _, f := range zr.File {
		if f.Name != "xl/workbook.xml" || f.UncompressedSize64 > 1<<20 {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, false
		}
		defer rc.Close()
		dec := xml.NewDecoder(io.LimitReader(rc, 1<<20))
		names := map[string]bool{}
		for {
			tok, err := dec.Token()
			if err == io.EOF {
				return names, len(names) > 0
			}
			if err != nil {
				return nil, false
			}
			if el, ok := tok.(xml.StartElement); ok && el.Name.Local == "sheet" {
				for _, at := range el.Attr {
					if at.Name.Local == "name" && at.Name.Space == "" {
						names[at.Value] = true
					}
				}
			}
		}
	}
	return nil, false
}
