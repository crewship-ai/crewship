package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPageImmutableReference(t *testing.T) {
	h, ws, author, _, create := documentCreationFixture(t)
	creationExpect(t, create("reference", documentCreationBody), 201)
	var id string
	if err := h.db.QueryRow(`SELECT id FROM pages WHERE workspace_id=? AND slug='created-doc'`, ws).Scan(&id); err != nil {
		t.Fatal(err)
	}
	pagesSeedUser(t, h, ws, "reference-admin", "admin@reference.example", "ADMIN")
	for _, tc := range []struct {
		name, workspace, user, role, ref string
		status                           int
	}{
		{"author stable ID", ws, author, "MEMBER", "@" + id, 200},
		{"legacy slug", ws, author, "MEMBER", "created-doc", 200},
		{"private administrator", ws, "reference-admin", "ADMIN", "@" + id, 404},
		{"foreign workspace", "foreign", author, "MEMBER", "@" + id, 404},
		{"empty immutable reference", ws, author, "MEMBER", "@", 404},
		{"invalid immutable reference", ws, author, "MEMBER", "@@" + id, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := pagesGetStatus(t, h, tc.workspace, tc.user, tc.role, tc.ref)
			if r.Code != tc.status {
				t.Fatalf("status %d want%d: %s", r.Code, tc.status, r.Body.String())
			}
			if tc.status == 200 {
				var page map[string]any
				if json.Unmarshal(r.Body.Bytes(), &page) != nil || page["id"] != id {
					t.Fatal("reference changed identity")
				}
			}
		})
	}
	// A deleted saved identity must not follow a new object reusing its address.
	execOrFatal(t, h.db, `DELETE FROM pages WHERE id=?`, id)
	execOrFatal(t, h.db, `INSERT INTO pages (id,workspace_id,slug,name,owner_user_id,spec_json) VALUES ('replacement-page',?,'created-doc','Replacement title',?,'{}')`, ws, author)
	r := pagesGetStatus(t, h, ws, author, "MEMBER", "@"+id)
	if r.Code != 404 || strings.Contains(r.Body.String(), "Replacement title") {
		t.Fatalf("deleted identity substituted another object: %d %s", r.Code, r.Body.String())
	}
	if r := pagesGetStatus(t, h, ws, author, "MEMBER", "created-doc"); r.Code != 200 {
		t.Fatalf("legacy address should independently reach replacement: %d", r.Code)
	}
}
