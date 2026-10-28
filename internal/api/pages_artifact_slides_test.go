package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/artifacts/slidespdf"
)

func slidesArtifactBody(base int, title string) string {
	raw, _ := json.Marshal(map[string]any{"base_version": base, "entrypoint": "slides/intro.html", "files": []map[string]string{
		{"path": "deck.json", "media_type": "application/json", "content": `{"schema_version":1,"order":["intro"]}`},
		{"path": "slides/intro.html", "media_type": "text/html", "content": `<section id="intro"><p id="title" style="left:40px;top:40px;width:1800px;height:200px;font-size:32px;color:#111111;text-align:left">` + title + `</p></section>`},
	}})
	return string(raw)
}

func slidesAPIRequest(t *testing.T, h *PageHandler, ws, user, page, suffix string) *http.Request {
	t.Helper()
	r := httptest.NewRequest("GET", "/api/v1/pages/@"+page+"/artifact/"+suffix, nil)
	r.SetPathValue("slug", "@"+page)
	token := "crewship_cli_slides-read-" + user
	mustExec(t, h.db, `INSERT OR IGNORE INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES(?,?, 'slides read',?,datetime('now'))`, "slides-token-"+user, user, sha256Hex(token))
	r.Header.Set("Authorization", "Bearer "+token)
	ctx := withWorkspace(withUser(r.Context(), &AuthUser{ID: user}), ws, "OWNER")
	ctx = context.WithValue(ctx, ctxAuthKind, AuthKindCLIToken)
	return r.WithContext(ctx)
}

func slidesAPIFixture(t *testing.T) (*PageHandler, string, string, string, string) {
	t.Helper()
	h, ws, owner, call := artifactProjectFixture(t)
	t.Setenv("CREWSHIP_ARTIFACT_SLIDES_ENABLED", "true")
	w := call(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Slides",`+strings.TrimPrefix(slidesArtifactBody(0, "Žluťoučký kůň"), "{"))
	if w.Code != 201 {
		t.Fatalf("create %d %s", w.Code, w.Body)
	}
	if w := call(owner, "POST", "three-screens/artifact/share", `{"base_version":1,"version":1}`); w.Code != 200 {
		t.Fatalf("share %d %s", w.Code, w.Body)
	}
	if w := call(owner, "POST", "three-screens/artifact/versions", slidesArtifactBody(1, "Private version two")); w.Code != 200 {
		t.Fatalf("publish %d %s", w.Code, w.Body)
	}
	var page string
	if err := h.db.QueryRow(`SELECT id FROM pages WHERE slug='three-screens'`).Scan(&page); err != nil {
		t.Fatal(err)
	}
	reader := "slides-reader"
	mustExec(t, h.db, `INSERT INTO users(id,email) VALUES(?,'slides-reader@example.test')`, reader)
	mustExec(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, generateCUID(), ws, reader)
	mustExec(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES(?,'user',?,'read',?,datetime('now'))`, page, reader, owner)
	return h, ws, owner, reader, page
}

func TestArtifactSlidesSelectedProjectionAndPDF(t *testing.T) {
	h, ws, owner, reader, page := slidesAPIFixture(t)
	for _, tt := range []struct {
		user  string
		v     int
		want  int
		title string
	}{{owner, 1, 200, "Žluťoučký kůň"}, {owner, 2, 200, "Private version two"}, {reader, 1, 200, "Žluťoučký kůň"}, {reader, 2, 404, ""}} {
		w := httptest.NewRecorder()
		h.GetArtifactSlides(w, slidesAPIRequest(t, h, ws, tt.user, page, fmt.Sprintf("slides?version=%d", tt.v)))
		if w.Code != tt.want {
			t.Fatalf("projection %s/%d: %d %s", tt.user, tt.v, w.Code, w.Body)
		}
		if tt.want == 200 {
			var p artifacts.ArtifactSlidesProjection
			if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil || p.Deck.Slides[0].Elements[0].Text.Value != tt.title || p.Anchors["title"].Quote != tt.title {
				t.Fatalf("projection %#v %v", p, err)
			}
		}
	}
	w := httptest.NewRecorder()
	h.ExportArtifactSlidesPDF(w, slidesAPIRequest(t, h, ws, reader, page, "export.pdf?version=1"))
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/pdf" || !strings.HasPrefix(w.Body.String(), "%PDF-") || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("export %d %#v %s", w.Code, w.Header(), w.Body)
	}
	for path, handler := range map[string]http.HandlerFunc{"slides": h.GetArtifactSlides, "export.pdf": h.ExportArtifactSlidesPDF} {
		for _, query := range []string{"", "?version=0", "?version=9007199254740993", "?version=1&version=2"} {
			w := httptest.NewRecorder()
			handler(w, slidesAPIRequest(t, h, ws, owner, page, path+query))
			if w.Code != 400 {
				t.Fatalf("invalid pin %s%s: %d", path, query, w.Code)
			}
		}
	}
}

func TestArtifactSlidesRechecksAuthorityAfterActualRender(t *testing.T) {
	cases := []string{"grant", "token", "member", "shared pin", "owner mode", "flag", "project flag", "retention", "source replacement", "blob corruption"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			h, ws, owner, reader, page := slidesAPIFixture(t)
			user := reader
			if name == "owner mode" || name == "retention" {
				user = owner
			}
			if name == "flag" {
				t.Setenv("CREWSHIP_ARTIFACT_SLIDES_ENABLED", "")
				mustExec(t, h.db, `INSERT OR IGNORE INTO feature_flags(id,key,description,enabled,created_at,updated_at) VALUES('ff_artifact_slides','artifact_slides','Slides',0,datetime('now'),datetime('now'))`)
				mustExec(t, h.db, `INSERT INTO feature_flag_overrides(id,flag_id,workspace_id,enabled) VALUES('slides-enabled','ff_artifact_slides',?,1)`, ws)
			}
			w := httptest.NewRecorder()
			calls := 0
			request := slidesAPIRequest(t, h, ws, user, page, "export.pdf?version=1")
			h.serveArtifactSlides(w, request, true, func(ctx context.Context, p artifacts.ArtifactSlidesProjection) ([]byte, error) {
				calls++
				if w.Body.Len() != 0 || w.Header().Get("Content-Type") != "" || w.Header().Get("Content-Disposition") != "" {
					t.Fatal("response committed before final authority")
				}
				data, err := slidespdf.Render(ctx, p.Deck)
				if err != nil {
					return nil, err
				}
				switch name {
				case "grant":
					mustExec(t, h.db, `DELETE FROM page_grants WHERE page_id=? AND subject_id=?`, page, reader)
				case "token":
					mustExec(t, h.db, `UPDATE cli_tokens SET revoked_at=datetime('now') WHERE user_id=?`, reader)
				case "member":
					mustExec(t, h.db, `DELETE FROM workspace_members WHERE workspace_id=? AND user_id=?`, ws, reader)
				case "shared pin":
					mustExec(t, h.db, `UPDATE artifact_version_heads SET shared_version=2 WHERE workspace_id=? AND page_id=?`, ws, page)
				case "owner mode":
					mustExec(t, h.db, `UPDATE pages SET owner_user_id=? WHERE id=?`, reader, page)
					mustExec(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES(?,'user',?,'read',?,datetime('now'))`, page, owner, reader)
				case "flag":
					mustExec(t, h.db, `UPDATE feature_flag_overrides SET enabled=0 WHERE id='slides-enabled'`)
				case "project flag":
					t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "")
				case "retention":
					mustExec(t, h.db, `UPDATE artifact_version_heads SET shared_version=2 WHERE page_id=?`, page)
					mustExec(t, h.db, `DELETE FROM artifact_versions WHERE page_id=? AND version=1`, page)
				case "source replacement", "blob corruption":
					var original []byte
					if err := h.db.QueryRow(`SELECT b.content FROM artifact_versions v JOIN artifact_version_blobs b ON b.workspace_id=v.workspace_id AND b.page_id=v.page_id AND b.hash=json_extract(v.manifest_json,'$.files."slides/intro.html".hash') WHERE v.workspace_id=? AND v.page_id=? AND v.version=1`, ws, page).Scan(&original); err != nil {
						t.Fatal(err)
					}
					changed := strings.ReplaceAll(string(original), "Žluťoučký kůň", "Different title")
					if changed == string(original) {
						t.Fatal("replacement fixture did not change source")
					}
					if name == "source replacement" {
						hash := sha256Hex(changed)
						mustExec(t, h.db, `INSERT INTO artifact_version_blobs(workspace_id,page_id,hash,content) VALUES(?,?,?,?)`, ws, page, hash, []byte(changed))
						mustExec(t, h.db, `UPDATE artifact_versions SET manifest_json=json_set(manifest_json,'$.files."slides/intro.html".hash',?) WHERE workspace_id=? AND page_id=? AND version=1`, hash, ws, page)
						// A fresh request is still authorized and has a valid new source
						// at this same number; only releasing the old render is forbidden.
						key := artifacts.VersionKey{WorkspaceID: ws, PageID: page}
						fresh, err := h.artifactProjectStore(ctx).View(ctx, key, h.artifactSlidesAuthority(request, key, 1))
						if err != nil {
							t.Fatalf("fresh replacement calibration: %v", err)
						}
						if _, err := artifacts.ParseSlidesArtifact(page, fresh.State.Manifest.Files); err != nil {
							t.Fatalf("replacement is not valid Slides: %v", err)
						}
					} else {
						mustExec(t, h.db, `UPDATE artifact_version_blobs SET content=? WHERE workspace_id=? AND page_id=? AND hash=?`, []byte(changed), ws, page, sha256Hex(string(original)))
					}
				}
				return data, nil
			})
			if calls != 1 {
				t.Fatalf("render calls=%d response=%d %s", calls, w.Code, w.Body)
			}
			if w.Code == 200 || strings.Contains(w.Body.String(), "%PDF-") || w.Header().Get("Content-Type") == "application/pdf" {
				t.Fatalf("revoked snapshot escaped %d %#v %s", w.Code, w.Header(), w.Body)
			}
			if name == "source replacement" && w.Code != 403 {
				t.Fatalf("source replacement must fail pinned selection: %d %s", w.Code, w.Body)
			}
			if name == "blob corruption" && (w.Code != 422 || !strings.Contains(w.Body.String(), "artifact_slides_unsupported")) {
				t.Fatalf("corrupt content address must fail integrity: %d %s", w.Code, w.Body)
			}
		})
	}
}

func TestArtifactSlidesDefaultOffAndWorkspaceOverride(t *testing.T) {
	h, ws, owner, _, page := slidesAPIFixture(t)
	t.Setenv("CREWSHIP_ARTIFACT_SLIDES_ENABLED", "")
	var enabled bool
	if err := h.db.QueryRow(`SELECT enabled FROM feature_flags WHERE key='artifact_slides'`).Scan(&enabled); err != nil || enabled {
		t.Fatalf("default-off migration: %v %v", enabled, err)
	}
	request := func() int {
		w := httptest.NewRecorder()
		h.GetArtifactSlides(w, slidesAPIRequest(t, h, ws, owner, page, "slides?version=1"))
		return w.Code
	}
	if got := request(); got != 404 {
		t.Fatalf("default off %d", got)
	}
	mustExec(t, h.db, `INSERT INTO feature_flag_overrides(id,flag_id,workspace_id,enabled) SELECT 'slides-on',id,?,1 FROM feature_flags WHERE key='artifact_slides'`, ws)
	if got := request(); got != 200 {
		t.Fatalf("workspace override %d", got)
	}
	t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "")
	mustExec(t, h.db, `UPDATE feature_flags SET enabled=0 WHERE key='artifact_projects'`)
	if got := request(); got != 404 {
		t.Fatalf("Project dependency %d", got)
	}
}

func TestArtifactSlidesPDFEnvelopeEncodedBeforeFinalAuthority(t *testing.T) {
	h, ws, _, reader, page := slidesAPIFixture(t)
	w := httptest.NewRecorder()
	processed := false
	h.serveArtifactSlides(w, slidesAPIRequest(t, h, ws, reader, page, "export.pdf?version=1"), false, func(ctx context.Context, p artifacts.ArtifactSlidesProjection) ([]byte, error) {
		processed = true
		encoded, err := artifactSlidesPDFProcess(1)(ctx, p)
		if err != nil {
			return nil, err
		}
		var receipt artifacts.ArtifactSlidesPDFResponse
		if json.Unmarshal(encoded, &receipt) != nil || receipt.Encoding != "base64" || receipt.ByteLength == 0 {
			t.Fatal("PDF envelope not encoded")
		}
		if w.Body.Len() != 0 || w.Header().Get("Content-Type") != "" {
			t.Fatal("response escaped before final authority")
		}
		mustExec(t, h.db, `DELETE FROM page_grants WHERE page_id=? AND subject_id=?`, page, reader)
		return encoded, nil
	})
	if !processed || (w.Code != 403 && w.Code != 404) || strings.Contains(w.Body.String(), `"data"`) || strings.Contains(w.Body.String(), "JVBER") {
		t.Fatalf("encoded PDF escaped revoked reader: %d %s", w.Code, w.Body)
	}
}
