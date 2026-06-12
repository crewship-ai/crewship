package api

// Coverage tests for agents_create.go (AgentHandler.Create) — validation
// 400s, conflict 409s, crew-role elevation, license no-op gate, and DB
// error paths not exercised by agents_test.go.

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/license"
)

type covACRig struct {
	h      *AgentHandler
	db     *sql.DB
	userID string
	wsID   string
	crewID string
}

func newCovACRig(t *testing.T) *covACRig {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := "crew-ac"
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'AC', 'ac')`, crewID, wsID); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	return &covACRig{h: NewAgentHandler(db, newTestLogger()), db: db, userID: userID, wsID: wsID, crewID: crewID}
}

// covACReq builds an authenticated create request. role is the workspace role.
func (r *covACRig) req(t *testing.T, role, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents", strings.NewReader(body))
	req = withWorkspaceUser(req, r.userID, r.wsID, role)
	return req
}

func TestCovACCreate_InvalidJSON400(t *testing.T) {
	r := newCovACRig(t)
	rec := httptest.NewRecorder()
	r.h.Create(rec, r.req(t, "ADMIN", `{nope`))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestCovACCreate_Validation400s(t *testing.T) {
	r := newCovACRig(t)
	cases := []struct {
		name string
		body string
		want string // substring of error
	}{
		{"name too short", `{"name":"x","slug":"ok-slug"}`, "name must be"},
		{"name too long", `{"name":"` + strings.Repeat("n", 101) + `","slug":"ok-slug"}`, "name must be"},
		{"slug missing", `{"name":"Agent X","slug":""}`, "slug must be"},
		{"slug bad format", `{"name":"Agent X","slug":"Bad_Slug!"}`, "lowercase"},
		{"lead requires crew", `{"name":"Agent X","slug":"lead-x","agent_role":"LEAD"}`, "requires crew_id"},
		{"bad lead_mode", `{"name":"Agent X","slug":"lead-x","agent_role":"LEAD","crew_id":"crew-ac","lead_mode":"turbo"}`, "lead_mode"},
		{"bad cli_adapter", `{"name":"Agent X","slug":"a-x","cli_adapter":"VIM"}`, "cli_adapter"},
		{"bad llm_provider", `{"name":"Agent X","slug":"a-x","llm_provider":"SKYNET"}`, "llm_provider"},
		{"bad tool_profile", `{"name":"Agent X","slug":"a-x","tool_profile":"EVERYTHING"}`, "tool_profile"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			r.h.Create(rec, r.req(t, "ADMIN", tc.body))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Errorf("body %q missing %q", rec.Body.String(), tc.want)
			}
		})
	}
}

func TestCovACCreate_HappyPathWithMemoryAndLicense(t *testing.T) {
	r := newCovACRig(t)
	// Wire a (no-op v0.1) license so the CheckAgentLimit gate executes.
	r.h.SetLicense(&license.License{})

	body := `{"name":"Echo","slug":"echo","crew_id":"crew-ac","memory_enabled":true,"agent_role":"LEAD"}`
	rec := httptest.NewRecorder()
	r.h.Create(rec, r.req(t, "ADMIN", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var resp agentResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.MemoryEnabled {
		t.Error("memory_enabled should round-trip true")
	}
	if resp.LeadMode == nil || *resp.LeadMode != "active" {
		t.Errorf("lead_mode = %v, want default active", resp.LeadMode)
	}
	// DB row persisted with memory_enabled = 1.
	var mem int
	if err := r.db.QueryRow(`SELECT memory_enabled FROM agents WHERE id = ?`, resp.ID).Scan(&mem); err != nil {
		t.Fatalf("query: %v", err)
	}
	if mem != 1 {
		t.Errorf("memory_enabled = %d, want 1", mem)
	}
}

func TestCovACCreate_SecondLeadConflict409(t *testing.T) {
	r := newCovACRig(t)
	rec := httptest.NewRecorder()
	r.h.Create(rec, r.req(t, "ADMIN", `{"name":"Lead One","slug":"lead-one","crew_id":"crew-ac","agent_role":"LEAD"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("first lead: status = %d; body=%s", rec.Code, rec.Body.String())
	}
	rec2 := httptest.NewRecorder()
	r.h.Create(rec2, r.req(t, "ADMIN", `{"name":"Lead Two","slug":"lead-two","crew_id":"crew-ac","agent_role":"LEAD"}`))
	if rec2.Code != http.StatusConflict {
		t.Errorf("second lead: status = %d, want 409; body=%s", rec2.Code, rec2.Body.String())
	}
}

func TestCovACCreate_SlugTaken409(t *testing.T) {
	r := newCovACRig(t)
	rec := httptest.NewRecorder()
	r.h.Create(rec, r.req(t, "ADMIN", `{"name":"First","slug":"dupe"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("first: status = %d", rec.Code)
	}
	rec2 := httptest.NewRecorder()
	r.h.Create(rec2, r.req(t, "ADMIN", `{"name":"Second","slug":"dupe"}`))
	if rec2.Code != http.StatusConflict {
		t.Errorf("dupe slug: status = %d, want 409", rec2.Code)
	}
}

// TestCovACCreate_CrewRoleElevation verifies Patch M5: a workspace MEMBER
// promoted to MANAGER inside the target crew may create agents in it.
func TestCovACCreate_CrewRoleElevation(t *testing.T) {
	r := newCovACRig(t)
	// Distinct member user, workspace role MEMBER, crew role MANAGER.
	memberID := "member-elevated"
	if _, err := r.db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, 'm@x', 'M')`, memberID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := r.db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('wm-elev', ?, ?, 'MEMBER')`, r.wsID, memberID); err != nil {
		t.Fatalf("seed ws member: %v", err)
	}
	if _, err := r.db.Exec(`INSERT INTO crew_members (id, crew_id, user_id, role) VALUES ('cm-elev', ?, ?, 'MANAGER')`, r.crewID, memberID); err != nil {
		t.Fatalf("seed crew member: %v", err)
	}

	body := `{"name":"Elevated","slug":"elevated","crew_id":"crew-ac"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents", strings.NewReader(body))
	req = withWorkspaceUser(req, memberID, r.wsID, "MEMBER")
	rec := httptest.NewRecorder()
	r.h.Create(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("elevated create: status = %d; body=%s", rec.Code, rec.Body.String())
	}

	// Control: plain MEMBER without crew elevation is forbidden.
	plainID := "member-plain"
	if _, err := r.db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, 'p@x', 'P')`, plainID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := r.db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('wm-plain', ?, ?, 'MEMBER')`, r.wsID, plainID); err != nil {
		t.Fatalf("seed ws member: %v", err)
	}
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/agents", strings.NewReader(`{"name":"Nope","slug":"nope-x","crew_id":"crew-ac"}`))
	req2 = withWorkspaceUser(req2, plainID, r.wsID, "MEMBER")
	rec2 := httptest.NewRecorder()
	r.h.Create(rec2, req2)
	if rec2.Code != http.StatusForbidden {
		t.Errorf("plain member create: status = %d, want 403; body=%s", rec2.Code, rec2.Body.String())
	}
}

// TestCovACCreate_CrewRoleLookupError500 drives the CrewRoleFromDB error
// branch: a closed DB with a crew_id + caller user forces the lookup to
// fail before any other query runs.
func TestCovACCreate_CrewRoleLookupError500(t *testing.T) {
	r := newCovACRig(t)
	r.db.Close()
	rec := httptest.NewRecorder()
	r.h.Create(rec, r.req(t, "ADMIN", `{"name":"X Y","slug":"x-y","crew_id":"crew-ac"}`))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// TestCovACCreate_LeadCheckError500 reaches the existing-lead query error:
// no user in context (CrewRoleFromDB short-circuits without DB), closed DB,
// LEAD role — the first real query is the lead check.
func TestCovACCreate_LeadCheckError500(t *testing.T) {
	r := newCovACRig(t)
	r.db.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents",
		strings.NewReader(`{"name":"Lead Z","slug":"lead-z","crew_id":"crew-ac","agent_role":"LEAD"}`))
	req = req.WithContext(withWorkspace(req.Context(), r.wsID, "ADMIN"))
	rec := httptest.NewRecorder()
	r.h.Create(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// TestCovACCreate_SlugCheckError500 reaches the slug-uniqueness query error
// the same way, with a plain AGENT (no lead query first).
func TestCovACCreate_SlugCheckError500(t *testing.T) {
	r := newCovACRig(t)
	r.db.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents",
		strings.NewReader(`{"name":"Solo","slug":"solo"}`))
	req = req.WithContext(withWorkspace(req.Context(), r.wsID, "ADMIN"))
	rec := httptest.NewRecorder()
	r.h.Create(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}
