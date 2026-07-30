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

// Address suggestions.
//
// The question they answer — "how would I know my own machine's address, when the
// thing that dials is a server somewhere else" — is one the operator genuinely
// cannot answer and the daemon can. The trust rule matters: a suggestion is a URL
// the operator may then ask the server to dial, so a header must not be able to
// put an arbitrary address in front of them.
func TestJudgeSuggestions_OffersTheServerAndTheCaller(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/admin/keeper/judge/models", nil)
	req.RemoteAddr = "192.168.1.118:54321"

	got := judgeSuggestions(req)
	if len(got) < 2 {
		t.Fatalf("got %d suggestions, want the server and the caller", len(got))
	}
	if got[0].URL != "http://localhost:11434" {
		t.Errorf("first suggestion = %q, want the server's own loopback", got[0].URL)
	}
	if got[1].URL != "http://192.168.1.118:11434" {
		t.Errorf("second suggestion = %q, want the caller's address", got[1].URL)
	}
	// The label has to say WHICH machine, because that is the entire confusion.
	if !strings.Contains(strings.ToLower(got[1].Label), "browsing from") {
		t.Errorf("label %q does not identify the machine", got[1].Label)
	}
}

// Behind a same-box reverse proxy the peer is loopback and tells us nothing; the
// forwarded hop is where the real LAN address appears. Only a PRIVATE one is
// taken — a public or spoofed-arbitrary hop must not become a suggested dial
// target.
func TestJudgeSuggestions_TakesOnlyAPrivateForwardedHop(t *testing.T) {
	for _, tc := range []struct {
		name, xff string
		wantURL   string
	}{
		{"private hop is used", "192.168.1.50", "http://192.168.1.50:11434"},
		{"public hop is ignored", "8.8.8.8", ""},
		{"garbage hop is ignored", "not-an-ip", ""},
		{"first private hop in a chain", "8.8.8.8, 10.0.0.7", "http://10.0.0.7:11434"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v1/admin/keeper/judge/models", nil)
			req.RemoteAddr = "127.0.0.1:9999" // the proxy
			req.Header.Set("X-Forwarded-For", tc.xff)

			got := judgeSuggestions(req)
			var caller string
			if len(got) > 1 {
				caller = got[1].URL
			}
			if caller != tc.wantURL {
				t.Errorf("caller suggestion = %q, want %q", caller, tc.wantURL)
			}
			// The server's own loopback is always offered, whatever the header says.
			if got[0].URL != "http://localhost:11434" {
				t.Errorf("lost the server suggestion: %q", got[0].URL)
			}
		})
	}
}

// A direct loopback caller (curl on the box) gets no second suggestion: it would
// be the same address as the first, and a duplicate reads as two options.
func TestJudgeSuggestions_NoDuplicateForALoopbackCaller(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/v1/admin/keeper/judge/models", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	if got := judgeSuggestions(req); len(got) != 1 {
		t.Errorf("got %d suggestions for a loopback caller, want just the server", len(got))
	}
}
