package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The hosted judge check.
//
// The local Ollama judge had a four-stage check; a hosted one had none. An
// operator picking "Anthropic" and one of several stored API keys — the normal
// case on an orchestration platform, where each key carries its own subscription
// limit — got no feedback until the next real credential request, and if they
// picked the exhausted or wrong-typed key that feedback arrived as a fail-closed
// DENY. These pin that the check answers the question the page asks.

func hostedTestReq(body string) *http.Request {
	return manageReq("POST", "/api/v1/admin/keeper/judge/test-hosted", body)
}

// Without the resolver there is no vault to resolve a key from. 503 rather than a
// green tick: a check that cannot run must not look like one that passed.
func TestJudgeTestHosted_UnwiredResolverIs503(t *testing.T) {
	h := NewAdminKeeperJudgeHandler(nil, newTestLogger())
	rr := httptest.NewRecorder()
	h.TestHosted(rr, hostedTestReq(`{"provider":"anthropic","model":"claude-opus-5"}`))
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("got %d, want 503", rr.Code)
	}
}

func TestJudgeTestHosted_RequiresManageRole(t *testing.T) {
	h := NewAdminKeeperJudgeHandler(nil, newTestLogger())
	req := httptest.NewRequest("POST", "/api/v1/admin/keeper/judge/test-hosted", strings.NewReader("{}"))
	req = req.WithContext(withWorkspace(req.Context(), "ws1", "VIEWER"))
	rr := httptest.NewRecorder()
	h.TestHosted(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("viewer got %d, want 403", rr.Code)
	}
}

// A provider with no model has nothing to ask, and the refusal should say that
// rather than producing an empty-looking failed stage.
func TestJudgeTestHosted_RejectsIncompleteConfig(t *testing.T) {
	db := setupTestDB(t)
	h := NewAdminKeeperJudgeHandler(nil, newTestLogger()).
		WithGovJudge(NewGovModelResolver(db, nil, newTestLogger(), "http://127.0.0.1:11434", "qwen2.5:7b"))

	for _, tc := range []struct{ name, body, want string }{
		{"no provider", `{"model":"claude-opus-5"}`, "provider"},
		{"no model", `{"provider":"anthropic"}`, "model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			h.TestHosted(rr, hostedTestReq(tc.body))
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("got %d, want 400: %s", rr.Code, rr.Body.String())
			}
			if !strings.Contains(strings.ToLower(rr.Body.String()), tc.want) {
				t.Errorf("body %q does not name what is missing", rr.Body.String())
			}
		})
	}
}

// A key that cannot be resolved is stage 1 failing with the reason, and the
// verdict stage SKIPPED rather than failed — "we never got to ask" and "we asked
// and it refused" are different answers, and the second one would send an
// operator looking at the model when the problem is the key.
func TestJudgeTestHosted_UnresolvableKeyFailsStageOneAndSkipsTheRest(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewAdminKeeperJudgeHandler(nil, newTestLogger()).
		WithGovJudge(NewGovModelResolver(db, nil, newTestLogger(), "http://127.0.0.1:11434", "qwen2.5:7b"))

	req := hostedTestReq(`{"provider":"anthropic","model":"claude-opus-5","credential_id":"cred_does_not_exist"}`)
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.TestHosted(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 (a failed check is a result, not an error): %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"ok":false`) {
		t.Errorf("a missing credential reported ok: %s", body)
	}
	if !strings.Contains(body, `"skipped":true`) {
		t.Errorf("the verdict stage was not skipped after the key failed: %s", body)
	}
	// The §4.4 degrade is the contract working — but reporting it as a pass would
	// tell the operator their Anthropic key works when the local judge is what
	// would actually run.
	if !strings.Contains(strings.ToLower(body), "would not be used") &&
		!strings.Contains(strings.ToLower(body), "unavailable") {
		t.Errorf("the failure does not explain what would happen instead: %s", body)
	}
}
