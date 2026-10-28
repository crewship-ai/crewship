package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

func dashboardBody(base int, collections ...string) string {
	var body map[string]any
	_ = json.Unmarshal([]byte(artifactProjectBody(base, "Dashboard")), &body)
	manifest, _ := json.Marshal(map[string]any{"schema_version": 1, "collections": collections})
	body["files"] = append(body["files"].([]any), map[string]any{"path": "dashboard.json", "media_type": "application/json", "content": string(manifest)})
	raw, _ := json.Marshal(body)
	return string(raw)
}

func TestDashboardDataFinalSelectionRejectsOwnerToReaderDowngrade(t *testing.T) {
	h, ws, owner, call := artifactProjectFixture(t)
	t.Setenv("CREWSHIP_ARTIFACT_DASHBOARD_DATA_ENABLED", "true")
	if w := call(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Dashboard",`+strings.TrimPrefix(dashboardBody(0, "sales"), "{")); w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	if w := call(owner, "POST", "three-screens/artifact/share", `{"base_version":1,"version":1}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	mustExec(t, h.db, `INSERT INTO users(id,email) VALUES('next-owner','next-owner@example.test')`)
	mustExec(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,'next-owner','MEMBER')`, generateCUID(), ws)
	var page string
	if err := h.db.QueryRow(`SELECT id FROM pages WHERE slug='three-screens'`).Scan(&page); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/v1/pages/three-screens/artifact/data/sales?version=1", nil)
	req.Header.Set("Authorization", "Bearer crewship_cli_artifact-project-owner")
	ctx := withWorkspace(withUser(req.Context(), &AuthUser{ID: owner}), ws, "OWNER")
	ctx = context.WithValue(ctx, ctxAuthKind, AuthKindCLIToken)
	req = req.WithContext(ctx)
	key := artifacts.VersionKey{WorkspaceID: ws, PageID: page}
	auth, _ := h.artifactDataAuthority(req, key, "sales", 1, false)
	tx, err := h.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	before, err := auth(ctx, tx)
	if err != nil {
		t.Fatalf("owner calibration: %v", err)
	}
	if _, err := tx.Exec(`UPDATE pages SET owner_user_id='next-owner' WHERE id=?`, page); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES(?,'user',?,'read','next-owner',datetime('now'))`, page, owner); err != nil {
		t.Fatal(err)
	}
	fresh, _ := h.artifactDataAuthority(req, key, "sales", 1, false)
	if after, err := fresh(ctx, tx); err != nil || after != before {
		t.Fatalf("reader must remain authorized as same actor: %q %v", after, err)
	}
	if _, err := auth(ctx, tx); !errors.Is(err, artifacts.ErrVersionAuthority) {
		t.Fatalf("mode downgrade reused owner selection: %v", err)
	}
}

func TestDashboardDataMaximumRowsAndValueBytesFitResponse(t *testing.T) {
	h, ws, owner, call := artifactProjectFixture(t)
	t.Setenv("CREWSHIP_ARTIFACT_DASHBOARD_DATA_ENABLED", "true")
	if w := call(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Dashboard",`+strings.TrimPrefix(dashboardBody(0, "sales"), "{")); w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	var page string
	if err := h.db.QueryRow(`SELECT id FROM pages WHERE slug='three-screens'`).Scan(&page); err != nil {
		t.Fatal(err)
	}
	mustExec(t, h.db, `INSERT INTO artifact_data_collections(workspace_id,page_id,name) VALUES(?,?,'sales')`, ws, page)
	seed := func(count int, value string) {
		t.Helper()
		tx, err := h.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		for i := 0; i < count; i++ {
			if _, err := tx.Exec(`INSERT INTO artifact_data_documents(workspace_id,page_id,collection,id,version,value_json,updated_by,updated_at) VALUES(?,?,'sales',?,1,?,'agent:private-agent:private-run',datetime('now'))`, ws, page, fmt.Sprintf("row-%04d", i), value); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	seed(artifacts.MaxDataDocuments, `{"n":1}`)
	w := dashboardRequest(t, h, ws, owner, "GET", "sales", "?version=1", "")
	var body artifactDataResponse
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Documents) != artifacts.MaxDataDocuments {
		t.Fatalf("maximum rows truncated: status%d rows%d", w.Code, len(body.Documents))
	}
	mustExec(t, h.db, `DELETE FROM artifact_data_documents WHERE page_id=?`, page)
	seed(32, `{"x":"`+strings.Repeat("<", artifacts.MaxDataDocumentBytes-8)+`"}`)
	w = dashboardRequest(t, h, ws, owner, "GET", "sales", "?version=1", "")
	if w.Code != 200 || w.Body.Len() > artifacts.MaxDataResponseBytes || strings.Contains(w.Body.String(), "private-agent") || strings.Contains(w.Body.String(), `\u003c`) {
		t.Fatalf("valid8MiB dataset response: status%d bytes%d", w.Code, w.Body.Len())
	}
}

func dashboardRequest(t *testing.T, h *PageHandler, ws, user, method, collection, query, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/api/v1/pages/three-screens/artifact/data/"+collection+query, strings.NewReader(body))
	token := "crewship_cli_dashboard_" + user
	mustExec(t, h.db, `INSERT OR IGNORE INTO cli_tokens(id,user_id,name,token_hash,created_at) VALUES(?,?,?, ?,datetime('now'))`, "dashboard-token-"+user, user, "dashboard", sha256Hex(token))
	req.Header.Set("Authorization", "Bearer "+token)
	ctx := withWorkspace(withUser(req.Context(), &AuthUser{ID: user}), ws, "OWNER")
	ctx = context.WithValue(ctx, ctxAuthKind, AuthKindCLIToken)
	req = req.WithContext(ctx)
	req.SetPathValue("slug", "three-screens")
	req.SetPathValue("collection", collection)
	w := httptest.NewRecorder()
	if method == "POST" {
		h.BatchArtifactData(w, req)
	} else if collection == "" {
		h.ListArtifactData(w, req)
	} else {
		h.GetArtifactData(w, req)
	}
	return w
}

func TestDashboardDataAPIAtomicCASAndCodeIndependence(t *testing.T) {
	h, ws, owner, call := artifactProjectFixture(t)
	t.Setenv("CREWSHIP_ARTIFACT_DASHBOARD_DATA_ENABLED", "true")
	if w := call(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Dashboard",`+strings.TrimPrefix(dashboardBody(0, "sales"), "{")); w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	batch := `{"base_version":1,"documents":[{"id":"row","ifVersion":0,"value":{"amount":12}}]}`
	if w := dashboardRequest(t, h, ws, owner, "POST", "sales", "", batch); w.Code != 200 {
		t.Fatalf("batch: %d %s", w.Code, w.Body)
	}
	if w := dashboardRequest(t, h, ws, owner, "POST", "sales", "", batch); w.Code != 409 {
		t.Fatalf("CAS: %d %s", w.Code, w.Body)
	}
	w := dashboardRequest(t, h, ws, owner, "GET", "sales", "?version=1", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"amount":12`) || strings.Contains(w.Body.String(), "updated_by") {
		t.Fatalf("read: %d %s", w.Code, w.Body)
	}
	var latest int64
	if err := h.db.QueryRow(`SELECT latest_version FROM artifact_version_heads WHERE workspace_id=?`, ws).Scan(&latest); err != nil || latest != 1 {
		t.Fatalf("data changed code version: %d %v", latest, err)
	}
	for _, body := range []string{
		`{"base_version":1,"documents":[{"id":"other","value":{}}]}`,
		`{"base_version":1,"documents":[{"id":"other","ifVersion":0,"value":{},"updated_by":"spoof"}]}`,
		`{"base_version":1,"documents":[{"id":"other","ifVersion":9007199254740993,"value":{}}]}`,
	} {
		if w := dashboardRequest(t, h, ws, owner, "POST", "sales", "", body); w.Code != 400 {
			t.Errorf("invalid input: %d %s", w.Code, w.Body)
		}
	}
	t.Setenv("CREWSHIP_ARTIFACT_DASHBOARD_DATA_ENABLED", "false")
	if w := dashboardRequest(t, h, ws, owner, "GET", "sales", "?version=1", ""); w.Code != 404 {
		t.Errorf("disabled: %d %s", w.Code, w.Body)
	}
}

func TestDashboardDataSharedManifestBoundsAndSafeProjection(t *testing.T) {
	h, ws, owner, call := artifactProjectFixture(t)
	t.Setenv("CREWSHIP_ARTIFACT_DASHBOARD_DATA_ENABLED", "true")
	if w := call(owner, "POST", "artifacts", `{"slug":"three-screens","name":"Dashboard",`+strings.TrimPrefix(dashboardBody(0, "sales"), "{")); w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	if w := dashboardRequest(t, h, ws, owner, "POST", "sales", "", `{"base_version":1,"documents":[{"id":"row","ifVersion":0,"value":{"amount":12}}]}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := call(owner, "POST", "three-screens/artifact/share", `{"base_version":1,"version":1}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := call(owner, "POST", "three-screens/artifact/versions", dashboardBody(1, "sales", "private")); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := dashboardRequest(t, h, ws, owner, "POST", "private", "", `{"base_version":2,"documents":[{"id":"secret","ifVersion":0,"value":{"amount":"PRIVATE_DATA"}}]}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	reader := "dashboard-reader"
	mustExec(t, h.db, `INSERT INTO users(id,email) VALUES(?,'dashboard-reader@example.test')`, reader)
	mustExec(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, generateCUID(), ws, reader)
	var page string
	if err := h.db.QueryRow(`SELECT id FROM pages WHERE workspace_id=? AND slug='three-screens'`, ws).Scan(&page); err != nil {
		t.Fatal(err)
	}
	mustExec(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES(?,'user',?,'read',?,datetime('now'))`, page, reader, owner)
	w := dashboardRequest(t, h, ws, reader, "GET", "sales", "?version=1", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"amount":12`) || strings.Contains(w.Body.String(), owner) || strings.Contains(w.Body.String(), "updated_by") {
		t.Fatalf("safe shared data: %d %s", w.Code, w.Body)
	}
	for _, query := range []string{"?version=1", "?version=2"} {
		w = dashboardRequest(t, h, ws, reader, "GET", "private", query, "")
		if w.Code != 404 && w.Code != 409 {
			t.Errorf("private dataset: %d %s", w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), "PRIVATE_DATA") {
			t.Fatal("private dataset leaked")
		}
	}
	if w := dashboardRequest(t, h, ws, reader, "GET", "sales", "?version=2", ""); w.Code != 409 {
		t.Errorf("stale shared pin: %d %s", w.Code, w.Body)
	}
	if w := dashboardRequest(t, h, ws, reader, "POST", "sales", "", fmt.Sprintf(`{"base_version":2,"documents":[{"id":"row","ifVersion":1,"value":{}}]}`)); w.Code != 403 {
		t.Errorf("reader write: %d %s", w.Code, w.Body)
	}
}

func TestDashboardDataDeclarationMatchesPreviewByteBound(t *testing.T) {
	h, ws, owner, call := artifactProjectFixture(t)
	t.Setenv("CREWSHIP_ARTIFACT_DASHBOARD_DATA_ENABLED", "true")
	var body map[string]any
	if err := json.Unmarshal([]byte(dashboardBody(0, "sales")), &body); err != nil {
		t.Fatal(err)
	}
	files := body["files"].([]any)
	declaration := files[len(files)-1].(map[string]any)
	declaration["content"] = declaration["content"].(string) + strings.Repeat(" ", 16<<10)
	body["name"] = "Oversized declaration"
	body["slug"] = "three-screens"
	raw, _ := json.Marshal(body)
	if w := call(owner, "POST", "artifacts", string(raw)); w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	if w := dashboardRequest(t, h, ws, owner, "GET", "sales", "?version=1", ""); w.Code != 404 {
		t.Fatalf("oversized declaration admitted: %d %s", w.Code, w.Body)
	}
}
