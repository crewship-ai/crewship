package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/mooring"
)

func craftNativeInitializeTCP(t *testing.T, router *Router, workspace string) func(string, string, string, string, string, any) (int, json.RawMessage) {
	t.Helper()
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return func(method, page, operation, bearer, token string, body any) (int, json.RawMessage) {
		t.Helper()
		var raw []byte
		if body != nil {
			var err error
			raw, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		path := "/api/v1/pages/@" + page
		if operation != "" {
			path += "/craft/" + operation
		}
		req, err := http.NewRequest(method, server.URL+path+"?workspace_id="+workspace, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("X-Craft-Session", token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out, err := io.ReadAll(io.LimitReader(resp.Body, 46<<20))
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, out
	}
}
func craftNativeInitializeNoPartial(t *testing.T, f *pagesTransferRig, router *Router, page string) {
	t.Helper()
	for _, table := range []string{"craft_native_documents", "mooring_edit_leases", "artifact_version_heads", "artifact_versions"} {
		var count int
		if err := f.db.QueryRow("SELECT count(*) FROM "+table+" WHERE page_id=?", page).Scan(&count); err != nil || count != 0 {
			t.Errorf("partial durable %s rows=%d error=%v", table, count, err)
		}
	}
	m := router.craftNativeHTTP
	m.mu.Lock()
	ids, hashes := len(m.sessionsByID), len(m.sessionsByHash)
	m.mu.Unlock()
	if ids != 0 || hashes != 0 {
		t.Errorf("provisional admission leaked: IDs=%d hashes=%d", ids, hashes)
	}
}

func TestCraftNativeInitializeDisabledProfileFailsClosed(t *testing.T) {
	f, _, _ := craftNativeHTTPFixture(t)
	const page = "native-init-disabled"
	f.seedPage(t, page, page, "Disabled native initializer", f.userID, "")
	router, err := NewRouter(f.db, "native-init-unavailable-secret-32chars", newTestLogger(), WithCraftNativeHTTP(CraftNativeHTTPConfig{}))
	if err != nil {
		t.Fatal(err)
	}
	defer router.Shutdown()
	bearer := mintIngressScopedToken(t, f.db, f.userID, "native-init-unavailable-owner", "pages:*")
	call := craftNativeInitializeTCP(t, router, f.wsID)
	status, _ := call("POST", page, "initialize", bearer, "", map[string]any{"application_id": "wordcraft", "bytes": []byte(`{"v":1}`)})
	if status != 503 {
		t.Errorf("disabled initializer expected503 got%d", status)
	}
	craftNativeInitializeNoPartial(t, f, router, page)
}

// The private clock/validator are synthetic authority fixtures only. Production
// Router/CLI/whole-engine qualification is exercised by the Linux TCP tests.
func TestCraftNativeInitializeDeterministicOwnerAndRollbackAuthority(t *testing.T) {
	for _, application := range []string{"wordcraft", "gridcraft"} {
		t.Run(application, func(t *testing.T) {
			for _, scenario := range []string{"owner", "nonowner-writer", "legacy", "c1-causal", "c1-command", "final-revoke"} {
				t.Run(scenario, func(t *testing.T) {
					f, h, ctx := craftNativeHTTPFixture(t)
					const page = "native-init-synthetic"
					owner := f.userID
					if scenario == "nonowner-writer" {
						owner = f.adminID
					}
					f.seedPage(t, page, page, "Synthetic initializer authority", owner, "")
					if scenario == "nonowner-writer" {
						mustExec(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES(?,'user',?,'write',?)`, page, f.userID, f.adminID)
					}
					if scenario == "legacy" {
						f.seedCrew(t, "native-synthetic-crew", "Native synthetic", "native-synthetic-crew")
						f.seedPanel(t, "native-synthetic-panel", page, "native-synthetic-crew")
					}
					if strings.HasPrefix(scenario, "c1-") {
						craftNativeSeedCollaboration(t, f, page, scenario == "c1-command")
					}
					if scenario == "final-revoke" {
						mustExec(t, f.db, `CREATE TRIGGER native_init_synthetic_revoke AFTER INSERT ON artifact_versions WHEN NEW.page_id='native-init-synthetic' BEGIN DELETE FROM workspace_members WHERE workspace_id=NEW.workspace_id AND user_id=(SELECT owner_user_id FROM pages WHERE id=NEW.page_id); END`)
					}
					cfg := mooring.NativeStoreConfig{Application: application, EngineRevision: strings.Repeat("a", 40), EngineSHA256: strings.Repeat("b", 64), EditorBundle: strings.Repeat("c", 64), Validate: func(_ context.Context, raw []byte) error {
						if !json.Valid(raw) {
							return mooring.ErrNativeValidation
						}
						return nil
					}}
					d := newCraftNativeDocumentsHTTP(h, map[string]craftNativeApplication{application: {Store: cfg, EditorURL: "/_editor/b/" + cfg.EditorBundle + "/index.html"}})
					raw, err := json.Marshal(map[string]any{"application_id": application, "bytes": []byte(`{"v":1}`)})
					if err != nil {
						t.Fatal(err)
					}
					req := httptest.NewRequest("POST", "/api/v1/pages/@"+page+"/craft/initialize", bytes.NewReader(raw)).WithContext(ctx)
					req.SetPathValue("slug", "@"+page)
					out := httptest.NewRecorder()
					d.initialize(out, req)
					expected := 201
					if scenario == "nonowner-writer" || scenario == "final-revoke" {
						expected = 403
					}
					if scenario == "legacy" || strings.HasPrefix(scenario, "c1-") {
						expected = 409
					}
					if out.Code != expected {
						t.Fatalf("synthetic %s expected%d got%d", scenario, expected, out.Code)
					}
					if expected != 201 {
						craftNativeInitializeNoPartial(t, f, &Router{craftNativeHTTP: h}, page)
					} else {
						var receipt craftNativeInitializeResponse
						if err = json.Unmarshal(out.Body.Bytes(), &receipt); err != nil || receipt.ApplicationID != application || receipt.Version != "1" || receipt.Generation == "" || receipt.SessionID == "" || receipt.Token == receipt.SessionID {
							t.Fatal("synthetic initializer lost committed version/admission")
						}
					}
					if scenario == "final-revoke" {
						var exists int
						if err = f.db.QueryRow(`SELECT count(*) FROM workspace_members WHERE workspace_id=? AND user_id=?`, f.wsID, f.userID).Scan(&exists); err != nil || exists != 1 {
							t.Fatalf("synthetic final-authority rollback lost membership %d %v", exists, err)
						}
					}
				})
			}
		})
	}
}

// Synthetic persisted collaboration custody, including a checkpoint, is enough
// to make a Page occupied; this fixture does not qualify a C1 engine.
func craftNativeSeedCollaboration(t *testing.T, f *pagesTransferRig, page string, command bool) {
	t.Helper()
	profile, revision, hash := "mooring-causal-v1", "", ""
	if command {
		profile = "wordcraft-c1-v2"
		revision = "7ef2633a7e84956522d1ca47b99fe6feef16c1db"
		hash = "sha256:" + strings.Repeat("a", 64)
	}
	mustExec(t, f.db, `INSERT INTO mooring_collab_documents(page_id,workspace_id,application_id,revision,checkpoint_base64,format_profile,engine_revision,engine_sha256) VALUES(?,?,'wordcraft','0','e30=',?,?,?)`, page, f.wsID, profile, revision, hash)
	mustExec(t, f.db, `INSERT INTO mooring_collab_checkpoints(page_id,workspace_id,revision,checkpoint_base64,decoded_bytes) VALUES(?,?,'0','e30=',2)`, page, f.wsID)
	t.Cleanup(func() {
		var app, rev, checkpoint, gotProfile, gotRevision, gotHash string
		if err := f.db.QueryRow(`SELECT application_id,revision,checkpoint_base64,format_profile,engine_revision,engine_sha256 FROM mooring_collab_documents WHERE page_id=? AND workspace_id=?`, page, f.wsID).Scan(&app, &rev, &checkpoint, &gotProfile, &gotRevision, &gotHash); err != nil || app != "wordcraft" || rev != "0" || checkpoint != "e30=" || gotProfile != profile || gotRevision != revision || gotHash != hash {
			t.Errorf("existing collaboration custody changed: %s/%s/%s/%s/%s/%s error=%v", app, rev, checkpoint, gotProfile, gotRevision, gotHash, err)
		}
		var rows int
		if err := f.db.QueryRow(`SELECT count(*) FROM mooring_collab_checkpoints WHERE page_id=? AND workspace_id=? AND revision='0' AND checkpoint_base64='e30=' AND decoded_bytes=2`, page, f.wsID).Scan(&rows); err != nil || rows != 1 {
			t.Errorf("existing collaboration checkpoint changed: rows=%d error=%v", rows, err)
		}
	})
}
