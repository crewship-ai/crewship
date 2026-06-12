package api

// Coverage tests for task_handler.go — CreateTask dependency branches and
// DB error paths, UpdateTask metadata field updates. Reuses the mission
// seed helpers from missions_test.go / mission_handler_mutate_cov_test.go
// and the covMHRig from mission_handler_cov_test.go.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func covTHCreateReq(r *covMHRig, missionID, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body))
	req.SetPathValue("crewId", r.crewID)
	req.SetPathValue("missionId", missionID)
	return withWorkspaceUser(req, r.userID, r.wsID, "MANAGER")
}

func covTHUpdateReq(r *covMHRig, missionID, taskID, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPatch, "/x", strings.NewReader(body))
	req.SetPathValue("crewId", r.crewID)
	req.SetPathValue("missionId", missionID)
	req.SetPathValue("taskId", taskID)
	return withWorkspaceUser(req, r.userID, r.wsID, "MANAGER")
}

func TestCovTHCreateTask_DependencyStatuses(t *testing.T) {
	r := newCovMHRig(t)
	covMMSeedMission(t, r.db, "m-th", r.wsID, r.crewID, r.leadID, "PLANNING")
	r.seedTask(t, "dep-done", "m-th", "COMPLETED", 1)
	r.seedTask(t, "dep-open", "m-th", "PENDING", 2)

	t.Run("all deps completed -> PENDING", func(t *testing.T) {
		rec := httptest.NewRecorder()
		r.h.CreateTask(rec, covTHCreateReq(r, "m-th", `{"title":"T1","depends_on":["dep-done"]}`))
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
		}
		var resp missionTaskResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.Status != "PENDING" {
			t.Errorf("status = %q, want PENDING", resp.Status)
		}
	})

	t.Run("incomplete dep -> BLOCKED", func(t *testing.T) {
		rec := httptest.NewRecorder()
		r.h.CreateTask(rec, covTHCreateReq(r, "m-th", `{"title":"T2","depends_on":["dep-open"]}`))
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
		}
		var resp missionTaskResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.Status != "BLOCKED" {
			t.Errorf("status = %q, want BLOCKED", resp.Status)
		}
	})

	t.Run("unknown dep -> 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		r.h.CreateTask(rec, covTHCreateReq(r, "m-th", `{"title":"T3","depends_on":["ghost"]}`))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
		}
	})
}

func TestCovTHCreateTask_TableErrors500(t *testing.T) {
	t.Run("insert error", func(t *testing.T) {
		r := newCovMHRig(t)
		covMMSeedMission(t, r.db, "m-e1", r.wsID, r.crewID, r.leadID, "PLANNING")
		if _, err := r.db.Exec(`DROP TABLE mission_tasks`); err != nil {
			t.Fatalf("drop: %v", err)
		}
		rec := httptest.NewRecorder()
		r.h.CreateTask(rec, covTHCreateReq(r, "m-e1", `{"title":"X"}`))
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", rec.Code)
		}
	})

	t.Run("dependency lookup error", func(t *testing.T) {
		r := newCovMHRig(t)
		covMMSeedMission(t, r.db, "m-e2", r.wsID, r.crewID, r.leadID, "PLANNING")
		if _, err := r.db.Exec(`DROP TABLE mission_tasks`); err != nil {
			t.Fatalf("drop: %v", err)
		}
		rec := httptest.NewRecorder()
		r.h.CreateTask(rec, covTHCreateReq(r, "m-e2", `{"title":"X","depends_on":["d1"]}`))
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", rec.Code)
		}
	})
}

func TestCovTHUpdateTask_MetadataFields(t *testing.T) {
	r := newCovMHRig(t)
	covMMSeedMission(t, r.db, "m-md", r.wsID, r.crewID, r.leadID, "IN_PROGRESS")
	r.seedTask(t, "t-md", "m-md", "PENDING", 1)

	body := `{"result_summary":"done well","error_message":"none","output_path":"/out","token_count":42,"estimated_cost":0.5,"max_iterations":7}`
	rec := httptest.NewRecorder()
	r.h.UpdateTask(rec, covTHUpdateReq(r, "m-md", "t-md", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}

	var summary, outputPath string
	var tokens, maxIter int
	var cost float64
	if err := r.db.QueryRow(`SELECT result_summary, output_path, token_count, estimated_cost, max_iterations
		FROM mission_tasks WHERE id = 't-md'`).Scan(&summary, &outputPath, &tokens, &cost, &maxIter); err != nil {
		t.Fatalf("query: %v", err)
	}
	if summary != "done well" || outputPath != "/out" || tokens != 42 || cost != 0.5 || maxIter != 7 {
		t.Errorf("row = (%s, %s, %d, %f, %d)", summary, outputPath, tokens, cost, maxIter)
	}
}

func TestCovTHUpdateTask_Guards(t *testing.T) {
	r := newCovMHRig(t)
	covMMSeedMission(t, r.db, "m-g", r.wsID, r.crewID, r.leadID, "IN_PROGRESS")
	r.seedTask(t, "t-g", "m-g", "IN_PROGRESS", 1)

	t.Run("edit fields on started task 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		r.h.UpdateTask(rec, covTHUpdateReq(r, "m-g", "t-g", `{"title":"new"}`))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("max_iterations out of range 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		r.h.UpdateTask(rec, covTHUpdateReq(r, "m-g", "t-g", `{"max_iterations":999}`))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("status+depends_on conflict 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		r.h.UpdateTask(rec, covTHUpdateReq(r, "m-g", "t-g", `{"status":"COMPLETED","depends_on":"[]"}`))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
	})
}
