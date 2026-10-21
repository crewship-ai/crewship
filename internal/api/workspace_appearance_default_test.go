package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateWorkspaceAppearanceDefault(t *testing.T) {
	long := strings.Repeat("a", 2100)
	cases := []struct {
		name string
		raw  string
		ok   bool
	}{
		{"empty object resets", `{}`, true},
		{"full", `{"style":"harbor-night","knobs":{"radius":"sharp","cards":"shadow"},"accent":"teal","theme":"dark"}`, true},
		{"workspace accent", `{"accent":"workspace"}`, true},
		{"theme system", `{"theme":"system"}`, true},
		{"only knobs", `{"knobs":{}}`, true},
		{"null", `null`, false},
		{"array", `[]`, false},
		{"string", `"harbor"`, false},
		{"unknown key", `{"density":"compact"}`, false},
		{"style uppercase", `{"style":"Harbor"}`, false},
		{"style too long", `{"style":"` + strings.Repeat("a", 33) + `"}`, false},
		{"style not string", `{"style":3}`, false},
		{"accent css", `{"accent":"url(x)"}`, false},
		{"theme unknown", `{"theme":"sepia"}`, false},
		{"knobs not object", `{"knobs":"x"}`, false},
		{"knob value not string", `{"knobs":{"radius":1}}`, false},
		{"knob key bad", `{"knobs":{"Bad Key":"x"}}`, false},
		{"knob value too long", `{"knobs":{"radius":"` + strings.Repeat("a", 65) + `"}}`, false},
		{"too many knobs", `{"knobs":{` + manyKnobs(17) + `}}`, false},
		{"16 knobs", `{"knobs":{` + manyKnobs(16) + `}}`, true},
		{"over 2 KiB", `{"style":"harbor","pad":"` + long + `"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateWorkspaceAppearanceDefault(json.RawMessage(tc.raw))
			if (err == nil) != tc.ok {
				t.Fatalf("ok=%v, err=%v", tc.ok, err)
			}
		})
	}
}

func manyKnobs(n int) string {
	parts := make([]string, 0, n)
	for i := 0; i < n; i++ {
		parts = append(parts, `"k`+strings.Repeat("a", i)+`":"v"`)
	}
	return strings.Join(parts, ",")
}

func TestWorkspaceAppearanceDefault(t *testing.T) {
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
	const value = `{"style":"slate","knobs":{"radius":"sharp"},"accent":"indigo","theme":"dark"}`
	for _, role := range []string{"MEMBER", "VIEWER", "MANAGER"} {
		if w := patch(role, `{"appearance_default":`+value+`}`); w.Code != 403 {
			t.Fatalf("%s wrote the default: %d", role, w.Code)
		}
	}
	if w := patch("OWNER", `{"appearance_default":{"theme":"sepia"}}`); w.Code != 400 {
		t.Fatalf("invalid value accepted: %d", w.Code)
	}
	for _, role := range []string{"ADMIN", "OWNER"} {
		w := patch(role, `{"appearance_default":`+value+`}`)
		if w.Code != 200 {
			t.Fatalf("%s save: %d %s", role, w.Code, w.Body)
		}
		var res workspaceResponse
		if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || !strings.Contains(string(res.AppearanceDefault), `"slate"`) {
			t.Fatalf("default missing in response: %s %v", w.Body, err)
		}
	}
	// An unrelated update keeps it; a member sees it in the list.
	if w := patch("ADMIN", `{"name":"Renamed"}`); w.Code != 200 {
		t.Fatal(w.Body)
	}
	req := httptest.NewRequest("GET", "/api/v1/workspaces", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxUser, &AuthUser{ID: user}))
	list := httptest.NewRecorder()
	h.List(list, req)
	if list.Code != 200 || !strings.Contains(list.Body.String(), `"appearance_default":{"style":"slate"`) {
		t.Fatalf("default not listed: %s", list.Body)
	}
	var audit int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE workspace_id=? AND action='workspace.update' AND metadata LIKE '%appearance_default%'`, ws).Scan(&audit); err != nil || audit != 2 {
		t.Fatalf("audit count %d %v", audit, err)
	}
	if w := patch("ADMIN", `{"appearance_default":{}}`); w.Code != 200 {
		t.Fatal(w.Body)
	}
	var raw string
	if err := db.QueryRow("SELECT appearance_default FROM workspaces WHERE id=?", ws).Scan(&raw); err != nil || raw != "{}" {
		t.Fatalf("reset: %s %v", raw, err)
	}
}
