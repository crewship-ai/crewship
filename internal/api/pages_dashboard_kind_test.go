package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDashboardKindUsesOwnerLatestAndReaderSharedManifest(t *testing.T) {
	h, ws, owner, call := artifactProjectFixture(t)
	t.Setenv("CREWSHIP_ARTIFACT_DASHBOARD_DATA_ENABLED", "true")
	create := `{"slug":"three-screens","name":"Dashboard",` + strings.TrimPrefix(artifactProjectBody(0, "Project"), "{")
	if w := call(owner, "POST", "artifacts", create); w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	if w := call(owner, "POST", "three-screens/artifact/share", `{"base_version":1,"version":1}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	reader := "dashboard-kind-reader"
	mustExec(t, h.db, `INSERT INTO users(id,email) VALUES(?,'dashboard-kind-reader@example.test')`, reader)
	mustExec(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES(?,?,?,'MEMBER')`, generateCUID(), ws, reader)
	var page string
	if err := h.db.QueryRow(`SELECT id FROM pages WHERE workspace_id=? AND slug='three-screens'`, ws).Scan(&page); err != nil {
		t.Fatal(err)
	}
	mustExec(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id,granted_at) VALUES(?,'user',?,'read',?,datetime('now'))`, page, reader, owner)
	assertKind := func(user, want string) {
		t.Helper()
		row := pagesListRows(t, h, ws, user, "MEMBER")["three-screens"]
		if row["artifact_kind"] != want {
			t.Errorf("%s list kind=%v want%s", user, row["artifact_kind"], want)
		}
		r := pagesRequest(t, "GET", "/api/v1/pages/three-screens", ws, user, "MEMBER", "")
		r.SetPathValue("slug", "three-screens")
		w := httptest.NewRecorder()
		h.Get(w, r)
		var detail map[string]any
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &detail) != nil || detail["artifact_kind"] != want {
			t.Errorf("%s detail=%d %s want%s", user, w.Code, w.Body, want)
		}
	}
	assertKind(owner, "project")
	assertKind(reader, "project")
	var manifest map[string]any
	_ = json.Unmarshal([]byte(artifactProjectBody(1, "Dashboard")), &manifest)
	manifest["files"] = append(manifest["files"].([]any), map[string]any{"path": "dashboard.json", "media_type": "application/json", "content": `{"schema_version":1,"collections":["sales"]}`})
	raw, _ := json.Marshal(manifest)
	if w := call(owner, "POST", "three-screens/artifact/versions", string(raw)); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	assertKind(owner, "dashboard")
	assertKind(reader, "project")
	if w := call(owner, "POST", "three-screens/artifact/share", `{"base_version":2,"version":2}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	assertKind(reader, "dashboard")
	if w := call(owner, "POST", "three-screens/artifact/versions", artifactProjectBody(2, "Project again")); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	assertKind(owner, "project")
	assertKind(reader, "dashboard")
	t.Setenv("CREWSHIP_ARTIFACT_DASHBOARD_DATA_ENABLED", "false")
	assertKind(reader, "project")
}
