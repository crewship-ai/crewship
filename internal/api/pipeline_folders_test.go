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
