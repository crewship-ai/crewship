package api

import "net/http"

// workbookEditorStatus answers GET /api/v1/admin/instance/workbook-editor:
// the editor state for instance administrators (`crewship admin instance
// workbook-editor`, `crewship doctor`). While the editor is off it is
// {"mode":"off"}.
func (r *Router) workbookEditorStatus(w http.ResponseWriter, _ *http.Request) {
	if r.workbookEditorDetail == nil {
		writeJSON(w, http.StatusOK, map[string]string{"mode": "off"})
		return
	}
	writeJSON(w, http.StatusOK, r.workbookEditorDetail())
}
