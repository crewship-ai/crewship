package api

import (
	"context"
	"net/http/httptest"
	"testing"
)

// New artifact → Docs sends exactly this empty-Page create before it
// initializes the Doc (#5153). A read-only role, or a member without
// page.create, is refused and nothing is stored, whatever the browser offers;
// a role that may create gets the Page.
func TestDocsCreationRefusesReadOnlyRole(t *testing.T) {
	const body = `{"name":"Team notes","slug":"team-notes","panels":[],"allow_empty":true}`
	for _, tc := range []struct {
		role, capabilities string
		status             int
	}{
		{"VIEWER", "", 403},
		{"MEMBER", "", 403},
		{"MEMBER", `["page.create"]`, 201},
		{"MANAGER", "", 201},
	} {
		t.Run(tc.role+tc.capabilities, func(t *testing.T) {
			h, _, _, ws, _ := newPagesFixture(t)
			user := "docs-" + tc.role
			pagesSeedUser(t, h, ws, user, user+"@example.test", tc.role)
			if tc.capabilities != "" {
				mustExec(t, h.db, `UPDATE workspace_members SET capabilities=? WHERE workspace_id=? AND user_id=?`, tc.capabilities, ws, user)
			}
			req := pagesRequest(t, "POST", "/api/v1/pages", ws, user, tc.role, body)
			req = req.WithContext(context.WithValue(req.Context(), ctxAuthKind, AuthKindSession))
			w := httptest.NewRecorder()
			h.Create(w, req)
			if w.Code != tc.status {
				t.Fatalf("%s create=%d want %d: %s", tc.role, w.Code, tc.status, w.Body)
			}
			var stored int
			if err := h.db.QueryRow(`SELECT COUNT(*) FROM pages WHERE workspace_id=? AND slug='team-notes'`, ws).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			if want := map[bool]int{true: 1, false: 0}[tc.status == 201]; stored != want {
				t.Fatalf("%s stored %d Pages, want %d", tc.role, stored, want)
			}
		})
	}
}
