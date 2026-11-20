package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// #5411: restoring a stopped schedule's artifact access is an administrator
// adoption. Changing what a schedule runs goes through PATCH, which is the
// manage tier (OWNER/ADMIN) at the router and in the handler; a MANAGER can
// create a schedule but cannot change one, their own included. So a MANAGER
// creator whose access was revoked cannot restore it by re-saving, even after
// a fresh login, and neither can an unrelated MANAGER; a workspace OWNER or
// ADMIN in good standing restores it by changing what it runs, and becomes
// its creator. This drives the real router with real CLI credentials.
func TestScheduleRestoreIsAnAdministratorAdoption(t *testing.T) {
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, ownerID)
	ownerTok := mintTokenFor(t, db, ownerID, "owner5411000000000000000000")
	mgrTok := seedRoleMemberToken(t, db, wsID, "mgr-5411", "MANAGER", "mgr54110000000000000000000")
	otherTok := seedRoleMemberToken(t, db, wsID, "mgr2-5411", "MANAGER", "mgr254110000000000000000000")
	seedPipelineRow(t, db, wsID, "pln_5411", "restore-routine")
	if _, err := db.Exec(`INSERT INTO feature_flag_overrides(id,flag_id,workspace_id,enabled) VALUES('flag-5411','ff_artifact_agent_access_modes',?,1)`, wsID); err != nil {
		t.Fatal(err)
	}
	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger())
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	r.PipelinesHandler.SetScheduleStore(pipeline.NewScheduleStore(db))
	base := "/api/v1/workspaces/" + wsID + "/pipeline-schedules"
	call := func(method, path, token, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		return rr
	}
	state := func(token, id string) scheduleResponse {
		t.Helper()
		rr := call("GET", base, token, "")
		if rr.Code != http.StatusOK {
			t.Fatalf("list: %d %s", rr.Code, rr.Body)
		}
		var rows []scheduleResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
			t.Fatalf("list body: %v %s", err, rr.Body)
		}
		for _, row := range rows {
			if row.ID == id {
				return row
			}
		}
		t.Fatalf("schedule %s not listed: %s", id, rr.Body)
		return scheduleResponse{}
	}

	rr := call("POST", base, mgrTok, `{"cron_expr":"0 * * * *","target_pipeline_slug":"restore-routine","name":"mine","enabled":false}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("MANAGER create: %d %s", rr.Code, rr.Body)
	}
	var mine scheduleResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &mine); err != nil {
		t.Fatal(err)
	}
	if got := state(mgrTok, mine.ID); got.ArtifactAccess != "active" || got.CreatorUserID != "mgr-5411" {
		t.Fatalf("a good-standing MANAGER's schedule: %+v", got)
	}

	// An administrator revokes the MANAGER's access; they sign in again.
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := MarkAccessRevokedTx(t.Context(), tx, "mgr-5411"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	freshTok := mintSecondToken(t, db, "mgr-5411", "mgrfresh5411000000000000000")
	if got := state(freshTok, mine.ID); got.ArtifactAccess+":"+got.StoppedReason != "stopped:creator_access_revoked" {
		t.Fatalf("after revocation the creator sees %+v", got)
	}

	for _, tc := range []struct{ name, token, body string }{
		{"the MANAGER creator re-saving the same cron after a fresh login", freshTok, `{"cron_expr":"0 * * * *"}`},
		{"the MANAGER creator changing the cron", freshTok, `{"cron_expr":"0 5 * * *"}`},
		{"the MANAGER creator re-saving the inputs", freshTok, `{"inputs":{}}`},
		{"an unrelated MANAGER", otherTok, `{"cron_expr":"0 * * * *"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rr := call("PATCH", base+"/"+mine.ID, tc.token, tc.body); rr.Code != http.StatusForbidden {
				t.Fatalf("PATCH = %d, want 403: %s", rr.Code, rr.Body)
			}
		})
	}
	if got := state(ownerTok, mine.ID); got.ArtifactAccess+":"+got.StoppedReason != "stopped:creator_access_revoked" || got.CreatorUserID != "mgr-5411" || got.CronExpr != "0 * * * *" {
		t.Fatalf("a refused PATCH changed the schedule: %+v", got)
	}

	// The OWNER adopts it by changing what it runs.
	rr = call("PATCH", base+"/"+mine.ID, ownerTok, `{"cron_expr":"0 * * * *"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("OWNER re-save: %d %s", rr.Code, rr.Body)
	}
	if got := state(ownerTok, mine.ID); got.ArtifactAccess != "active" || got.CreatorUserID != ownerID {
		t.Fatalf("the OWNER's re-save did not adopt and restore it: %+v", got)
	}
}

// mintSecondToken is a fresh CLI login for a user who already holds a token.
func mintSecondToken(t *testing.T, db *sql.DB, userID, plaintextSuffix string) string {
	t.Helper()
	plaintext := "crewship_cli_" + plaintextSuffix
	if _, err := db.Exec(`INSERT INTO cli_tokens (id, user_id, name, token_hash, created_at) VALUES (?, ?, 'fresh', ?, datetime('now'))`,
		"clt-fresh-"+userID, userID, sha256Hex(plaintext)); err != nil {
		t.Fatalf("mint fresh token for %s: %v", userID, err)
	}
	return plaintext
}
