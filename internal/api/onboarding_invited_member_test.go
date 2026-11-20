package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/services"
)

// Issue #4876: onboarding sets up a workspace of one's own. Someone invited
// into a workspace another person set up must not be sent into the wizard,
// and no onboarding route may rename that workspace, deploy crews into it
// (wired to the owner's credential) or create the Crewship Guide there on
// their behalf. A user invited first who later created a workspace of their
// own is onboarded into THAT workspace, never the shared one.
//
// Every row acts on a shared workspace owned by test-user-id that holds the
// owner's model credential; the assertions are about that workspace.

const (
	invSharedName = "Test"
	invHijackName = "Hijacked"
)

type invitedCase struct {
	name string
	// role of the caller in the shared workspace; "OWNER" means the caller
	// is the owner itself.
	role string
	// ownLater: the caller also owns a workspace they created after joining.
	ownLater bool
}

var invitedCases = []invitedCase{
	{name: "OWNER", role: "OWNER"},
	{name: "ADMIN", role: "ADMIN"},
	{name: "MANAGER", role: "MANAGER"},
	{name: "MEMBER", role: "MEMBER"},
	{name: "VIEWER", role: "VIEWER"},
	{name: "invited_then_own_workspace", role: "MEMBER", ownLater: true},
}

func newInvitedFixture(t *testing.T, tc invitedCase) (*OnboardingHandler, string, string, string) {
	t.Helper()
	withTokenProbeSkipped(t)
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	ownerID := seedTestUser(t, db)
	sharedWS := seedTestWorkspace(t, db, ownerID)
	execOrFatal(t, db, `UPDATE workspace_members SET created_at = '2026-01-01T00:00:00Z' WHERE workspace_id = ?`, sharedWS)
	if err := insertOnboardingCredential(context.Background(), db, ownerID, sharedWS, "API Key", "ANTHROPIC", "ANTHROPIC_API_KEY", "sk-ant-oat01-fake", isoMillisNow()); err != nil {
		t.Fatalf("insert owner credential: %v", err)
	}
	covOHCSeedBuiltinTemplate(t, db, "inv-crew", "Invited Crew")

	callerID, ownWS := ownerID, ""
	if tc.role != "OWNER" {
		callerID = "invited-user-id"
		execOrFatal(t, db, `INSERT INTO users (id, email, full_name) VALUES (?, 'invited@example.com', 'Invited User')`, callerID)
		execOrFatal(t, db, `INSERT INTO workspace_members (id, workspace_id, user_id, role, created_at) VALUES ('m-invited', ?, ?, ?, '2026-02-01T00:00:00Z')`, sharedWS, callerID, tc.role)
	}
	if tc.ownLater {
		ownWS = "own-workspace-id"
		execOrFatal(t, db, `INSERT INTO workspaces (id, name, slug) VALUES (?, 'Own', 'own')`, ownWS)
		execOrFatal(t, db, `INSERT INTO workspace_members (id, workspace_id, user_id, role, created_at) VALUES ('m-own', ?, ?, 'OWNER', '2026-03-01T00:00:00Z')`, ownWS, callerID)
	}
	h := NewOnboardingHandler(db, services.NewOnboardingService(db, testLogger(), generateCUID), testLogger())
	return h, sharedWS, callerID, ownWS
}

type invSnapshot struct {
	name                     string
	crews, agents, agentCred int
}

func invSnap(t *testing.T, h *OnboardingHandler, wsID string) invSnapshot {
	t.Helper()
	var s invSnapshot
	if err := h.db.QueryRow(`SELECT name FROM workspaces WHERE id = ?`, wsID).Scan(&s.name); err != nil {
		t.Fatalf("read workspace: %v", err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM crews WHERE workspace_id = ?`, wsID).Scan(&s.crews); err != nil {
		t.Fatalf("count crews: %v", err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM agents WHERE workspace_id = ?`, wsID).Scan(&s.agents); err != nil {
		t.Fatalf("count agents: %v", err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM agent_credentials ac JOIN agents a ON a.id = ac.agent_id WHERE a.workspace_id = ?`, wsID).Scan(&s.agentCred); err != nil {
		t.Fatalf("count agent credentials: %v", err)
	}
	return s
}

func invCall(h *OnboardingHandler, fn http.HandlerFunc, userID, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	w := httptest.NewRecorder()
	fn(w, req)
	return w
}

// want describes the expected outcome on the SHARED workspace per route.
type invWant struct {
	code    int
	writes  bool // crews/agents may be created in the shared workspace
	renamed bool // shared workspace renamed to invHijackName
}

func TestOnboardingRoutes_InvitedMemberCannotOnboardSharedWorkspace(t *testing.T) {
	forbidden := invWant{code: http.StatusForbidden}
	routes := []struct {
		route string
		want  map[string]invWant
		call  func(t *testing.T, h *OnboardingHandler, sharedWS, callerID string) *httptest.ResponseRecorder
	}{
		{
			route: "status",
			want: map[string]invWant{
				"OWNER": {code: 200, writes: true}, "ADMIN": {code: 200}, "MANAGER": {code: 200},
				"MEMBER": {code: 200}, "VIEWER": {code: 200}, "invited_then_own_workspace": {code: 200},
			},
			call: func(t *testing.T, h *OnboardingHandler, _, callerID string) *httptest.ResponseRecorder {
				return invCall(h, h.Status, callerID, "")
			},
		},
		{
			route: "setup_template",
			want: map[string]invWant{
				"OWNER": {code: 201, writes: true, renamed: true}, "ADMIN": forbidden, "MANAGER": forbidden,
				"MEMBER": forbidden, "VIEWER": forbidden, "invited_then_own_workspace": {code: 201},
			},
			call: func(t *testing.T, h *OnboardingHandler, _, callerID string) *httptest.ResponseRecorder {
				return invCall(h, h.Setup, callerID, `{"crew_template_slug":"inv-crew","workspace_name":"`+invHijackName+`"}`)
			},
		},
		{
			route: "setup_applied_proposal",
			want: map[string]invWant{
				"OWNER": {code: 200, renamed: true}, "ADMIN": forbidden, "MANAGER": forbidden,
				"MEMBER": forbidden, "VIEWER": forbidden, "invited_then_own_workspace": {code: 400},
			},
			call: func(t *testing.T, h *OnboardingHandler, sharedWS, callerID string) *httptest.ResponseRecorder {
				// The owner's chat applied a proposal in the shared workspace.
				oph := NewOnboardingProposalHandler(h.db, newTestLogger())
				opSeedTemplate(t, h.db, sharedWS, "eng-crew", opTwoAgentRoster())
				_, p := opCreate(t, oph, "test-user-id", sharedWS, map[string]string{"crew_name": "Chat Crew", "template_slug": "eng-crew"})
				if rr := opApply(oph, "test-user-id", sharedWS, p.ID, "OWNER", nil); rr.Code != http.StatusCreated {
					t.Fatalf("fixture apply = %d %s", rr.Code, rr.Body.String())
				}
				return invCall(h, h.Setup, callerID, fmt.Sprintf(`{"applied_proposal_id":%q,"workspace_name":%q}`, p.ID, invHijackName))
			},
		},
		{
			route: "setup_blank",
			want: map[string]invWant{
				// Blank setup stays open to the crew-create tier (OWNER/ADMIN/
				// MANAGER, TestOnboardingBlankConfiguredAudit) but only the
				// owner may rename the workspace through it.
				"OWNER": {code: 201, writes: true, renamed: true}, "ADMIN": {code: 201, writes: true}, "MANAGER": {code: 201, writes: true},
				"MEMBER": forbidden, "VIEWER": forbidden, "invited_then_own_workspace": {code: 201},
			},
			call: func(t *testing.T, h *OnboardingHandler, _, callerID string) *httptest.ResponseRecorder {
				return invCall(h, h.Setup, callerID, `{"crew_name":"Blank crew","agent_name":"Blank agent","llm_provider":"OLLAMA","workspace_name":"`+invHijackName+`"}`)
			},
		},
		{
			route: "setup_agent_start",
			want: map[string]invWant{
				"OWNER": {code: 200, writes: true}, "ADMIN": forbidden, "MANAGER": forbidden,
				"MEMBER": forbidden, "VIEWER": forbidden,
				// Their own workspace has no credential yet.
				"invited_then_own_workspace": {code: http.StatusPreconditionRequired},
			},
			call: func(t *testing.T, h *OnboardingHandler, _, callerID string) *httptest.ResponseRecorder {
				return invCall(h, h.StartSetupAgent, callerID, "")
			},
		},
	}

	for _, rt := range routes {
		for _, tc := range invitedCases {
			t.Run(rt.route+"/"+tc.name, func(t *testing.T) {
				want := rt.want[tc.name]
				h, sharedWS, callerID, ownWS := newInvitedFixture(t, tc)
				w := rt.call(t, h, sharedWS, callerID)
				after := invSnap(t, h, sharedWS)
				if w.Code != want.code {
					t.Errorf("HTTP %d, want %d; body=%s", w.Code, want.code, w.Body.String())
				}
				if renamed := after.name == invHijackName; renamed != want.renamed {
					t.Errorf("shared workspace renamed = %v (name %q), want %v", renamed, after.name, want.renamed)
				}
				if !want.writes {
					base := 0
					if rt.route == "setup_applied_proposal" {
						base = 1 // the owner's applied crew
					}
					if after.crews != base {
						t.Errorf("shared workspace crews = %d, want %d", after.crews, base)
					}
					if rt.route != "setup_applied_proposal" && (after.agents != 0 || after.agentCred != 0) {
						t.Errorf("shared workspace agents = %d, agent credential grants = %d, want 0", after.agents, after.agentCred)
					}
				}
				// The mixed user is onboarded into the workspace they own.
				if tc.ownLater && (rt.route == "setup_template" || rt.route == "setup_blank") {
					if own := invSnap(t, h, ownWS); own.crews == 0 {
						t.Errorf("own workspace got no crew; setup targeted the wrong workspace")
					}
				}
				if rt.route == "status" && w.Code == 200 {
					var body map[string]bool
					_ = json.Unmarshal(w.Body.Bytes(), &body)
					wantCompleted := tc.role != "OWNER" && !tc.ownLater
					if body["completed"] != wantCompleted {
						t.Errorf("completed = %v, want %v", body["completed"], wantCompleted)
					}
				}
			})
		}
	}
}
