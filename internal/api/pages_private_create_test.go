package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pages"
	"github.com/crewship-ai/crewship/internal/slides"
)

func TestPrivateEmptyPageCreationAndSlidesMetadata(t *testing.T) {
	f := newInstanceFixture(t)
	f.r.RegisterSlidesHuman(true)
	call := func(token, method, path, body string) *httptest.ResponseRecorder {
		return f.do(token, method, path+"?workspace_id=ws-old", body)
	}
	for _, tc := range []struct {
		name, fields string
		code         int
	}{
		{"private", `,"allow_empty":true`, 201},
		{"default", `,"allow_empty":true`, 201},
		{"missing-empty", ``, 400},
		{"crew", `,"allow_empty":true,"owner":"crew/missing"`, 400},
	} {
		path := "/api/v1/private-pages"
		if tc.name == "default" {
			path = "/api/v1/pages"
		}
		rr := call(f.boss, "POST", path, fmt.Sprintf(`{"slug":"%s","name":"Private title sentinel","panels":[]%s}`, tc.name, tc.fields))
		wantCode(t, rr, tc.code, tc.name)
		if tc.code != 201 {
			continue
		}
		var page map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if page["private_audience"] != (tc.name == "private") || page["has_slides"] != false || page["slides_schema"] != float64(0) {
			t.Fatalf("initial metadata %s", rr.Body)
		}
	}
	var pageID string
	if err := f.db.QueryRow(`SELECT id FROM pages WHERE workspace_id='ws-old' AND slug='private'`).Scan(&pageID); err != nil {
		t.Fatal(err)
	}
	var unsafe int
	if err := f.db.QueryRow(`SELECT (owner_user_id!='boss') OR owner_crew_id IS NOT NULL OR folder_id IS NOT NULL OR EXISTS(SELECT 1 FROM page_panels WHERE page_id=pages.id) OR EXISTS(SELECT 1 FROM page_grants WHERE page_id=pages.id) OR EXISTS(SELECT 1 FROM page_public_tokens WHERE page_id=pages.id) FROM pages WHERE id=?`, pageID).Scan(&unsafe); err != nil || unsafe != 0 {
		t.Fatalf("private creation widened audience: %d %v", unsafe, err)
	}
	body := humanSlidesJSON(t, slides.InitializeRequest{PageID: pageID, SchemaVersion: 1, MutationID: "first", Deck: humanSlidesDeck(t, pageID)})
	wantCode(t, call(f.boss, "POST", "/api/v1/pages/private/slides/initialize", body), 200, "real private create -> initialize")
	for _, schema := range []int{1, 2} {
		mustExec(t, f.db, `UPDATE slides_heads SET schema_version=? WHERE page_id=?`, schema, pageID)
		for _, list := range []bool{false, true} {
			path := "/api/v1/pages/private"
			if list {
				path = "/api/v1/pages"
			}
			rr := call(f.boss, "GET", path, "")
			wantCode(t, rr, 200, "authorized metadata")
			var page map[string]any
			if list {
				var rows []map[string]any
				if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
					t.Fatal(err)
				}
				for _, row := range rows {
					if row["id"] == pageID {
						page = row
					}
				}
			} else if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
			if page["has_slides"] != true || page["slides_schema"] != float64(schema) {
				t.Fatalf("truthful schema required: %s", rr.Body)
			}
		}
	}
	wantCode(t, call(f.wsAdmin, "GET", "/api/v1/pages/private", ""), 404, "non-owner denied")
	other := call(f.wsAdmin, "GET", "/api/v1/pages", "")
	wantCode(t, other, 200, "other list")
	if strings.Contains(other.Body.String(), pageID) || strings.Contains(other.Body.String(), `"slug":"private"`) {
		t.Fatalf("private metadata leaked: %s", other.Body)
	}
}

func TestPrivateEmptyPageRejectsAudienceAndAuthorFields(t *testing.T) {
	f := newInstanceFixture(t)
	for i, field := range []string{
		`"folder_id":"shared"`, `"folder":"shared"`, `"owner":"crew/missing"`,
		`"owner":"user/wsadmin"`, `"owner_user_id":"wsadmin"`, `"created_by":"wsadmin"`,
		`"public":true`, `"private_audience":false`, `"grants":[{"subject":"user/wsadmin"}]`,
		`"share":true`, `"public_token":"chosen"`, `"spec":{"panels":[]}`,
		`"allow_empty":false`, `"allow_empty":null`,
		`"Private":false`,
	} {
		t.Run(field, func(t *testing.T) {
			body := fmt.Sprintf(`{"slug":"denied-%d","name":"Private","allow_empty":true,"panels":[],%s}`, i, field)
			rr := f.do(f.boss, "POST", "/api/v1/private-pages?workspace_id=ws-old", body)
			wantCode(t, rr, 400, "ambiguous private audience rejected")
		})
	}
	var count int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM pages WHERE slug LIKE 'denied-%'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid creation persisted: %d %v", count, err)
	}
}

func TestPrivateEmptyPageRechecksAuthorityAfterBody(t *testing.T) {
	for _, change := range []string{"credential", "membership", "capability"} {
		t.Run(change, func(t *testing.T) {
			f := newInstanceFixture(t)
			req := httptest.NewRequest("POST", "/api/v1/private-pages?workspace_id=ws-old", nil)
			req.Header.Set("Authorization", "Bearer "+f.boss)
			req.Body = &slidesRevokingBody{Reader: strings.NewReader(`{"slug":"revoked-private","name":"Private","allow_empty":true,"panels":[]}`), once: func() {
				switch change {
				case "credential":
					mustExec(t, f.db, `UPDATE cli_tokens SET revoked_at='2000-01-01T00:00:00Z' WHERE token_hash=?`, sha256Hex(f.boss))
				case "membership":
					mustExec(t, f.db, `DELETE FROM workspace_members WHERE workspace_id='ws-old' AND user_id='boss'`)
				case "capability":
					mustExec(t, f.db, `UPDATE workspace_members SET role='MEMBER',capabilities='[]' WHERE workspace_id='ws-old' AND user_id='boss'`)
				}
			}}
			rr := httptest.NewRecorder()
			f.r.ServeHTTP(rr, req)
			if rr.Code < 400 {
				t.Fatalf("revoked authority created Page: %s", rr.Body)
			}
			var count int
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM pages WHERE slug='revoked-private'`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("revoked creation persisted: %d %v", count, err)
			}
		})
	}
}

func TestPrivateCreationFlagCannotReclassifyExistingPage(t *testing.T) {
	f := newInstanceFixture(t)
	wantCode(t, f.do(f.boss, "POST", "/api/v1/pages?workspace_id=ws-old", `{"slug":"legacy-empty","name":"Legacy","panels":[],"allow_empty":true}`), 201, "ordinary empty Page")
	for _, audience := range []string{"true", "false"} {
		rr := f.do(f.boss, "PATCH", "/api/v1/pages/legacy-empty?workspace_id=ws-old", `{"private":`+audience+`}`)
		wantCode(t, rr, 400, "creation-only audience flag")
	}
	var private bool
	if err := f.db.QueryRow(`SELECT private_audience FROM pages WHERE slug='legacy-empty'`).Scan(&private); err != nil || private {
		t.Fatalf("legacy audience changed: %v %v", private, err)
	}
}

func TestPrivatePageRejectsLegacyContentAudienceBypasses(t *testing.T) {
	f := newInstanceFixture(t)
	f.r.pages.SetProjectStore(&pages.ProjectStore{Directory: t.TempDir()})
	wmSeedPage(t, f.db, "ws-old", "private-shell", "private-shell", "Private", "boss", "")
	mustExec(t, f.db, `UPDATE pages SET private_audience=1 WHERE id='private-shell'`)
	mustExec(t, f.db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('lookout','ws-old','Lookout','lookout')`)
	var panelSpec pageWriteRequest
	if err := json.Unmarshal([]byte(pagesSpecBody("private-shell")), &panelSpec); err != nil {
		t.Fatal(err)
	}
	definition, valid := f.r.pages.documentFrom(httptest.NewRecorder(), &panelSpec, "")
	if !valid {
		t.Fatal("invalid fixture Page definition")
	}
	for _, tc := range []struct{ method, path, body string }{
		{"PUT", "/project", humanSlidesJSON(t, map[string]any{"expected_revision": 0, "project": projectTestSource(), "definition": definition})},
		{"POST", "/project/publish", `{}`},
		{"POST", "/public", `{}`},
		{"PUT", "/grants", `{"subject":"user/wsadmin","permission":"read"}`},
		{"PATCH", "", pagesSpecBody("private-shell")},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			rr := f.do(f.boss, tc.method, "/api/v1/pages/private-shell"+tc.path+"?workspace_id=ws-old", tc.body)
			wantCode(t, rr, 409, "unsupported private target type/audience")
		})
	}
}
