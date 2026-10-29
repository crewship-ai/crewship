package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/mooring"
)

func TestCraftNativeDocumentRoutesMissingProfileFailsClosedOverTCP(t *testing.T) {
	f, _, _ := craftNativeHTTPFixture(t)
	bearer := mintIngressScopedToken(t, f.db, f.userID, "native-document-route-missing-profile", "pages:*")
	router, err := NewRouter(f.db, "native-document-route-secret-32chars", newTestLogger(), WithCraftNativeHTTP(CraftNativeHTTPConfig{Enabled: true}))
	if err != nil {
		t.Fatal(err)
	}
	defer router.Shutdown()
	server := httptest.NewServer(router)
	defer server.Close()
	request := func(method, path, admission, body string) (int, []byte) {
		req, err := http.NewRequest(method, server.URL+"/api/v1/pages/@native-page/craft/"+path+"?workspace_id="+f.wsID, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Craft-Session", admission)
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, raw
	}
	status, raw := request("POST", "sessions", "", "{}")
	if status != 201 {
		t.Fatalf("session positive calibration: %d %s", status, raw)
	}
	var session struct {
		Token string `json:"session_token"`
	}
	if err = json.Unmarshal(raw, &session); err != nil || len(session.Token) != 64 {
		t.Fatal("invalid admission calibration")
	}
	for _, tc := range []struct{ method, path string }{{"GET", "document"}, {"POST", "document/save"}, {"POST", "document/import"}, {"POST", "document/restore"}, {"POST", "document/reconcile"}} {
		status, raw = request(tc.method, tc.path, session.Token, "{}")
		if status != 503 {
			t.Errorf("registered missing-profile route %s %s must failclosed503: got%d %s", tc.method, tc.path, status, raw)
		}
	}
}

// The injected validator exists only in this private protocol fixture: these
// synthetic JSON bytes are not represented as qualified native application data.
// Production profile construction always installs NativeCLIValidator.Validate.
func craftNativeDataTCPFixture(t *testing.T, injectValidator bool) (*pagesTransferRig, *Router, func(string, string, string, string, any) (int, json.RawMessage), string, string, mooring.NativeStoreConfig) {
	t.Helper()
	f, _, _ := craftNativeHTTPFixture(t)
	f.seedPage(t, "native-data-page", "native-data", "Native data", f.userID, "")
	mustExec(t, f.db, `UPDATE pages SET private_audience=1 WHERE id='native-data-page'`)
	mustExec(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('native-data-page','user',?,'write',?)`, f.adminID, f.userID)
	binary := filepath.Join(t.TempDir(), "fixture-cli")
	executable := []byte("#!/bin/sh\nexit 9\n")
	if err := os.WriteFile(binary, executable, 0700); err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(executable))
	profile := CraftNativeDocumentProfile{Application: "wordcraft", EngineRevision: strings.Repeat("a", 40), CLIPath: binary, CLISHA256: hash, EditorBundle: strings.Repeat("c", 64), EditorURL: "/_editor/b/" + strings.Repeat("c", 64) + "/index.html"}
	mustExec(t, f.db, `INSERT INTO craft_native_documents(page_id,workspace_id,application_id,engine_revision,engine_sha256,editor_bundle) VALUES('native-data-page',?,'wordcraft',?,?,?)`, f.wsID, profile.EngineRevision, profile.CLISHA256, profile.EditorBundle)
	router, err := NewRouter(f.db, "native-data-route-secret-32chars", newTestLogger(), WithCraftNativeHTTP(CraftNativeHTTPConfig{Enabled: true, Profiles: map[string]CraftNativeDocumentProfile{"wordcraft": profile}}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	p := router.craftNativeDocuments.applications["wordcraft"]
	if injectValidator {
		p.Store.Validate = func(_ context.Context, raw []byte) error {
			if !json.Valid(raw) {
				return mooring.ErrNativeValidation
			}
			return nil
		}
		router.craftNativeDocuments.applications["wordcraft"] = p
	}
	store := artifacts.NewVersionStore(f.db, artifacts.VersionOptions{Enabled: true, ContentType: "wordcraft"})
	if _, err = store.Apply(t.Context(), artifacts.VersionKey{WorkspaceID: f.wsID, PageID: "native-data-page"}, artifacts.VersionRequest{BaseVersion: 0, EntryPoint: "document.json", Files: map[string]artifacts.VersionFile{"document.json": {Content: []byte(`{"v":1}`), MediaType: "application/json"}}}, func(context.Context, *sql.Tx) (string, error) { return "user:" + f.userID, nil }); err != nil {
		t.Fatal(err)
	}
	owner := mintIngressScopedToken(t, f.db, f.userID, "native-data-route-owner", "pages:*")
	other := mintIngressScopedToken(t, f.db, f.adminID, "native-data-route-other", "pages:*")
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	client := server.Client()
	client.Timeout = 5 * time.Second
	call := func(method, path, bearer, admission string, body any) (int, json.RawMessage) {
		t.Helper()
		var raw []byte
		if body != nil {
			var err error
			raw, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		req, err := http.NewRequest(method, server.URL+"/api/v1/pages/@native-data-page/craft/"+path+"?workspace_id="+f.wsID, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("X-Craft-Session", admission)
		req.Header.Set("Content-Type", "application/json")
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(io.LimitReader(response.Body, 46<<20))
		if err != nil {
			t.Fatal(err)
		}
		if !json.Valid(data) {
			t.Fatalf("non-JSON response HTTP%d %q", response.StatusCode, data)
		}
		return response.StatusCode, data
	}
	return f, router, call, owner, other, p.Store
}
func craftNativeDataTCPAdmission(t *testing.T, call func(string, string, string, string, any) (int, json.RawMessage), bearer string) (string, string) {
	t.Helper()
	status, raw := call("POST", "sessions", bearer, "", struct{}{})
	if status != 201 {
		t.Fatalf("session %d %s", status, raw)
	}
	var out struct {
		Token string `json:"session_token"`
		ID    string `json:"session_id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Token) != 64 || out.Token == out.ID {
		t.Fatal("invalid session")
	}
	return out.Token, out.ID
}
func craftNativeDataTCPBody(f *pagesTransferRig, cfg mooring.NativeStoreConfig, session, generation, base, revision string, raw []byte) map[string]any {
	return map[string]any{"workspaceId": f.wsID, "pageId": "native-data-page", "applicationId": "wordcraft", "enginePin": mooring.NewNativeStore(nil, cfg).EnginePin(), "sessionId": session, "generation": generation, "baseVersion": base, "editorRevision": revision, "contentHash": fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), "bytes": raw}
}
func TestCraftNativeDocumentRoutesTwoHumansSaveLostACKRestoreAndStaleGeneration(t *testing.T) {
	f, _, call, owner, other, cfg := craftNativeDataTCPFixture(t, true)
	at, aid := craftNativeDataTCPAdmission(t, call, owner)
	bt, bid := craftNativeDataTCPAdmission(t, call, other)
	if status, raw := call("POST", "lease", owner, at, struct{}{}); status != 200 {
		t.Fatalf("acquire %d %s", status, raw)
	}
	if status, raw := call("GET", "document", owner, at, nil); status != 200 {
		t.Fatalf("read %d %s", status, raw)
	}
	body := craftNativeDataTCPBody(f, cfg, aid, "1", "1", "opaque:first:1", []byte(`{"v":2}`))
	status, raw := call("POST", "document/save", owner, at, body)
	if status != 200 {
		t.Fatalf("save %d %s", status, raw)
	}
	var saved mooring.NativeSaveReceipt
	if err := json.Unmarshal(raw, &saved); err != nil || saved.Version != "2" || saved.Generation != "1" || saved.SessionID != aid || saved.ContentHash != body["contentHash"] {
		t.Fatalf("unbound save receipt %+v %v", saved, err)
	}
	newer := craftNativeDataTCPBody(f, cfg, aid, "1", "2", "opaque:next:1", []byte(`{"v":3}`))
	if status, raw = call("POST", "document/import", owner, at, newer); status != 200 {
		t.Fatalf("import %d %s", status, raw)
	}
	// Discard the first ACK, then reconcile the exact attempt after a newer commit.
	status, raw = call("POST", "document/reconcile", owner, at, body)
	if status != 200 {
		t.Fatalf("reconcile %d %s", status, raw)
	}
	var reconciled mooring.NativeSaveReceipt
	if err := json.Unmarshal(raw, &reconciled); err != nil || !reflect.DeepEqual(saved, reconciled) {
		t.Fatalf("reconcile adopted latest or changed binding: %+v %v", reconciled, err)
	}
	restore := craftNativeDataTCPBody(f, cfg, aid, "1", "3", "opaque:restore:1", nil)
	restore["targetVersion"] = "1"
	if status, raw = call("POST", "document/restore", owner, at, restore); status != 200 || !bytes.Contains(raw, []byte(`"version":"4"`)) {
		t.Fatalf("restore %d %s", status, raw)
	}
	status, raw = call("POST", "lease/request-handoff", other, bt, struct{}{})
	if status != 201 {
		t.Fatalf("request handoff %d %s", status, raw)
	}
	var requested struct {
		ID string `json:"request_id"`
	}
	if err := json.Unmarshal(raw, &requested); err != nil || requested.ID == "" {
		t.Fatal("missing target request")
	}
	if status, raw = call("POST", "lease/handoff", owner, at, map[string]string{"generation": "1", "request_id": requested.ID}); status != 200 {
		t.Fatalf("handoff %d %s", status, raw)
	}
	stale := craftNativeDataTCPBody(f, cfg, aid, "1", "4", "opaque:stale:1", []byte(`{"v":9}`))
	if status, raw = call("POST", "document/save", owner, at, stale); status != 409 {
		t.Fatalf("stale generation %d %s", status, raw)
	}
	status, raw = call("GET", "document", other, bt, nil)
	if status != 200 {
		t.Fatalf("read restored %d %s", status, raw)
	}
	var read struct {
		Version      string `json:"version"`
		Bytes        []byte `json:"bytes"`
		EditorURL    string `json:"editorURL"`
		EditorBundle string `json:"editorBundle"`
	}
	if err := json.Unmarshal(raw, &read); err != nil || read.Version != "4" || string(read.Bytes) != `{"v":1}` || read.EditorURL != "/_editor/b/"+cfg.EditorBundle+"/index.html" {
		t.Fatalf("restored read %+v %v", read, err)
	}
	fresh := craftNativeDataTCPBody(f, cfg, bid, "2", "4", "opaque:new-holder:1", []byte(`{"v":5}`))
	if status, raw = call("POST", "document/save", other, bt, fresh); status != 200 {
		t.Fatalf("new holder save %d %s", status, raw)
	}
	var head int
	if err := f.db.QueryRow(`SELECT latest_version FROM artifact_version_heads WHERE workspace_id=? AND page_id='native-data-page'`, f.wsID).Scan(&head); err != nil || head != 5 {
		t.Fatalf("head %d %v", head, err)
	}
}
func TestCraftNativeDocumentRoutesProductionValidatorRefusesSyntheticNativeAndOverrides(t *testing.T) {
	f, _, call, owner, _, cfg := craftNativeDataTCPFixture(t, false)
	token, id := craftNativeDataTCPAdmission(t, call, owner)
	if status, raw := call("POST", "lease", owner, token, struct{}{}); status != 200 {
		t.Fatalf("acquire %d %s", status, raw)
	}
	body := craftNativeDataTCPBody(f, cfg, id, "1", "1", "opaque:invalid", []byte(`{"v":2}`))
	if status, raw := call("POST", "document/save", owner, token, body); status != 422 {
		t.Fatalf("production CLI must refuse synthetic model %d %s", status, raw)
	}
	body["CLIPath"] = "/bin/true"
	if status, raw := call("POST", "document/import", owner, token, body); status != 400 {
		t.Fatalf("request cannot select validator %d %s", status, raw)
	}
	var head int
	if err := f.db.QueryRow(`SELECT latest_version FROM artifact_version_heads WHERE workspace_id=? AND page_id='native-data-page'`, f.wsID).Scan(&head); err != nil || head != 1 {
		t.Fatalf("invalid native changed head %d %v", head, err)
	}
}
