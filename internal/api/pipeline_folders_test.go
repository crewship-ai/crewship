package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ODCH-RT-01: routine folders. They organise the Routines panel; deleting
// one never deletes a routine, and every operation stays inside the workspace.

type folderRig struct {
	t *testing.T
	h *PipelineHandler
}

func newFolderRig(t *testing.T) *folderRig {
	db := openSmokeDB(t)
	seedSmokePipeline(t, db, "weekly")
	seedSmokePipeline(t, db, "digest")
	if _, err := db.Exec(`INSERT INTO workspaces(id,name,slug) VALUES('ws_other','Other','other')`); err != nil {
		t.Fatal(err)
	}
	return &folderRig{t: t, h: NewPipelineHandler(db, slog.Default(), nil, nil)}
}

func (f *folderRig) do(method, path, body string, fn http.HandlerFunc) *httptest.ResponseRecorder {
	f.t.Helper()
	req := withWorkspaceCtx(httptest.NewRequest(method, "/api/v1/workspaces/ws_smoke"+path, strings.NewReader(body)), "ws_smoke")
	if i := strings.LastIndex(path, "/"); i >= 0 && method != "POST" {
		req.SetPathValue("id", path[i+1:])
	}
	w := httptest.NewRecorder()
	fn(w, req)
	return w
}

func (f *folderRig) create(name string) routineFolderResponse {
	f.t.Helper()
	w := f.do("POST", "/pipeline-folders", `{"name":"`+name+`","color":"teal"}`, f.h.CreateFolder)
	if w.Code != http.StatusCreated {
		f.t.Fatalf("create %q: %d %s", name, w.Code, w.Body.String())
	}
	var out routineFolderResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		f.t.Fatal(err)
	}
	return out
}

func TestRoutineFolders_Create(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		pre      string
		wantCode int
	}{
		{"creates", `{"name":"Market intel","color":"teal"}`, "", http.StatusCreated},
		{"trims the name", `{"name":"  Ops  "}`, "", http.StatusCreated},
		{"blank name", `{"name":"   "}`, "", http.StatusBadRequest},
		{"missing name", `{}`, "", http.StatusBadRequest},
		{"too long", `{"name":"` + strings.Repeat("x", 81) + `"}`, "", http.StatusBadRequest},
		{"not json", `nope`, "", http.StatusBadRequest},
		{"duplicate ignoring case", `{"name":"market INTEL"}`, "Market intel", http.StatusConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := newFolderRig(t)
			if tc.pre != "" {
				rig.create(tc.pre)
			}
			w := rig.do("POST", "/pipeline-folders", tc.body, rig.h.CreateFolder)
			if w.Code != tc.wantCode {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.wantCode, w.Body.String())
			}
		})
	}
}

func TestRoutineFolders_ListUpdateMoveDelete(t *testing.T) {
	rig := newFolderRig(t)
	mi := rig.create("Market intel")
	ops := rig.create("Ops")

	// Move two routines in; a count shows up in the list.
	w := rig.do("POST", "/pipeline-folders/move", `{"slugs":["weekly","digest"],"folder_id":"`+mi.ID+`"}`, rig.h.MoveToFolder)
	if w.Code != http.StatusOK {
		t.Fatalf("move: %d %s", w.Code, w.Body.String())
	}
	var list []routineFolderResponse
	w = rig.do("GET", "/pipeline-folders", "", rig.h.ListFolders)
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list) != 2 {
		t.Fatalf("list: %v %s", err, w.Body.String())
	}
	if list[0].Name != "Market intel" || list[0].RoutineCount != 2 || list[1].Name != "Ops" || list[1].RoutineCount != 0 {
		t.Fatalf("list = %+v", list)
	}

	// The routine list carries folder_id.
	req := withWorkspaceCtx(httptest.NewRequest("GET", "/api/v1/workspaces/ws_smoke/pipelines", nil), "ws_smoke")
	rec := httptest.NewRecorder()
	rig.h.List(rec, req)
	var routines []pipelineResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &routines); err != nil {
		t.Fatal(err)
	}
	for _, p := range routines {
		if p.FolderID != mi.ID {
			t.Errorf("%s folder_id = %q, want %q", p.Slug, p.FolderID, mi.ID)
		}
	}

	// Rename and recolour; clearing the colour; a clashing name is refused.
	w = rig.do("PATCH", "/pipeline-folders/"+mi.ID, `{"name":"Intel","color":""}`, rig.h.UpdateFolder)
	var upd routineFolderResponse
	_ = json.Unmarshal(w.Body.Bytes(), &upd)
	if w.Code != http.StatusOK || upd.Name != "Intel" || upd.Color != "" || upd.RoutineCount != 2 {
		t.Fatalf("update: %d %+v", w.Code, upd)
	}
	if w = rig.do("PATCH", "/pipeline-folders/"+mi.ID, `{"name":"ops"}`, rig.h.UpdateFolder); w.Code != http.StatusConflict {
		t.Fatalf("clashing rename: %d", w.Code)
	}
	if w = rig.do("PATCH", "/pipeline-folders/nope", `{"name":"x"}`, rig.h.UpdateFolder); w.Code != http.StatusNotFound {
		t.Fatalf("unknown folder: %d", w.Code)
	}

	// Moving out (null) unfiles; deleting a folder unfiles what is in it and keeps the routines.
	if w = rig.do("POST", "/pipeline-folders/move", `{"slugs":["digest"],"folder_id":null}`, rig.h.MoveToFolder); w.Code != http.StatusOK {
		t.Fatalf("unfile: %d", w.Code)
	}
	if w = rig.do("DELETE", "/pipeline-folders/"+mi.ID, "", rig.h.DeleteFolder); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	var members int
	if err := rig.h.db.QueryRow(`SELECT COUNT(*) FROM routine_folder_members`).Scan(&members); err != nil || members != 0 {
		t.Fatalf("members after folder delete: %d %v", members, err)
	}
	var routinesLeft int
	if err := rig.h.db.QueryRow(`SELECT COUNT(*) FROM pipelines WHERE slug IN ('weekly','digest') AND deleted_at IS NULL`).Scan(&routinesLeft); err != nil || routinesLeft != 2 {
		t.Fatalf("routines after folder delete: %d %v", routinesLeft, err)
	}
	if w = rig.do("DELETE", "/pipeline-folders/"+mi.ID, "", rig.h.DeleteFolder); w.Code != http.StatusNotFound {
		t.Fatalf("delete twice: %d", w.Code)
	}
	_ = ops
}

func TestRoutineFolders_MoveIsAllOrNothingAndWorkspaceScoped(t *testing.T) {
	rig := newFolderRig(t)
	f := rig.create("Ops")
	cases := []struct {
		name     string
		body     string
		wantCode int
	}{
		{"unknown routine changes nothing", `{"slugs":["weekly","ghost"],"folder_id":"` + f.ID + `"}`, http.StatusNotFound},
		{"unknown folder", `{"slugs":["weekly"],"folder_id":"nope"}`, http.StatusNotFound},
		{"no slugs", `{"slugs":[],"folder_id":"` + f.ID + `"}`, http.StatusBadRequest},
		{"too many", `{"slugs":[` + strings.TrimSuffix(strings.Repeat(`"a",`, 201), ",") + `],"folder_id":"` + f.ID + `"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := rig.do("POST", "/pipeline-folders/move", tc.body, rig.h.MoveToFolder)
			if w.Code != tc.wantCode {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.wantCode, w.Body.String())
			}
			var n int
			_ = rig.h.db.QueryRow(`SELECT COUNT(*) FROM routine_folder_members`).Scan(&n)
			if n != 0 {
				t.Fatalf("%d routines were filed by a refused move", n)
			}
		})
	}

	// A folder of another workspace is not reachable.
	if _, err := rig.h.db.Exec(`INSERT INTO routine_folders(id,workspace_id,name,created_at,updated_at) VALUES('other_f','ws_other','Theirs','t','t')`); err != nil {
		t.Fatal(err)
	}
	if w := rig.do("POST", "/pipeline-folders/move", `{"slugs":["weekly"],"folder_id":"other_f"}`, rig.h.MoveToFolder); w.Code != http.StatusNotFound {
		t.Fatalf("foreign folder: %d", w.Code)
	}
	if w := rig.do("DELETE", "/pipeline-folders/other_f", "", rig.h.DeleteFolder); w.Code != http.StatusNotFound {
		t.Fatalf("delete foreign folder: %d", w.Code)
	}
}

// #3768: a folder's own menu — Icon, New subfolder, Move to. A folder wears an
// icon from the crew icon registry (the page folders' vocabulary) and may sit
// inside another folder, at most maxFolderDepth levels deep, never inside
// itself or its own subfolder.

func (f *folderRig) createWith(body string) (int, routineFolderResponse) {
	f.t.Helper()
	w := f.do("POST", "/pipeline-folders", body, f.h.CreateFolder)
	var out routineFolderResponse
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestRoutineFolders_CreateIconAndParent(t *testing.T) {
	cases := []struct {
		name       string
		body       func(top, deep string) string
		wantCode   int
		wantIcon   string
		wantParent func(top, deep string) string
	}{
		{"icon from the crew registry", func(_, _ string) string { return `{"name":"A","icon":"rocket"}` }, http.StatusCreated, "rocket", nil},
		{"no icon", func(_, _ string) string { return `{"name":"A"}` }, http.StatusCreated, "", nil},
		{"unknown icon refused", func(_, _ string) string { return `{"name":"A","icon":"not-an-icon"}` }, http.StatusBadRequest, "", nil},
		{"subfolder", func(top, _ string) string { return `{"name":"A","parent_id":"` + top + `"}` }, http.StatusCreated, "", func(top, _ string) string { return top }},
		{"empty parent is top level", func(_, _ string) string { return `{"name":"A","parent_id":""}` }, http.StatusCreated, "", func(_, _ string) string { return "" }},
		{"unknown parent", func(_, _ string) string { return `{"name":"A","parent_id":"nope"}` }, http.StatusNotFound, "", nil},
		{"parent of another workspace", func(_, _ string) string { return `{"name":"A","parent_id":"other_f"}` }, http.StatusNotFound, "", nil},
		{"too deep", func(_, deep string) string { return `{"name":"A","parent_id":"` + deep + `"}` }, http.StatusBadRequest, "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := newFolderRig(t)
			top := rig.create("Top")
			_, mid := rig.createWith(`{"name":"Mid","parent_id":"` + top.ID + `"}`)
			_, deep := rig.createWith(`{"name":"Deep","parent_id":"` + mid.ID + `"}`)
			if deep.ID == "" {
				t.Fatal("the three-level chain could not be built")
			}
			if _, err := rig.h.db.Exec(`INSERT INTO routine_folders(id,workspace_id,name,created_at,updated_at) VALUES('other_f','ws_other','Theirs','t','t')`); err != nil {
				t.Fatal(err)
			}
			code, got := rig.createWith(tc.body(top.ID, deep.ID))
			if code != tc.wantCode {
				t.Fatalf("status %d, want %d", code, tc.wantCode)
			}
			if code != http.StatusCreated {
				return
			}
			if got.Icon != tc.wantIcon {
				t.Errorf("icon = %q, want %q", got.Icon, tc.wantIcon)
			}
			want := ""
			if tc.wantParent != nil {
				want = tc.wantParent(top.ID, deep.ID)
			}
			if got.ParentID != want {
				t.Errorf("parent_id = %q, want %q", got.ParentID, want)
			}
		})
	}
}

func TestRoutineFolders_UpdateIconAndParent(t *testing.T) {
	// Tree: A › B › C, and D on its own.
	type tree struct{ a, b, c, d routineFolderResponse }
	cases := []struct {
		name       string
		target     func(tr tree) string
		body       func(tr tree) string
		wantCode   int
		wantIcon   string
		wantParent func(tr tree) string
	}{
		{"set icon", func(tr tree) string { return tr.d.ID }, func(tree) string { return `{"icon":"briefcase"}` }, http.StatusOK, "briefcase", func(tree) string { return "" }},
		{"clear icon", func(tr tree) string { return tr.a.ID }, func(tree) string { return `{"icon":""}` }, http.StatusOK, "", func(tree) string { return "" }},
		{"unknown icon", func(tr tree) string { return tr.d.ID }, func(tree) string { return `{"icon":"nope"}` }, http.StatusBadRequest, "", nil},
		{"omitted parent is kept", func(tr tree) string { return tr.b.ID }, func(tree) string { return `{"name":"B2"}` }, http.StatusOK, "", func(tr tree) string { return tr.a.ID }},
		{"move into another folder", func(tr tree) string { return tr.d.ID }, func(tr tree) string { return `{"parent_id":"` + tr.a.ID + `"}` }, http.StatusOK, "", func(tr tree) string { return tr.a.ID }},
		{"null moves to the top", func(tr tree) string { return tr.b.ID }, func(tree) string { return `{"parent_id":null}` }, http.StatusOK, "", func(tree) string { return "" }},
		{"empty moves to the top", func(tr tree) string { return tr.c.ID }, func(tree) string { return `{"parent_id":""}` }, http.StatusOK, "", func(tree) string { return "" }},
		{"into itself", func(tr tree) string { return tr.a.ID }, func(tr tree) string { return `{"parent_id":"` + tr.a.ID + `"}` }, http.StatusBadRequest, "", nil},
		{"into its own subfolder", func(tr tree) string { return tr.a.ID }, func(tr tree) string { return `{"parent_id":"` + tr.c.ID + `"}` }, http.StatusBadRequest, "", nil},
		{"subtree would be too deep", func(tr tree) string { return tr.a.ID }, func(tr tree) string { return `{"parent_id":"` + tr.d.ID + `"}` }, http.StatusBadRequest, "", nil},
		{"unknown parent", func(tr tree) string { return tr.d.ID }, func(tree) string { return `{"parent_id":"nope"}` }, http.StatusNotFound, "", nil},
		{"parent not a string", func(tr tree) string { return tr.d.ID }, func(tree) string { return `{"parent_id":5}` }, http.StatusBadRequest, "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := newFolderRig(t)
			var tr tree
			_, tr.a = rig.createWith(`{"name":"A","icon":"rocket"}`)
			_, tr.b = rig.createWith(`{"name":"B","parent_id":"` + tr.a.ID + `"}`)
			_, tr.c = rig.createWith(`{"name":"C","parent_id":"` + tr.b.ID + `"}`)
			tr.d = rig.create("D")
			if tr.c.ID == "" {
				t.Fatal("tree could not be built")
			}
			id := tc.target(tr)
			w := rig.do("PATCH", "/pipeline-folders/"+id, tc.body(tr), rig.h.UpdateFolder)
			if w.Code != tc.wantCode {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.wantCode, w.Body.String())
			}
			if w.Code != http.StatusOK {
				return
			}
			var got routineFolderResponse
			_ = json.Unmarshal(w.Body.Bytes(), &got)
			if got.Icon != tc.wantIcon {
				t.Errorf("icon = %q, want %q", got.Icon, tc.wantIcon)
			}
			if want := tc.wantParent(tr); got.ParentID != want {
				t.Errorf("parent_id = %q, want %q", got.ParentID, want)
			}
		})
	}
}

// Deleting a folder hands its subfolders to its own parent; its routines are
// unfiled, the subfolders' routines stay where they are.
func TestRoutineFolders_DeleteLiftsSubfolders(t *testing.T) {
	rig := newFolderRig(t)
	top := rig.create("Top")
	_, mid := rig.createWith(`{"name":"Mid","parent_id":"` + top.ID + `"}`)
	_, leaf := rig.createWith(`{"name":"Leaf","parent_id":"` + mid.ID + `"}`)
	rig.do("POST", "/pipeline-folders/move", `{"slugs":["weekly"],"folder_id":"`+mid.ID+`"}`, rig.h.MoveToFolder)
	rig.do("POST", "/pipeline-folders/move", `{"slugs":["digest"],"folder_id":"`+leaf.ID+`"}`, rig.h.MoveToFolder)

	if w := rig.do("DELETE", "/pipeline-folders/"+mid.ID, "", rig.h.DeleteFolder); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	var list []routineFolderResponse
	_ = json.Unmarshal(rig.do("GET", "/pipeline-folders", "", rig.h.ListFolders).Body.Bytes(), &list)
	byName := map[string]routineFolderResponse{}
	for _, f := range list {
		byName[f.Name] = f
	}
	if len(list) != 2 || byName["Leaf"].ParentID != top.ID || byName["Leaf"].RoutineCount != 1 {
		t.Fatalf("after delete = %+v", list)
	}
	var filed int
	_ = rig.h.db.QueryRow(`SELECT COUNT(*) FROM routine_folder_members WHERE pipeline_id = (SELECT id FROM pipelines WHERE slug = 'weekly')`).Scan(&filed)
	if filed != 0 {
		t.Fatal("the deleted folder's routine must be unfiled")
	}
}
