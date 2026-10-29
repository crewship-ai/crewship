package api

import (
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestAuditHeadIsExplicitOwnerReadOnly(t *testing.T) {
	f := newInstanceFixture(t)
	defer f.r.Shutdown()
	t.Setenv("CREWSHIP_OWNER_EMAIL", "boss@ex.com")
	var before int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM instance_audit_logs`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"instance", "workspace"} {
		t.Run(scope, func(t *testing.T) {
			table, column, where := "instance_audit_logs", "chain_seq", ""
			path := "/api/v1/admin/audit/head?scope=" + scope
			if scope == "workspace" {
				table, column, where = "journal_entries", "seq", " AND workspace_id='ws-old'"
				path += "&workspace_id=ws-old"
			}
			var expectedSeq int64
			var expectedHash string
			err := f.db.QueryRow(`SELECT `+column+`,entry_hash FROM `+table+` WHERE `+column+`>0`+where+` ORDER BY `+column+` DESC LIMIT 1`).Scan(&expectedSeq, &expectedHash)
			if err != nil && err != sql.ErrNoRows {
				t.Fatalf("actual native head calibration: %v", err)
			}
			response := f.do(f.boss, "GET", path, "")
			if response.Code != 200 {
				t.Errorf("current OWNER has no supported audit tail read: HTTP%d", response.Code)
			} else {
				var body map[string]json.RawMessage
				if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				var seq int64
				var hash string
				if len(body) != 2 || json.Unmarshal(body["tail_seq"], &seq) != nil || json.Unmarshal(body["tail_hash"], &hash) != nil || seq != expectedSeq || hash != expectedHash {
					t.Error("head response differs from exact native tail or leaks additional fields")
				}
			}
			// A workspace administrator must supply their actual workspace to
			// cross the existing admin-read middleware before the OWNER check.
			req := httptest.NewRequest("GET", path, nil)
			req.Header.Set("Authorization", "Bearer "+f.wsAdmin)
			req.Header.Set("X-Workspace-ID", "ws-old")
			denied := httptest.NewRecorder()
			f.r.ServeHTTP(denied, req)
			if denied.Code != 403 {
				t.Errorf("nonowner head status=%d want403", denied.Code)
			}
		})
	}
	// Instance ADMIN is also insufficient: OWNER is the fresh configured
	// instance owner, not the instance-admin role or a workspace floor.
	if _, err := f.db.Exec(`UPDATE users SET instance_role='ADMIN' WHERE id='wsadmin'`); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/api/v1/admin/audit/head?scope=instance",
		"/api/v1/admin/audit/head?scope=workspace&workspace_id=ws-old",
	} {
		if denied := f.do(f.wsAdmin, "GET", path, ""); denied.Code != 403 {
			t.Errorf("instance ADMIN alone read OWNER head: HTTP%d", denied.Code)
		}
	}
	var after int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM instance_audit_logs`).Scan(&after); err != nil || after != before {
		t.Error("head read wrote an audit row")
	}
}
