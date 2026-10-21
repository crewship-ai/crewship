package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestPagesCatalogPersonalFilterAndPagination(t *testing.T) {
	h, _, _, workspace, owner := newPagesFixture(t)
	pagesSeedUser(t, h, workspace, "personal-author", "personal-author@example.test", "MEMBER")
	for _, slug := range []string{"a-own-private", "b-own-private", "c-foreign-private", "d-own-legacy"} {
		page := pagesCreate(t, h, workspace, owner, slug)
		author, private := "personal-author", true
		if slug == "c-foreign-private" {
			author = owner
		}
		if slug == "d-own-legacy" {
			private = false
		}
		execOrFatal(t, h.db, `UPDATE pages SET owner_user_id=?, private_audience=? WHERE id=?`, author, private, page["id"])
	}
	for _, tc := range []struct {
		name, user, role, query, count string
		slugs                          []string
		status                         int
	}{
		{"author page", "personal-author", "MEMBER", "audience=personal&limit=1&offset=1", "2", []string{"b-own-private"}, 200},
		{"author out of range", "personal-author", "MEMBER", "audience=personal&limit=1&offset=900", "2", nil, 200},
		{"owner personal excludes custody", owner, "OWNER", "audience=personal", "1", []string{"c-foreign-private"}, 200},
		{"empty filtered count", "personal-author", "MEMBER", "audience=personal&offset=9223372036854775807", "2", nil, 200},
		{"unknown audience", owner, "OWNER", "audience=someone-else", "", nil, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := pagesRequest(t, "GET", "/api/v1/pages?"+tc.query, workspace, tc.user, tc.role, "")
			response := httptest.NewRecorder()
			h.List(response, request)
			if response.Code != tc.status {
				t.Fatalf("HTTP %d: %s", response.Code, response.Body.String())
			}
			if tc.status != 200 {
				return
			}
			if response.Header().Get("X-Total-Count") != tc.count {
				t.Errorf("authorized filtered count=%q want %s", response.Header().Get("X-Total-Count"), tc.count)
			}
			var rows []struct {
				Slug string `json:"slug"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &rows); err != nil {
				t.Fatal(err)
			}
			if len(rows) != len(tc.slugs) {
				t.Fatalf("rows=%s want %v", response.Body.String(), tc.slugs)
			}
			for i, row := range rows {
				if row.Slug != tc.slugs[i] {
					t.Errorf("row %d=%s want %s", i, row.Slug, tc.slugs[i])
				}
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Error("catalog private metadata must not be cached")
			}
		})
	}
}
