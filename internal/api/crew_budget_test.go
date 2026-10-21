package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type budgetResp struct {
	CrewID   string   `json:"crew_id"`
	Window   string   `json:"window"`
	LimitUSD *float64 `json:"limit_usd"`
	Mode     string   `json:"mode"`
	SpentUSD float64  `json:"spent_usd"`
	State    string   `json:"state"`
}

// The Budget card in Crew settings needs a way to set and read the crew's
// monthly limit. budget_limits was only ever read by admission and preflight.
func TestCrewBudget(t *testing.T) {
	db := setupTestDB(t)
	h := NewCrewHandler(db, newTestLogger())
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-b', ?, 'B', 'b')`, wsID)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-other', ?, 'O', 'o')`, wsID)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/crews/{crewId}/budget", h.BudgetGet)
	mux.HandleFunc("PUT /api/v1/crews/{crewId}/budget", h.BudgetPut)
	mux.HandleFunc("DELETE /api/v1/crews/{crewId}/budget", h.BudgetDelete)
	do := func(method, path, role, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, role))
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		return rr
	}
	spend := func(crew string, usd float64) {
		execOrFatal(t, db, `INSERT INTO cost_ledger (id, workspace_id, crew_id, provider, model, cost_usd, ts)
			VALUES (lower(hex(randomblob(8))), ?, ?, 'p', 'm', ?, strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, wsID, crew, usd)
	}

	steps := []struct {
		name       string
		method     string
		path       string
		role       string
		body       string
		wantStatus int
		check      func(t *testing.T, b budgetResp)
	}{
		{name: "no limit yet", method: "GET", path: "/api/v1/crews/crew-b/budget", role: "ADMIN", wantStatus: 200,
			check: func(t *testing.T, b budgetResp) {
				if b.LimitUSD != nil || b.State != "none" || b.Window != "month" {
					t.Errorf("got %+v", b)
				}
			}},
		{name: "member cannot read it", method: "GET", path: "/api/v1/crews/crew-b/budget", role: "MEMBER", wantStatus: 403},
		{name: "member cannot set it", method: "PUT", path: "/api/v1/crews/crew-b/budget", role: "MEMBER", body: `{"limit_usd":50}`, wantStatus: 403},
		{name: "zero is refused", method: "PUT", path: "/api/v1/crews/crew-b/budget", role: "ADMIN", body: `{"limit_usd":0}`, wantStatus: 400},
		{name: "negative is refused", method: "PUT", path: "/api/v1/crews/crew-b/budget", role: "ADMIN", body: `{"limit_usd":-5}`, wantStatus: 400},
		{name: "unknown mode is refused", method: "PUT", path: "/api/v1/crews/crew-b/budget", role: "ADMIN", body: `{"limit_usd":5,"mode":"loud"}`, wantStatus: 400},
		{name: "unknown crew", method: "PUT", path: "/api/v1/crews/nope/budget", role: "ADMIN", body: `{"limit_usd":5}`, wantStatus: 404},
		{name: "set 80 dollars", method: "PUT", path: "/api/v1/crews/crew-b/budget", role: "ADMIN", body: `{"limit_usd":80}`, wantStatus: 200,
			check: func(t *testing.T, b budgetResp) {
				if b.LimitUSD == nil || *b.LimitUSD != 80 || b.Mode != "tiered" || b.State != "ok" {
					t.Errorf("got %+v", b)
				}
			}},
		{name: "changing it replaces, not duplicates", method: "PUT", path: "/api/v1/crews/crew-b/budget", role: "OWNER", body: `{"limit_usd":100,"mode":"soft"}`, wantStatus: 200,
			check: func(t *testing.T, b budgetResp) {
				if b.LimitUSD == nil || *b.LimitUSD != 100 || b.Mode != "soft" {
					t.Errorf("got %+v", b)
				}
			}},
		{name: "reads back with this month's spend", method: "GET", path: "/api/v1/crews/crew-b/budget", role: "ADMIN", wantStatus: 200,
			check: func(t *testing.T, b budgetResp) {
				if b.SpentUSD != 0 || b.LimitUSD == nil || *b.LimitUSD != 100 {
					t.Errorf("got %+v", b)
				}
			}},
		{name: "clear", method: "DELETE", path: "/api/v1/crews/crew-b/budget", role: "ADMIN", wantStatus: 200,
			check: func(t *testing.T, b budgetResp) {
				if b.LimitUSD != nil || b.State != "none" {
					t.Errorf("got %+v", b)
				}
			}},
		{name: "clear is idempotent", method: "DELETE", path: "/api/v1/crews/crew-b/budget", role: "ADMIN", wantStatus: 200},
	}
	for _, s := range steps {
		t.Run(s.name, func(t *testing.T) {
			rr := do(s.method, s.path, s.role, s.body)
			if rr.Code != s.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rr.Code, s.wantStatus, rr.Body.String())
			}
			if s.check != nil {
				var b budgetResp
				if err := json.Unmarshal(rr.Body.Bytes(), &b); err != nil {
					t.Fatalf("decode: %v: %s", err, rr.Body.String())
				}
				s.check(t, b)
			}
		})
	}

	t.Run("spend this month moves the state", func(t *testing.T) {
		do("PUT", "/api/v1/crews/crew-b/budget", "ADMIN", `{"limit_usd":10}`)
		spend("crew-b", 8.5)
		spend("crew-other", 100) // another crew's spend never counts
		var b budgetResp
		_ = json.Unmarshal(do("GET", "/api/v1/crews/crew-b/budget", "ADMIN", "").Body.Bytes(), &b)
		if b.SpentUSD != 8.5 || b.State != "warn" {
			t.Errorf("at 85%% got spent=%v state=%q", b.SpentUSD, b.State)
		}
		spend("crew-b", 2)
		_ = json.Unmarshal(do("GET", "/api/v1/crews/crew-b/budget", "ADMIN", "").Body.Bytes(), &b)
		if b.State != "exceeded" {
			t.Errorf("at 105%% got state=%q", b.State)
		}
	})

	t.Run("a workspace limit is not the crew's", func(t *testing.T) {
		execOrFatal(t, db, `INSERT INTO budget_limits (id, workspace_id, scope_kind, scope_id, window, limit_usd) VALUES ('ws-b', ?, 'workspace', ?, 'month', 5)`, wsID, wsID)
		do("DELETE", "/api/v1/crews/crew-b/budget", "ADMIN", "")
		var b budgetResp
		_ = json.Unmarshal(do("GET", "/api/v1/crews/crew-b/budget", "ADMIN", "").Body.Bytes(), &b)
		if b.LimitUSD != nil {
			t.Errorf("crew shows the workspace limit: %+v", b)
		}
	})
}
