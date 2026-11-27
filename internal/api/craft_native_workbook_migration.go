package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/craftapps"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// Conversion runs outside the SQL writer. Limit concurrent exports on this
// server; publication independently rechecks the source and the caller.
var craftWorkbookConversions = make(chan struct{}, 2)

func (d *craftNativeDocumentsHTTP) migrateWorkbook(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	r, entry, store, _, err := d.bind(r)
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	source, err := store.LegacyWorkbook(r.Context(), entry.scope, true)
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	converted := source.WorkbookVersion
	if converted == 0 {
		if !d.human.apps.allows(r.Context(), entry.application, craftapps.OpStartEdit) {
			d.human.apps.refuse(w, r.Context(), entry.application)
			return
		}
		select {
		case craftWorkbookConversions <- struct{}{}:
			defer func() { <-craftWorkbookConversions }()
		default:
			writeJSON(w, 503, map[string]string{"code": "legacy_workbook_busy", "error": "Sheet conversion is busy. Reopen the document to try again."})
			return
		}
		profile, ok := d.profile(entry.application)
		if !ok || !profile.Workbook {
			craftNativeDocumentError(w, mooring.ErrNativeDisabled)
			return
		}
		workbook, err := mooring.ConvertLegacyWorkbook(r.Context(), profile.Engine, source.Bytes)
		if errors.Is(err, mooring.ErrWorkbookFidelity) {
			writeJSON(w, 422, map[string]string{"code": "legacy_workbook_fidelity", "error": "The Sheet could not be converted to Excel without losing information. Download the original Sheet."})
			return
		}
		if err != nil {
			replyError(w, 500, "Sheet conversion could not be completed")
			return
		}
		converted, err = store.MigrateLegacyWorkbook(r.Context(), entry.scope, source, workbook)
		if errors.Is(err, artifacts.ErrVersionConflict) || errors.Is(err, mooring.ErrEditLeaseBusy) {
			writeJSON(w, 409, map[string]string{"code": "legacy_workbook_busy", "error": "The Sheet changed or is being edited. Reopen it to try again."})
			return
		}
		if err != nil {
			craftNativeDocumentError(w, err)
			return
		}
	}
	writeJSON(w, 200, map[string]string{"version": strconv.FormatInt(converted, 10)})
}

func (d *craftNativeDocumentsHTTP) downloadLegacyWorkbook(w http.ResponseWriter, r *http.Request) {
	r, entry, store, _, err := d.bind(r)
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	source, err := store.LegacyWorkbook(r.Context(), entry.scope, false)
	if err != nil {
		craftNativeDocumentError(w, err)
		return
	}
	if !d.auditRead(w, r, entry) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=sheet-original.json")
	w.Header().Set("Content-Length", strconv.Itoa(len(source.Bytes)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(source.Bytes)
}
