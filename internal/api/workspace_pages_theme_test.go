package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWorkspacePagesTheme(t *testing.T) {
	db := setupTestDB(t)
	h := &WorkspaceHandler{db: db, logger: slog.Default()}
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	patch := func(role, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("PATCH", "/api/v1/workspaces/"+ws, strings.NewReader(body))
		ctx := context.WithValue(r.Context(), ctxWorkspaceID, ws)
		ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: user})
		ctx = context.WithValue(ctx, ctxRole, role)
		w := httptest.NewRecorder()
		h.Update(w, r.WithContext(ctx))
		return w
	}
	for _, role := range []string{"MEMBER", "VIEWER", "MANAGER"} {
		if w := patch(role, `{"pages_theme":{"accent":"#123abc"}}`); w.Code != 403 {
			t.Fatalf("%s wrote theme: %d", role, w.Code)
		}
	}
	for _, value := range []string{`null`, `[]`, `{"accent":"red"}`, `{"accent":"url(https://example.com)"}`, `{"accent":"#abcdef;display:none"}`, `{"secret":"#abcdef"}`, `{"accent":17}`} {
		if w := patch("OWNER", `{"pages_theme":`+value+`}`); w.Code != 400 {
			t.Fatalf("invalid %s accepted: %d", value, w.Code)
		}
	}
	w := patch("OWNER", `{"pages_theme":{"accent":"#123abc","text":"#ffffff"}}`)
	if w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	var result workspaceResponse
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || !strings.Contains(string(result.PagesTheme), "#123abc") {
		t.Fatalf("theme missing in response: %s %v", w.Body, err)
	}
	// An unrelated update must preserve the palette, and list must expose it to members.
	if w := patch("ADMIN", `{"name":"New workspace name"}`); w.Code != 200 {
		t.Fatal(w.Body)
	}
	req := httptest.NewRequest("GET", "/api/v1/workspaces", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxUser, &AuthUser{ID: user}))
	list := httptest.NewRecorder()
	h.List(list, req)
	if list.Code != 200 || !strings.Contains(list.Body.String(), "#123abc") {
		t.Fatalf("palette not persisted/listed: %s", list.Body)
	}
	var audit int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE workspace_id=? AND action='workspace.update' AND metadata LIKE '%pages_theme%'`, ws).Scan(&audit); err != nil {
		t.Fatal(err)
	}
	if audit != 1 {
		t.Fatalf("theme audit count %d", audit)
	}
	if w := patch("OWNER", `{"pages_theme":{}}`); w.Code != 200 {
		t.Fatal(w.Body)
	}
	var raw string
	if err := db.QueryRow("SELECT pages_theme FROM workspaces WHERE id=?", ws).Scan(&raw); err != nil || raw != "{}" {
		t.Fatalf("reset: %s %v", raw, err)
	}
}
