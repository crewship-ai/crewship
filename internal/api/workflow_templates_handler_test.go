package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// newWorkflowTemplateHandler spins up an isolated handler with a fresh test DB
// seeded with a workspace+user so every test starts from the same baseline.
// Returns (handler, userID, workspaceID).
func newWorkflowTemplateHandler(t *testing.T) (*WorkflowTemplateHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	return NewWorkflowTemplateHandler(db, nil, logger), userID, wsID
}

// validStagesJSON is a reusable five-stage template that exercises every stage type.
// Tests that just need a "well-formed" payload reach for this so handler tests
// stay focused on the verb under test, not on template-shape boilerplate.
const validStagesJSON = `[` +
	`{"name":"backlog","type":"open","position":1,"color":"#9CA3AF"},` +
	`{"name":"in_progress","type":"started","position":2,"color":"#3B82F6"},` +
	`{"name":"in_review","type":"started","position":3,"color":"#F59E0B"},` +
	`{"name":"done","type":"completed","position":4,"color":"#10B981"},` +
	`{"name":"cancelled","type":"cancelled","position":5,"color":"#EF4444"}` +
	`]`

func wtPostBody(name, stagesJSON string) string {
	body, _ := json.Marshal(map[string]string{
		"name":          name,
		"description":   "Default issue lifecycle",
		"template_json": stagesJSON,
		"icon":          ":hammer_and_wrench:",
		"color":         "#3B82F6",
	})
	return string(body)
}

// ── Create ─────────────────────────────────────────────────────────────────

func TestWorkflowTemplate_Create_OK(t *testing.T) {
	h, userID, wsID := newWorkflowTemplateHandler(t)

	req := httptest.NewRequest("POST", "/", bytes.NewBufferString(wtPostBody("Engineering Standard", validStagesJSON)))
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d body=%s", rr.Code, rr.Body.String())
	}
	var resp workflowTemplateResponse
	mustUnmarshal(t, rr, &resp)
	if resp.ID == "" {
		t.Error("id should be populated")
	}
	if resp.Name != "Engineering Standard" {
		t.Errorf("name = %q, want Engineering Standard", resp.Name)
	}
	if resp.IsBuiltin {
		t.Error("is_builtin must be false for user-created templates")
	}
	if resp.TemplateJSON != validStagesJSON {
		t.Errorf("template_json round-trip mismatch")
	}
}

func TestWorkflowTemplate_Create_Validations(t *testing.T) {
	h, userID, wsID := newWorkflowTemplateHandler(t)

	cases := []struct {
		name string
		body string
		want int
	}{
		{"no name", `{"template_json":` + jsonQuote(validStagesJSON) + `}`, http.StatusBadRequest},
		{"no template_json", `{"name":"x"}`, http.StatusBadRequest},
		{"bad json body", `not json`, http.StatusBadRequest},
		{"template_json not an array",
			`{"name":"x","template_json":"{}"}`, http.StatusBadRequest},
		{"stage missing name",
			`{"name":"x","template_json":` + jsonQuote(`[{"type":"open","position":1}]`) + `}`, http.StatusBadRequest},
		{"stage missing type",
			`{"name":"x","template_json":` + jsonQuote(`[{"name":"a","position":1}]`) + `}`, http.StatusBadRequest},
		{"stage missing position",
			`{"name":"x","template_json":` + jsonQuote(`[{"name":"a","type":"open"}]`) + `}`, http.StatusBadRequest},
		{"stage invalid type",
			`{"name":"x","template_json":` + jsonQuote(`[{"name":"a","type":"yolo","position":1}]`) + `}`, http.StatusBadRequest},
		{"duplicate name",
			`{"name":"x","template_json":` + jsonQuote(`[{"name":"a","type":"open","position":1},{"name":"a","type":"completed","position":2}]`) + `}`, http.StatusBadRequest},
		{"duplicate position",
			`{"name":"x","template_json":` + jsonQuote(`[{"name":"a","type":"open","position":1},{"name":"b","type":"completed","position":1}]`) + `}`, http.StatusBadRequest},
		{"no open stage",
			`{"name":"x","template_json":` + jsonQuote(`[{"name":"a","type":"started","position":1},{"name":"b","type":"completed","position":2}]`) + `}`, http.StatusBadRequest},
		{"no completed stage",
			`{"name":"x","template_json":` + jsonQuote(`[{"name":"a","type":"open","position":1}]`) + `}`, http.StatusBadRequest},
		{"two open stages",
			`{"name":"x","template_json":` + jsonQuote(`[{"name":"a","type":"open","position":1},{"name":"b","type":"open","position":2},{"name":"c","type":"completed","position":3}]`) + `}`, http.StatusBadRequest},
		{"bad stage color",
			`{"name":"x","template_json":` + jsonQuote(`[{"name":"a","type":"open","position":1,"color":"notahex"},{"name":"b","type":"completed","position":2}]`) + `}`, http.StatusBadRequest},
		{"empty stages",
			`{"name":"x","template_json":"[]"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/", bytes.NewBufferString(tc.body))
			req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
			rr := httptest.NewRecorder()
			h.Create(rr, req)
			if rr.Code != tc.want {
				t.Errorf("got %d want %d body=%s", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

// Server must reject duplicate template names within the same workspace
// (DB has a UNIQUE(workspace_id, name) index — handler should surface 409).
func TestWorkflowTemplate_Create_DuplicateName(t *testing.T) {
	h, userID, wsID := newWorkflowTemplateHandler(t)
	body := wtPostBody("dup", validStagesJSON)

	// First create succeeds.
	req := httptest.NewRequest("POST", "/", bytes.NewBufferString(body))
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("first create: %d body=%s", rr.Code, rr.Body.String())
	}

	// Second with same name in same workspace must 409.
	req2 := httptest.NewRequest("POST", "/", bytes.NewBufferString(body))
	req2 = req2.WithContext(withWorkspace(withUser(req2.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr2 := httptest.NewRecorder()
	h.Create(rr2, req2)
	if rr2.Code != http.StatusConflict {
		t.Errorf("dup create status = %d, want 409 body=%s", rr2.Code, rr2.Body.String())
	}
}

// ── RBAC for Create ────────────────────────────────────────────────────────

func TestWorkflowTemplate_Create_RBAC(t *testing.T) {
	body := wtPostBody("rbac", validStagesJSON)

	cases := []struct {
		role string
		want int
	}{
		{"OWNER", http.StatusCreated},
		{"ADMIN", http.StatusCreated},
		{"MANAGER", http.StatusCreated},
		{"MEMBER", http.StatusForbidden},
		{"VIEWER", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.role, func(t *testing.T) {
			h, userID, wsID := newWorkflowTemplateHandler(t)
			req := httptest.NewRequest("POST", "/", bytes.NewBufferString(body))
			req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, tc.role))
			rr := httptest.NewRecorder()
			h.Create(rr, req)
			if rr.Code != tc.want {
				t.Errorf("role=%s got %d want %d body=%s", tc.role, rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

// ── List / Get ─────────────────────────────────────────────────────────────

func TestWorkflowTemplate_List_EmptyReturnsEmptyArray(t *testing.T) {
	h, userID, wsID := newWorkflowTemplateHandler(t)
	req := httptest.NewRequest("GET", "/", nil)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "VIEWER"))
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: %d body=%s", rr.Code, rr.Body.String())
	}
	if got := bytes.TrimSpace(rr.Body.Bytes()); string(got) != "[]" {
		t.Errorf("empty list body = %q, want []", string(got))
	}
}

func TestWorkflowTemplate_List_VIEWER_CanRead(t *testing.T) {
	h, userID, wsID := newWorkflowTemplateHandler(t)
	// Seed one as OWNER first.
	created := mustCreateTemplate(t, h, userID, wsID, "shared")

	// Then a VIEWER lists.
	req := httptest.NewRequest("GET", "/", nil)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "VIEWER"))
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: %d body=%s", rr.Code, rr.Body.String())
	}
	var list []workflowTemplateResponse
	mustUnmarshal(t, rr, &list)
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("list mismatch: %+v", list)
	}
}

func TestWorkflowTemplate_Get_OK(t *testing.T) {
	h, userID, wsID := newWorkflowTemplateHandler(t)
	created := mustCreateTemplate(t, h, userID, wsID, "g1")

	req := httptest.NewRequest("GET", "/", nil)
	req.SetPathValue("id", created.ID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "VIEWER"))
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("get: %d body=%s", rr.Code, rr.Body.String())
	}
	var got workflowTemplateResponse
	mustUnmarshal(t, rr, &got)
	if got.ID != created.ID || got.Name != created.Name {
		t.Errorf("get mismatch: %+v vs %+v", got, created)
	}
}

func TestWorkflowTemplate_Get_NotFound(t *testing.T) {
	h, userID, wsID := newWorkflowTemplateHandler(t)
	req := httptest.NewRequest("GET", "/", nil)
	req.SetPathValue("id", "missing")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d", rr.Code)
	}
}

// Cross-workspace lookup must NOT leak: rows from other workspaces are 404 here.
func TestWorkflowTemplate_Get_OtherWorkspaceIsolated(t *testing.T) {
	h, userID, wsID := newWorkflowTemplateHandler(t)
	created := mustCreateTemplate(t, h, userID, wsID, "iso")

	// Pretend caller is in a different workspace.
	req := httptest.NewRequest("GET", "/", nil)
	req.SetPathValue("id", created.ID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), "another-ws", "OWNER"))
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("cross-ws get must 404, got %d", rr.Code)
	}
}

// ── Update ─────────────────────────────────────────────────────────────────

func TestWorkflowTemplate_Update_OK(t *testing.T) {
	h, userID, wsID := newWorkflowTemplateHandler(t)
	created := mustCreateTemplate(t, h, userID, wsID, "u1")

	body := `{"name":"u1-renamed","description":"updated desc","color":"#000000"}`
	req := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(body))
	req.SetPathValue("id", created.ID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("update: %d body=%s", rr.Code, rr.Body.String())
	}
	var got workflowTemplateResponse
	mustUnmarshal(t, rr, &got)
	if got.Name != "u1-renamed" {
		t.Errorf("name = %q, want u1-renamed", got.Name)
	}
	if got.Color == nil || *got.Color != "#000000" {
		t.Errorf("color = %v, want #000000", got.Color)
	}
}

func TestWorkflowTemplate_Update_NoFields(t *testing.T) {
	h, userID, wsID := newWorkflowTemplateHandler(t)
	created := mustCreateTemplate(t, h, userID, wsID, "u-nofields")

	req := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(`{}`))
	req.SetPathValue("id", created.ID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestWorkflowTemplate_Update_BadStages(t *testing.T) {
	h, userID, wsID := newWorkflowTemplateHandler(t)
	created := mustCreateTemplate(t, h, userID, wsID, "u-bad-stages")

	// Pass an invalid template_json — handler must re-validate stage shape on Update.
	body := `{"template_json":` + jsonQuote(`[{"name":"a","type":"yolo","position":1}]`) + `}`
	req := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(body))
	req.SetPathValue("id", created.ID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestWorkflowTemplate_Update_NotFound(t *testing.T) {
	h, userID, wsID := newWorkflowTemplateHandler(t)
	req := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(`{"name":"x"}`))
	req.SetPathValue("id", "missing")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Update(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d", rr.Code)
	}
}

func TestWorkflowTemplate_Update_RBAC(t *testing.T) {
	cases := []struct {
		role string
		want int
	}{
		{"OWNER", http.StatusOK},
		{"ADMIN", http.StatusOK},
		{"MANAGER", http.StatusOK},
		{"MEMBER", http.StatusForbidden},
		{"VIEWER", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.role, func(t *testing.T) {
			h, userID, wsID := newWorkflowTemplateHandler(t)
			created := mustCreateTemplate(t, h, userID, wsID, "u-rbac-"+tc.role)

			req := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(`{"name":"new"}`))
			req.SetPathValue("id", created.ID)
			req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, tc.role))
			rr := httptest.NewRecorder()
			h.Update(rr, req)
			if rr.Code != tc.want {
				t.Errorf("role=%s got %d want %d body=%s", tc.role, rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

// ── Delete ─────────────────────────────────────────────────────────────────

func TestWorkflowTemplate_Delete_OK(t *testing.T) {
	h, userID, wsID := newWorkflowTemplateHandler(t)
	created := mustCreateTemplate(t, h, userID, wsID, "d1")

	req := httptest.NewRequest("DELETE", "/", nil)
	req.SetPathValue("id", created.ID)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Errorf("delete: %d", rr.Code)
	}

	// Confirm follow-up Get is 404.
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.SetPathValue("id", created.ID)
	req2 = req2.WithContext(withWorkspace(withUser(req2.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr2 := httptest.NewRecorder()
	h.Get(rr2, req2)
	if rr2.Code != http.StatusNotFound {
		t.Errorf("post-delete get must 404, got %d", rr2.Code)
	}
}

func TestWorkflowTemplate_Delete_NotFound(t *testing.T) {
	h, userID, wsID := newWorkflowTemplateHandler(t)
	req := httptest.NewRequest("DELETE", "/", nil)
	req.SetPathValue("id", "missing")
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d", rr.Code)
	}
}

func TestWorkflowTemplate_Delete_RBAC(t *testing.T) {
	cases := []struct {
		role string
		want int
	}{
		{"OWNER", http.StatusNoContent},
		{"ADMIN", http.StatusNoContent},
		{"MANAGER", http.StatusNoContent},
		{"MEMBER", http.StatusForbidden},
		{"VIEWER", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.role, func(t *testing.T) {
			h, userID, wsID := newWorkflowTemplateHandler(t)
			created := mustCreateTemplate(t, h, userID, wsID, "d-rbac-"+tc.role)

			req := httptest.NewRequest("DELETE", "/", nil)
			req.SetPathValue("id", created.ID)
			req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, tc.role))
			rr := httptest.NewRecorder()
			h.Delete(rr, req)
			if rr.Code != tc.want {
				t.Errorf("role=%s got %d want %d body=%s", tc.role, rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

// ── helpers ────────────────────────────────────────────────────────────────

// mustCreateTemplate creates a template as OWNER and returns the response.
// Centralised so each test doesn't have to repeat the eight-line ceremony.
func mustCreateTemplate(t *testing.T, h *WorkflowTemplateHandler, userID, wsID, name string) workflowTemplateResponse {
	t.Helper()
	req := httptest.NewRequest("POST", "/", bytes.NewBufferString(wtPostBody(name, validStagesJSON)))
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("seed create: %d body=%s", rr.Code, rr.Body.String())
	}
	var resp workflowTemplateResponse
	mustUnmarshal(t, rr, &resp)
	return resp
}

// jsonQuote returns the given string wrapped + escaped as a JSON string literal,
// e.g. `hello"world` -> `"hello\"world"`. Used to embed nested JSON inside test
// request bodies without manual escaping noise.
func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
