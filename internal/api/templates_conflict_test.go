package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTemplateDuplicateNameConflict(t *testing.T) {
	r := newCovTplRig(t)
	r.seedTemplate(t, "existing", "Release", false)
	r.seedTemplate(t, "rename", "Other", false)
	for _, tc := range []struct{ name, method, body, id string }{
		{"create", "POST", `{"name":"Release","template_json":null}`, ""},
		{"rename", "PATCH", `{"name":"Release"}`, "rename"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := r.req(tc.method, "/api/v1/templates", tc.body, tc.id, "ADMIN")
			if tc.method == "POST" {
				r.h.Create(rec, req)
			} else {
				r.h.Update(rec, req)
			}
			if rec.Code != http.StatusConflict {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
	var count int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM workflow_templates WHERE workspace_id=? AND name='Release'`, r.wsID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate changed rows: count=%d err=%v", count, err)
	}
	var name string
	if err := r.db.QueryRow(`SELECT name FROM workflow_templates WHERE id='rename'`).Scan(&name); err != nil || name != "Other" {
		t.Fatalf("failed rename changed row: name=%q err=%v", name, err)
	}
}

func TestTemplateSameNameInOtherWorkspace(t *testing.T) {
	r := newCovTplRig(t)
	r.seedTemplate(t, "existing", "Release", false)
	other := "other-template-workspace"
	if _, err := r.db.Exec(`INSERT INTO workspaces (id,name,slug) VALUES (?, 'Other', 'other-template-workspace')`, other); err != nil {
		t.Fatal(err)
	}
	req := r.req("POST", "/api/v1/templates", `{"name":"Release","template_json":null}`, "", "ADMIN")
	req = req.WithContext(withWorkspace(req.Context(), other, "ADMIN"))
	rec := httptest.NewRecorder()
	r.h.Create(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestTemplateNonUniqueStorageFailureRemains500(t *testing.T) {
	r := newCovTplRig(t)
	r.seedTemplate(t, "existing", "Release", false)
	for _, statement := range []string{
		`CREATE TRIGGER reject_template_insert BEFORE INSERT ON workflow_templates BEGIN SELECT RAISE(ABORT,'storage unavailable'); END`,
		`CREATE TRIGGER reject_template_update BEFORE UPDATE ON workflow_templates BEGIN SELECT RAISE(ABORT,'storage unavailable'); END`,
	} {
		if _, err := r.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	for _, method := range []string{"POST", "PATCH"} {
		t.Run(method, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := r.req(method, "/api/v1/templates", `{"name":"New","template_json":null}`, "existing", "ADMIN")
			if method == "POST" {
				r.h.Create(rec, req)
			} else {
				r.h.Update(rec, req)
			}
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestTemplateConflictClassificationIsNarrow(t *testing.T) {
	for _, tc := range []struct {
		message string
		want    bool
	}{
		{"constraint failed: UNIQUE constraint failed: workflow_templates.workspace_id, workflow_templates.name (2067)", true},
		{"constraint failed: UNIQUE constraint failed: workflow_templates.id (1555)", false},
		{"constraint failed: UNIQUE constraint failed: workflow_templates.workspace_id, workflow_templates.name_suffix (2067)", false},
		{"constraint failed: UNIQUE constraint failed: workflow_templates.workspace_id, workflow_templates.name, workflow_templates.third (2067)", false},
		{"constraint failed: CHECK constraint failed: template_json (275)", false},
		{"database is locked", false},
	} {
		if got := isTemplateNameConflict(errors.New(tc.message)); got != tc.want {
			t.Errorf("classification %q =%v, want%v", tc.message, got, tc.want)
		}
	}
	if isTemplateNameConflict(nil) {
		t.Fatal("nil is not a conflict")
	}
}

func TestTemplateClosedDatabaseRemains500(t *testing.T) {
	r := newCovTplRig(t)
	if err := r.db.Close(); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	r.h.Create(rec, r.req("POST", "/api/v1/templates", `{"name":"Release","template_json":null}`, "", "ADMIN"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
