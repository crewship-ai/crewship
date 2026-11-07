package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMilestoneCreateStatusContract(t *testing.T) {
	h, db, user, ws, project := covMSRig(t)
	for _, tc := range []struct {
		name, body, wantStatus string
		code                   int
	}{
		{"omitted", `{"name":"omitted"}`, "active", 201},
		{"empty", `{"name":"empty","status":""}`, "active", 201},
		{"null", `{"name":"null","status":null}`, "active", 201},
		{"active", `{"name":"active","status":"active"}`, "active", 201},
		{"completed", `{"name":"completed","status":"completed"}`, "completed", 201},
		{"cancelled", `{"name":"cancelled","status":"cancelled"}`, "cancelled", 201},
		{"unsupported", `{"name":"unsupported","status":"planned"}`, "", 400},
		{"case", `{"name":"case","status":"ACTIVE"}`, "", 400},
		{"whitespace", `{"name":"whitespace","status":" active "}`, "", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.Create(rec, covMSReq(user, ws, "ADMIN", "POST", tc.body, project, ""))
			if rec.Code != tc.code {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM milestones WHERE project_id=? AND name=?`, project, tc.name).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if tc.code == http.StatusBadRequest {
				if count != 0 {
					t.Fatal("invalid request persisted")
				}
				return
			}
			var status string
			if err := db.QueryRow(`SELECT status FROM milestones WHERE project_id=? AND name=?`, project, tc.name).Scan(&status); err != nil || status != tc.wantStatus {
				t.Fatalf("storedstatus=%q err=%v", status, err)
			}
		})
	}
}

func TestMilestoneUpdateStatusContract(t *testing.T) {
	h, db, user, ws, project := covMSRig(t)
	rec := httptest.NewRecorder()
	h.Create(rec, covMSReq(user, ws, "ADMIN", "POST", `{"name":"Original"}`, project, ""))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create=%d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"planned", "", "ACTIVE", " active "} {
		t.Run("invalid_"+status, func(t *testing.T) {
			body, _ := json.Marshal(map[string]string{"status": status, "name": "Changed"})
			rec := httptest.NewRecorder()
			h.Update(rec, covMSReq(user, ws, "ADMIN", "PATCH", string(body), project, created.ID))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			var name, stored string
			if err := db.QueryRow(`SELECT name,status FROM milestones WHERE id=?`, created.ID).Scan(&name, &stored); err != nil || name != "Original" || stored != "active" {
				t.Fatalf("row mutated name=%q status=%q err=%v", name, stored, err)
			}
		})
	}
	t.Run("null_only_is_no_fields", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.Update(rec, covMSReq(user, ws, "ADMIN", "PATCH", `{"status":null}`, project, created.ID))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("null-only update=%d", rec.Code)
		}
		var name, status string
		if err := db.QueryRow(`SELECT name,status FROM milestones WHERE id=?`, created.ID).Scan(&name, &status); err != nil || name != "Original" || status != "active" {
			t.Fatalf("null-only changed row: name=%q status=%q err=%v", name, status, err)
		}
	})
	for _, body := range []string{`{"description":"changed"}`, `{"description":"changed again","status":null}`} {
		rec := httptest.NewRecorder()
		h.Update(rec, covMSReq(user, ws, "ADMIN", "PATCH", body, project, created.ID))
		if rec.Code != http.StatusOK {
			t.Fatalf("omitted/null update status=%d body=%s", rec.Code, rec.Body.String())
		}
		var name, stored string
		if err := db.QueryRow(`SELECT name,status FROM milestones WHERE id=?`, created.ID).Scan(&name, &stored); err != nil || name != "Original" || stored != "active" {
			t.Fatalf("omitted/null changed status: name=%q status=%q err=%v", name, stored, err)
		}
	}
	for _, status := range []string{"completed", "cancelled", "active"} {
		t.Run("valid_"+status, func(t *testing.T) {
			body, _ := json.Marshal(map[string]string{"status": status})
			rec := httptest.NewRecorder()
			h.Update(rec, covMSReq(user, ws, "ADMIN", "PATCH", string(body), project, created.ID))
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			var stored string
			if err := db.QueryRow(`SELECT status FROM milestones WHERE id=?`, created.ID).Scan(&stored); err != nil || stored != status {
				t.Fatalf("status=%q err=%v", stored, err)
			}
		})
	}
}
