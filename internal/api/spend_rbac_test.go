package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// #3590: the cost read endpoints are OWNER/ADMIN only on the server. The
// UI hiding Spend from other roles is not enforcement.
func TestSpendReadEndpoints_RoleMatrix(t *testing.T) {
	jh, userID, wsID, _ := newJournalHandlerTest(t)
	ph := NewPaymasterHandler(jh.db, newTestLogger())
	seedCrewRow(t, jh.db, "crew-rbac", wsID, "R", "r")
	seedMissionRow(t, jh.db, "mission-rbac", wsID, "crew-rbac", "M")

	routes := []struct {
		name string
		path string
		pv   map[string]string
		call func(http.ResponseWriter, *http.Request)
	}{
		{"by-crew", "/api/v1/paymaster/spend/by-crew", nil, ph.SpendByCrew},
		{"by-agent", "/api/v1/paymaster/spend/by-agent/crew-rbac", map[string]string{"crewId": "crew-rbac"}, ph.SpendByAgent},
		{"by-mission", "/api/v1/paymaster/spend/by-mission/mission-rbac", map[string]string{"missionId": "mission-rbac"}, ph.SpendByMission},
		{"top-spenders", "/api/v1/paymaster/top-spenders", nil, ph.TopSpenders},
		{"journal-spend", "/api/v1/journal/spend", nil, jh.Spend},
	}
	roles := []struct {
		role string
		want int
	}{
		{"OWNER", http.StatusOK},
		{"ADMIN", http.StatusOK},
		{"MANAGER", http.StatusForbidden},
		{"MEMBER", http.StatusForbidden},
		{"VIEWER", http.StatusForbidden},
		{"GUEST", http.StatusForbidden},
		{"", http.StatusForbidden},
	}
	for _, rt := range routes {
		for _, rl := range roles {
			t.Run(rt.name+"/"+rl.role, func(t *testing.T) {
				req := httptest.NewRequest("GET", rt.path, nil)
				for k, v := range rt.pv {
					req.SetPathValue(k, v)
				}
				req = withWorkspaceUser(req, userID, wsID, rl.role)
				rr := httptest.NewRecorder()
				rt.call(rr, req)
				if rr.Code != rl.want {
					t.Fatalf("role %q: status = %d, want %d; body=%s", rl.role, rr.Code, rl.want, rr.Body.String())
				}
			})
		}
	}
}

// #3590: an empty rollup is `[]`, never `null` (the UI read null as "unknown").
func TestSpendReadEndpoints_EmptyRowsAreArrays(t *testing.T) {
	jh, userID, wsID, _ := newJournalHandlerTest(t)
	ph := NewPaymasterHandler(jh.db, newTestLogger())
	seedCrewRow(t, jh.db, "crew-empty", wsID, "E", "e")

	cases := []struct {
		name string
		path string
		pv   map[string]string
		call func(http.ResponseWriter, *http.Request)
	}{
		{"by-crew", "/api/v1/paymaster/spend/by-crew", nil, ph.SpendByCrew},
		{"by-agent", "/api/v1/paymaster/spend/by-agent/crew-empty", map[string]string{"crewId": "crew-empty"}, ph.SpendByAgent},
		{"top-spenders", "/api/v1/paymaster/top-spenders", nil, ph.TopSpenders},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", c.path, nil)
			for k, v := range c.pv {
				req.SetPathValue(k, v)
			}
			req = withWorkspaceUser(req, userID, wsID, "OWNER")
			rr := httptest.NewRecorder()
			c.call(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if got := string(body["rows"]); got != "[]" {
				t.Fatalf("rows = %s, want []", got)
			}
		})
	}
}
