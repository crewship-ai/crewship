package api

// Coverage tests for templates.go — Get success, Update flow (all setters,
// builtin guard, invalid JSON), Delete, and empty-list fallback.

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type covTplRig struct {
	h    *TemplateHandler
	db   *sql.DB
	wsID string
}

func newCovTplRig(t *testing.T) *covTplRig {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	return &covTplRig{h: NewTemplateHandler(db, newTestLogger()), db: db, wsID: wsID}
}

func (r *covTplRig) seedTemplate(t *testing.T, id, name string, builtin bool) {
	t.Helper()
	b := 0
	if builtin {
		b = 1
	}
	if _, err := r.db.Exec(`
		INSERT INTO workflow_templates (id, workspace_id, name, description, template_json, is_builtin, created_at, updated_at)
		VALUES (?, ?, ?, 'desc', '{"v":1}', ?, datetime('now'), datetime('now'))`,
		id, r.wsID, name, b); err != nil {
		t.Fatalf("seed template: %v", err)
	}
}

func (r *covTplRig) req(method, target, body, templateID, role string) *http.Request {
	var rd *strings.Reader
	if body == "" {
		rd = strings.NewReader("")
	} else {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rd)
	if templateID != "" {
		req.SetPathValue("templateId", templateID)
	}
	return req.WithContext(withWorkspace(req.Context(), r.wsID, role))
}

func TestCovTplGet_Success(t *testing.T) {
	r := newCovTplRig(t)
	r.seedTemplate(t, "wt_get", "My Template", false)

	rec := httptest.NewRecorder()
	r.h.Get(rec, r.req("GET", "/api/v1/templates/wt_get", "", "wt_get", "MEMBER"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var resp templateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Name != "My Template" || string(resp.TemplateJSON) != `{"v":1}` {
		t.Errorf("unexpected response: %+v", resp)
	}
}

func TestCovTplGet_NotFound(t *testing.T) {
	r := newCovTplRig(t)
	rec := httptest.NewRecorder()
	r.h.Get(rec, r.req("GET", "/api/v1/templates/missing", "", "missing", "MEMBER"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// Note: the json.Valid(template_json) guards in Create/Update are not
// reachable through the HTTP surface — json.RawMessage produced by a
// successful outer decode is always valid JSON — so they are not tested.

func TestCovTplUpdate_FullFlow(t *testing.T) {
	r := newCovTplRig(t)
	r.seedTemplate(t, "wt_upd", "Before", false)

	body := `{"name":"After","description":"new desc","template_json":{"v":2},"icon":"star","color":"#fff"}`
	rec := httptest.NewRecorder()
	r.h.Update(rec, r.req("PATCH", "/api/v1/templates/wt_upd", body, "wt_upd", "ADMIN"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}

	var name, desc, tmpl, icon, color string
	if err := r.db.QueryRow(`SELECT name, description, template_json, icon, color FROM workflow_templates WHERE id = 'wt_upd'`).
		Scan(&name, &desc, &tmpl, &icon, &color); err != nil {
		t.Fatalf("query: %v", err)
	}
	if name != "After" || desc != "new desc" || tmpl != `{"v":2}` || icon != "star" || color != "#fff" {
		t.Errorf("row = (%s, %s, %s, %s, %s)", name, desc, tmpl, icon, color)
	}
}

func TestCovTplUpdate_Guards(t *testing.T) {
	r := newCovTplRig(t)
	r.seedTemplate(t, "wt_b", "Builtin", true)
	r.seedTemplate(t, "wt_n", "Normal", false)

	t.Run("forbidden role", func(t *testing.T) {
		rec := httptest.NewRecorder()
		r.h.Update(rec, r.req("PATCH", "/x", `{}`, "wt_n", "VIEWER"))
		if rec.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", rec.Code)
		}
	})

	t.Run("invalid JSON body", func(t *testing.T) {
		rec := httptest.NewRecorder()
		r.h.Update(rec, r.req("PATCH", "/x", `{bad`, "wt_n", "ADMIN"))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("builtin forbidden", func(t *testing.T) {
		rec := httptest.NewRecorder()
		r.h.Update(rec, r.req("PATCH", "/x", `{"name":"hack"}`, "wt_b", "ADMIN"))
		if rec.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("not found", func(t *testing.T) {
		rec := httptest.NewRecorder()
		r.h.Update(rec, r.req("PATCH", "/x", `{"name":"x"}`, "wt_missing", "ADMIN"))
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})
}

func TestCovTplDelete_BuiltinForbidden(t *testing.T) {
	r := newCovTplRig(t)
	r.seedTemplate(t, "wt_bd", "Builtin", true)
	rec := httptest.NewRecorder()
	r.h.Delete(rec, r.req("DELETE", "/x", "", "wt_bd", "ADMIN"))
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
	// Row must survive.
	var n int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM workflow_templates WHERE id = 'wt_bd'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("builtin row gone (n=%d, err=%v)", n, err)
	}
}

func TestCovTplDelete_Success(t *testing.T) {
	r := newCovTplRig(t)
	r.seedTemplate(t, "wt_del", "Deletable", false)
	rec := httptest.NewRecorder()
	r.h.Delete(rec, r.req("DELETE", "/x", "", "wt_del", "ADMIN"))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	var n int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM workflow_templates WHERE id = 'wt_del'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("row not deleted (n=%d, err=%v)", n, err)
	}
}

func TestCovTplCheckModifiable_DBError(t *testing.T) {
	r := newCovTplRig(t)
	r.db.Close()
	rec := httptest.NewRecorder()
	r.h.Delete(rec, r.req("DELETE", "/x", "", "wt_x", "ADMIN"))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestCovTplGenerateTemplateID_Shape(t *testing.T) {
	a, b := generateTemplateID(), generateTemplateID()
	if !strings.HasPrefix(a, "wt_") || len(a) != len("wt_")+16 {
		t.Errorf("id shape = %q", a)
	}
	if a == b {
		t.Error("ids should be unique")
	}
}
