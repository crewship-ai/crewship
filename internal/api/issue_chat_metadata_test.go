package api

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestIssueDetailChatMetadataMatchesRevisionAndViewer(t *testing.T) {
	h, user, workspace, crew, lead, _ := newTestIssueHandler(t)
	id := seedIssue(t, h.db, workspace, crew, lead, "META-1", "BACKLOG")
	for _, tc := range []struct {
		name, role string
		scoped     bool
		write      bool
	}{
		{"owner", "OWNER", false, true},
		{"member", "MEMBER", false, false},
		{"scoped owner", "OWNER", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			execOrFatal(t, h.db, `UPDATE workspace_members SET role=? WHERE workspace_id=? AND user_id=?`, tc.role, workspace, user)
			execOrFatal(t, h.db, `UPDATE missions SET title=? WHERE id=?`, tc.name, id)
			var revision, aggregate int64
			if err := h.db.QueryRowContext(t.Context(), `SELECT revision,aggregate_revision FROM missions WHERE id=?`, id).Scan(&revision, &aggregate); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("GET", "/", nil)
			req.SetPathValue("identifier", "META-1")
			req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: user}), workspace, tc.role))
			if tc.scoped {
				req = withTokenScopes(req, "agents:write")
			}
			rr := httptest.NewRecorder()
			h.GetByIdentifier(rr, req)
			if rr.Code != 200 {
				t.Fatalf("%d %s", rr.Code, rr.Body.String())
			}
			var issue issueResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &issue); err != nil {
				t.Fatal(err)
			}
			if issue.Title != tc.name || issue.Revision != revision || issue.AggregateRevision != aggregate {
				t.Fatalf("wrong snapshot: %+v", issue)
			}
			if !slices.Contains(issue.AllowedActions, "view") || slices.Contains(issue.AllowedActions, "edit_fields") != tc.write {
				t.Fatalf("actions=%v", issue.AllowedActions)
			}
		})
	}
}
