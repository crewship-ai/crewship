package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #3789 — every endpoint that writes an avatar style accepts the two DiceBear
// 10 voxel styles and refuses a slug the web app cannot draw. A refused slug
// used to be stored and then silently rendered as the default, which is the
// "picked Open Peeps, kept the same face" bug one layer down.

var avatarStyleCases = []struct {
	name   string
	style  any
	status int
}{
	{"voxel-bot", "voxel-bot", http.StatusOK},
	{"voxel-art", "voxel-art", http.StatusOK},
	{"a 9.4 style", "lorelei", http.StatusOK},
	{"the old default", "bottts-neutral", http.StatusOK},
	{"a phantom label", "robots", http.StatusBadRequest},
	{"a label, not a slug", "Voxel Bot", http.StatusBadRequest},
	{"a DiceBear style the app does not offer", "blobs", http.StatusBadRequest},
}

func okStatus(code int) bool { return code >= 200 && code < 300 }

func TestAvatarStyleValidation_AgentCreate(t *testing.T) {
	for i, tc := range avatarStyleCases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewAgentHandler(setupTestDB(t), newTestLogger())
			userID := seedTestUser(t, h.db)
			wsID := seedTestWorkspace(t, h.db, userID)
			body, _ := json.Marshal(map[string]any{
				"name": "Voxel", "slug": "voxel-" + string(rune('a'+i)), "agent_role": "AGENT", "avatar_style": tc.style,
			})
			req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/agents?workspace_id="+wsID, strings.NewReader(string(body))), userID, wsID, "OWNER")
			rr := httptest.NewRecorder()
			h.Create(rr, req)
			if okStatus(tc.status) != okStatus(rr.Code) {
				t.Fatalf("create with avatar_style %v = %d, want %d; body=%s", tc.style, rr.Code, tc.status, rr.Body.String())
			}
			if !okStatus(rr.Code) && !strings.Contains(rr.Body.String(), "voxel-bot") {
				t.Errorf("the refusal should list the accepted slugs, got %s", rr.Body.String())
			}
		})
	}
}

func TestAvatarStyleValidation_AgentUpdate(t *testing.T) {
	for _, tc := range avatarStyleCases {
		t.Run(tc.name, func(t *testing.T) {
			h, userID, wsID := newAvatarAgentEnv(t)
			body, _ := json.Marshal(map[string]any{"avatar_style": tc.style})
			req := httptest.NewRequest("PATCH", "/api/v1/agents/ag-av", strings.NewReader(string(body)))
			req.SetPathValue("agentId", "ag-av")
			req = withWorkspaceUser(req, userID, wsID, "ADMIN")
			rr := httptest.NewRecorder()
			h.Update(rr, req)
			if rr.Code != tc.status {
				t.Fatalf("PATCH avatar_style %v = %d, want %d; body=%s", tc.style, rr.Code, tc.status, rr.Body.String())
			}
		})
	}
	t.Run("null still clears the style", func(t *testing.T) {
		h, userID, wsID := newAvatarAgentEnv(t)
		req := httptest.NewRequest("PATCH", "/api/v1/agents/ag-av", strings.NewReader(`{"avatar_style":null}`))
		req.SetPathValue("agentId", "ag-av")
		req = withWorkspaceUser(req, userID, wsID, "ADMIN")
		rr := httptest.NewRecorder()
		h.Update(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("PATCH avatar_style null = %d; body=%s", rr.Code, rr.Body.String())
		}
	})
}

func newAvatarCrewEnv(t *testing.T) (*CrewHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCrewRow(t, db, "crew-av", wsID, "Av", "av")
	seedAgentRow(t, db, "ag-1", wsID, "crew-av", "A1", "a1", "AGENT")
	return NewCrewHandler(db, newTestLogger()), userID, wsID
}

func TestAvatarStyleValidation_CrewUpdate(t *testing.T) {
	for _, tc := range avatarStyleCases {
		t.Run(tc.name, func(t *testing.T) {
			h, userID, wsID := newAvatarCrewEnv(t)
			body, _ := json.Marshal(map[string]any{"avatar_style": tc.style})
			req := httptest.NewRequest("PATCH", "/api/v1/crews/crew-av", strings.NewReader(string(body)))
			req.SetPathValue("crewId", "crew-av")
			req = withWorkspaceUser(req, userID, wsID, "OWNER")
			rr := httptest.NewRecorder()
			h.Update(rr, req)
			if rr.Code != tc.status {
				t.Fatalf("crew PATCH avatar_style %v = %d, want %d; body=%s", tc.style, rr.Code, tc.status, rr.Body.String())
			}
		})
	}
}

func TestAvatarStyleValidation_ApplyAvatarStyle(t *testing.T) {
	for _, tc := range avatarStyleCases {
		t.Run(tc.name, func(t *testing.T) {
			h, userID, wsID := newAvatarCrewEnv(t)
			body, _ := json.Marshal(map[string]any{"avatar_style": tc.style})
			req := httptest.NewRequest("POST", "/api/v1/crews/crew-av/apply-avatar-style", strings.NewReader(string(body)))
			req.SetPathValue("crewId", "crew-av")
			req = withWorkspaceUser(req, userID, wsID, "OWNER")
			rr := httptest.NewRecorder()
			h.ApplyAvatarStyle(rr, req)
			if rr.Code != tc.status {
				t.Fatalf("apply-avatar-style %v = %d, want %d; body=%s", tc.style, rr.Code, tc.status, rr.Body.String())
			}
		})
	}
}
