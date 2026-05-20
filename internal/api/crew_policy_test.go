package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/crewship-ai/crewship/internal/policy"
)

// TestCrewPolicy_Get_DefaultsToGuidedWarn — a fresh crew (created
// without explicitly setting autonomy_level / behavior_mode) reads
// back as guided/warn. Locks the v98 migration's "default for new
// crews" contract from the API surface, not just the DB.
func TestCrewPolicy_Get_DefaultsToGuidedWarn(t *testing.T) {
	h, wsID, crewID := setupPolicyHandler(t)

	w := doPolicyRequest(h, http.MethodGet, "/api/v1/crews/"+crewID+"/policy", nil, wsID, "u1")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var got crewPolicyResponse
	mustDecode(t, w.Body.Bytes(), &got)
	if got.AutonomyLevel != "guided" {
		t.Errorf("autonomy_level = %q, want guided", got.AutonomyLevel)
	}
	if got.BehaviorMode != "warn" {
		t.Errorf("behavior_mode = %q, want warn", got.BehaviorMode)
	}
}

// TestCrewPolicy_Put_HappyPath — operator sets trusted/warn with a
// reason; subsequent GET returns the new values plus audit triple.
func TestCrewPolicy_Put_HappyPath(t *testing.T) {
	h, wsID, crewID := setupPolicyHandler(t)

	body := crewPolicyUpdateBody{
		AutonomyLevel: "trusted",
		BehaviorMode:  "warn",
		Reason:        "engineering crew has earned the uplift",
	}
	w := doPolicyRequest(h, http.MethodPut, "/api/v1/crews/"+crewID+"/policy", body, wsID, "u1")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Read back via GET — confirms write actually landed AND the
	// resolver cache was invalidated (otherwise we'd see stale
	// guided/warn from the cache populated by the Put's pre-write
	// read — which doesn't exist in this handler, but the invariant
	// holds).
	w2 := doPolicyRequest(h, http.MethodGet, "/api/v1/crews/"+crewID+"/policy", nil, wsID, "u1")
	var got crewPolicyResponse
	mustDecode(t, w2.Body.Bytes(), &got)
	if got.AutonomyLevel != "trusted" {
		t.Errorf("after PUT, autonomy_level = %q, want trusted", got.AutonomyLevel)
	}
	if got.SetByUserID != "u1" {
		t.Errorf("set_by_user_id = %q, want u1 (audit triple must be populated)", got.SetByUserID)
	}
	if got.Reason != "engineering crew has earned the uplift" {
		t.Errorf("reason not persisted: %q", got.Reason)
	}
}

// TestCrewPolicy_Put_RejectsBogusEnum — guards the enum at the API
// boundary. Without this, a malformed JSON could land an invalid
// row (the v98 DB CHECK would also catch it, but the API should
// fail fast with a 400 + clear message instead of a 500-flavored
// constraint error).
func TestCrewPolicy_Put_RejectsBogusEnum(t *testing.T) {
	h, wsID, crewID := setupPolicyHandler(t)

	body := crewPolicyUpdateBody{
		AutonomyLevel: "yolo",
		BehaviorMode:  "warn",
	}
	w := doPolicyRequest(h, http.MethodPut, "/api/v1/crews/"+crewID+"/policy", body, wsID, "u1")
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for bogus autonomy_level, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCrewPolicy_Put_RejectsForbiddenFullBlock — the API blocks the
// combination the policy package's Validate() explicitly forbids.
// Without this gate, operators trying to flip a crew to full + block
// would land in a runtime contradiction (opt-in trust + opt-in
// restriction).
func TestCrewPolicy_Put_RejectsForbiddenFullBlock(t *testing.T) {
	h, wsID, crewID := setupPolicyHandler(t)

	body := crewPolicyUpdateBody{
		AutonomyLevel: "full",
		BehaviorMode:  "block",
		Reason:        "want both autonomy AND block — should fail",
	}
	w := doPolicyRequest(h, http.MethodPut, "/api/v1/crews/"+crewID+"/policy", body, wsID, "u1")
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for full+block, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCrewPolicy_Put_FullRequiresReason — PRD §6 F2 makes the reason
// mandatory only for the highest-autonomy flip, because that's the
// one most likely to need post-hoc justification if something
// surprising happens.
func TestCrewPolicy_Put_FullRequiresReason(t *testing.T) {
	h, wsID, crewID := setupPolicyHandler(t)

	body := crewPolicyUpdateBody{
		AutonomyLevel: "full",
		BehaviorMode:  "warn",
		// no reason
	}
	w := doPolicyRequest(h, http.MethodPut, "/api/v1/crews/"+crewID+"/policy", body, wsID, "u1")
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for full without reason, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCrewPolicy_Put_CrossWorkspace_Rejected — operator in ws1 cannot
// flip a crew in ws2. Workspace boundary is enforced at the handler.
func TestCrewPolicy_Put_CrossWorkspace_Rejected(t *testing.T) {
	h, _, _ := setupPolicyHandler(t)
	// Seed a second workspace + crew
	execOrFatal(t, h.db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws2', 'WS2', 'ws2')`)
	execOrFatal(t, h.db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('cr2', 'ws2', 'Crew2', 'crew2')`)

	body := crewPolicyUpdateBody{AutonomyLevel: "trusted", BehaviorMode: "warn"}
	// caller's workspace context is ws1
	w := doPolicyRequest(h, http.MethodPut, "/api/v1/crews/cr2/policy", body, "ws1", "u1")
	if w.Code != http.StatusNotFound {
		t.Errorf("cross-workspace PUT must 404, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCrewPolicy_List_WorkspaceScoped — workspace-scoped list returns
// every crew in the caller's workspace and nothing outside it.
func TestCrewPolicy_List_WorkspaceScoped(t *testing.T) {
	h, wsID, crewID := setupPolicyHandler(t)
	// Add a second crew to the same workspace + a crew in a different ws
	execOrFatal(t, h.db, `INSERT INTO crews (id, workspace_id, name, slug, autonomy_level) VALUES ('cr_extra', ?, 'Extra', 'extra', 'trusted')`, wsID)
	execOrFatal(t, h.db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_other', 'Other', 'ws-other')`)
	execOrFatal(t, h.db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('cr_other_ws', 'ws_other', 'Other crew', 'other-crew')`)

	w := doPolicyRequest(h, http.MethodGet, "/api/v1/policies", nil, wsID, "u1")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var got []crewPolicyResponse
	mustDecode(t, w.Body.Bytes(), &got)
	if len(got) != 2 {
		t.Fatalf("expected 2 crews in ws1, got %d", len(got))
	}
	ids := map[string]bool{}
	for _, p := range got {
		ids[p.CrewID] = true
	}
	if !ids[crewID] || !ids["cr_extra"] {
		t.Errorf("missing expected crews; got %v", ids)
	}
	if ids["cr_other_ws"] {
		t.Error("workspace boundary breached: returned crew from a different ws")
	}
}

// --- helpers ---

func setupPolicyHandler(t *testing.T) (*CrewPolicyHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := "cr_policy_" + wsID
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Policy Crew', 'policy-crew')`, crewID, wsID)

	resolver := policy.NewResolver(db)
	h := NewCrewPolicyHandler(db, resolver, logger)
	return h, wsID, crewID
}

func doPolicyRequest(h *CrewPolicyHandler, method, path string, body any, workspaceID, userID string) *httptest.ResponseRecorder {
	var rdr *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")

	// Inject auth + workspace context. The middleware would normally
	// do this; the test calls handler methods directly so we replicate
	// the context fields the handlers read.
	ctx := context.WithValue(req.Context(), ctxWorkspaceID, workspaceID)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: userID})
	req = req.WithContext(ctx)

	// http.Request.PathValue needs the route to be matched through a
	// ServeMux to populate; use a throwaway mux that registers just
	// the routes under test.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/policies", h.List)
	mux.HandleFunc("GET /api/v1/crews/{crewId}/policy", h.Get)
	mux.HandleFunc("PUT /api/v1/crews/{crewId}/policy", h.Put)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func mustDecode(t *testing.T, raw []byte, dst any) {
	t.Helper()
	if err := json.Unmarshal(raw, dst); err != nil {
		t.Fatalf("decode: %v\nbody: %s", err, string(raw))
	}
}
