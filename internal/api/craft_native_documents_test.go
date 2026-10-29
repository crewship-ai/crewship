package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/mooring"
)

func craftNativeDocumentsFixture(t *testing.T) (*pagesTransferRig, *craftNativeHTTP, *craftNativeDocumentsHTTP, context.Context, string, string, map[string]any) {
	t.Helper()
	f, h, ctx := craftNativeHTTPFixture(t)
	token, id := craftNativeIssued(t, h, ctx)
	if w := craftNativeLeaseCall(h, ctx, token, "native-page", "acquire", `{}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	cfg := mooring.NativeStoreConfig{Application: "wordcraft", EngineRevision: strings.Repeat("a", 40), EngineSHA256: strings.Repeat("b", 64), EditorBundle: strings.Repeat("c", 64), Validate: func(context.Context, []byte) error { return nil }}
	d := newCraftNativeDocumentsHTTP(h, map[string]craftNativeApplication{"wordcraft": {Store: cfg, EditorURL: "/workbook-editor/b/" + cfg.EditorBundle + "/index.html"}})
	s := artifacts.NewVersionStore(f.db, artifacts.VersionOptions{Enabled: true, ContentType: "wordcraft"})
	if _, err := s.Apply(t.Context(), artifacts.VersionKey{WorkspaceID: f.wsID, PageID: "native-page"}, artifacts.VersionRequest{BaseVersion: 0, EntryPoint: "document.json", Files: map[string]artifacts.VersionFile{"document.json": {Content: []byte(`{"v":1}`), MediaType: "application/json"}}}, func(context.Context, *sql.Tx) (string, error) { return "user:" + f.userID, nil }); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"v":2}`)
	body := map[string]any{"workspaceId": f.wsID, "pageId": "native-page", "applicationId": "wordcraft", "enginePin": mooring.NewNativeStore(nil, cfg).EnginePin(), "sessionId": id, "generation": "1", "baseVersion": "1", "editorRevision": "10:47:3", "contentHash": fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), "bytes": raw}
	return f, h, d, ctx, token, id, body
}
func craftNativeDocumentCall(t *testing.T, d *craftNativeDocumentsHTTP, ctx context.Context, token, operation string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/pages/native-page/craft/document/"+operation, strings.NewReader(string(raw))).WithContext(ctx)
	r.SetPathValue("slug", "native-page")
	r.Header.Set("X-Craft-Session", token)
	w := httptest.NewRecorder()
	switch operation {
	case "read":
		d.readSnapshot(w, r)
	case "save":
		d.saveSnapshot(w, r)
	case "import":
		d.importSnapshot(w, r)
	case "reconcile":
		d.reconcileSnapshot(w, r)
	case "restore":
		d.restoreSnapshot(w, r)
	}
	return w
}
func TestCraftNativeDocumentsWholeVersionAndExactLostACK(t *testing.T) {
	_, _, d, ctx, token, _, body := craftNativeDocumentsFixture(t)
	read := craftNativeDocumentCall(t, d, ctx, token, "read", nil)
	if read.Code != 200 {
		t.Fatal(read.Code, read.Body)
	}
	var opened struct {
		Version string `json:"version"`
		Bytes   []byte `json:"bytes"`
	}
	if err := json.Unmarshal(read.Body.Bytes(), &opened); err != nil || opened.Version != "1" || string(opened.Bytes) != `{"v":1}` {
		t.Fatalf("whole read: %+v %v", opened, err)
	}
	saved := craftNativeDocumentCall(t, d, ctx, token, "save", body)
	if saved.Code != 200 {
		t.Fatal(saved.Code, saved.Body)
	}
	var first mooring.NativeSaveReceipt
	if err := json.Unmarshal(saved.Body.Bytes(), &first); err != nil || first.Version != "2" || first.ContentHash != body["contentHash"] || first.Generation != "1" {
		t.Fatalf("unbound save receipt: %+v %v", first, err)
	}
	// Ignore the response as though it was lost, publish a newer head, then ask
	// for the original exact attempt. Reconciliation must not return the head.
	newer := make(map[string]any)
	for k, v := range body {
		newer[k] = v
	}
	newer["baseVersion"] = "2"
	newer["editorRevision"] = "10:48:3"
	if w := craftNativeDocumentCall(t, d, ctx, token, "save", newer); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	confirmed := craftNativeDocumentCall(t, d, ctx, token, "reconcile", body)
	var receipt mooring.NativeSaveReceipt
	if err := json.Unmarshal(confirmed.Body.Bytes(), &receipt); confirmed.Code != 200 || err != nil || receipt != first {
		t.Fatalf("wrong reconciled version: %d %+v %v", confirmed.Code, receipt, err)
	}
}
func TestCraftNativeDocumentsAllMutationsRejectAnotherTab(t *testing.T) {
	f, h, d, ctx, _, _, body := craftNativeDocumentsFixture(t)
	token, id := craftNativeIssued(t, h, ctx)
	body["sessionId"] = id
	for _, operation := range []string{"save", "import", "restore", "reconcile"} {
		t.Run(operation, func(t *testing.T) {
			candidate := make(map[string]any)
			for k, v := range body {
				candidate[k] = v
			}
			if operation == "restore" {
				candidate["targetVersion"] = "1"
			}
			w := craftNativeDocumentCall(t, d, ctx, token, operation, candidate)
			if w.Code != 409 {
				t.Fatalf("%s bypassed holder: %d %s", operation, w.Code, w.Body)
			}
		})
	}
	var count int
	if err := f.db.QueryRow(`SELECT count(*) FROM artifact_versions`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("unowned mutation published: %d %v", count, err)
	}
}
func TestCraftNativeDocumentsRejectForgedScopePinAndRevisionNumbers(t *testing.T) {
	_, _, d, ctx, token, _, body := craftNativeDocumentsFixture(t)
	for key, value := range map[string]any{"workspaceId": "other", "pageId": "other", "applicationId": "gridcraft", "enginePin": strings.Repeat("e", 64), "sessionId": "public-other", "generation": "01", "baseVersion": 1, "editorRevision": 9007199254740993} {
		t.Run(key, func(t *testing.T) {
			candidate := make(map[string]any)
			for k, v := range body {
				candidate[k] = v
			}
			candidate[key] = value
			if w := craftNativeDocumentCall(t, d, ctx, token, "save", candidate); w.Code < 400 || w.Code >= 500 {
				t.Fatalf("invalid %s admitted: %d %s", key, w.Code, w.Body)
			}
		})
	}
}

func TestCraftNativeDocumentsImportRestoreAndEngineFailure(t *testing.T) {
	_, _, d, ctx, token, _, body := craftNativeDocumentsFixture(t)
	profile := d.applications["wordcraft"]
	profile.Store.Validate = func(context.Context, []byte) error { return fmt.Errorf("unqualified model") }
	d.applications["wordcraft"] = profile
	if w := craftNativeDocumentCall(t, d, ctx, token, "import", body); w.Code != 422 {
		t.Fatalf("engine rejection status: %d %s", w.Code, w.Body)
	}
	profile.Store.Validate = func(context.Context, []byte) error { return nil }
	d.applications["wordcraft"] = profile
	if w := craftNativeDocumentCall(t, d, ctx, token, "import", body); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	body["baseVersion"] = "2"
	body["targetVersion"] = "1"
	restored := craftNativeDocumentCall(t, d, ctx, token, "restore", body)
	if restored.Code != 200 {
		t.Fatal(restored.Code, restored.Body)
	}
	read := craftNativeDocumentCall(t, d, ctx, token, "read", nil)
	var state struct {
		Version string `json:"version"`
		Bytes   []byte `json:"bytes"`
	}
	if err := json.Unmarshal(read.Body.Bytes(), &state); err != nil || state.Version != "3" || string(state.Bytes) != `{"v":1}` {
		t.Fatalf("restore must append exact original bytes: %+v %v", state, err)
	}
}
