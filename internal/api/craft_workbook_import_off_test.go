package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// With workbook Sheets the xlsx import is gone (an upload starts the Sheet as its file through
// initialize); a native server keeps it until migration.
func TestWorkbookSheetsHaveNoImport(t *testing.T) {
	rig := workbookDocumentsFixture(t)
	call := func(d *craftNativeDocumentsHTTP, name string, h func(*craftNativeDocumentsHTTP) http.HandlerFunc) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/pages/native-page/craft/import", strings.NewReader("PK")).WithContext(rig.ctx)
		r.SetPathValue("slug", "native-page")
		r.SetPathValue("id", "imp_0123456789abcdef")
		w := httptest.NewRecorder()
		h(d)(w, r)
		return w
	}
	handlers := map[string]func(*craftNativeDocumentsHTTP) http.HandlerFunc{
		"start":      func(d *craftNativeDocumentsHTTP) http.HandlerFunc { return d.importStart },
		"status":     func(d *craftNativeDocumentsHTTP) http.HandlerFunc { return d.importStatus },
		"document":   func(d *craftNativeDocumentsHTTP) http.HandlerFunc { return d.importDocument },
		"initialize": func(d *craftNativeDocumentsHTTP) http.HandlerFunc { return d.importInitialize },
	}
	for name, h := range handlers {
		if w := call(rig.d, name, h); w.Code != 404 || !strings.Contains(w.Body.String(), `"code":"use_initialize"`) {
			t.Errorf("workbook %s: %d %s", name, w.Code, w.Body)
		}
	}
	native := newCraftNativeDocumentsHTTP(rig.d.human, map[string]craftNativeApplication{"gridcraft": {Store: rig.d.applications["gridcraft"].Store}})
	p := native.applications["gridcraft"]
	p.Store.Format = ""
	native.applications["gridcraft"] = p
	if w := call(native, "start", handlers["start"]); strings.Contains(w.Body.String(), "use_initialize") {
		t.Errorf("a native server refused its import: %d %s", w.Code, w.Body)
	}
}
