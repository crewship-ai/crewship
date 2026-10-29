package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/mooring"
)

func TestCraftNativeAgentTaskDefaultOffOverRealRouter(t *testing.T) {
	f, native, _ := craftNativeHTTPFixture(t)
	token := mintIngressScopedToken(t, f.db, f.userID, "native-agent-task-off", "pages:*")
	clockCalls := 0
	router, err := NewRouter(f.db, "native-agent-task-original-secret-32", newTestLogger(), WithCraftNativeHTTP(CraftNativeHTTPConfig{Enabled: true, AgentsEnabled: false, newOwnerClock: func() (mooring.OwnerClock, error) { clockCalls++; return native.clock, nil }}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	if clockCalls != 1 || !router.craftNativeHTTP.enabled || router.craftNativeHTTP.agents.enabled || router.craftNativeHTTP.clock != native.clock {
		t.Fatal("real-router test must enable the editor with injected deterministic clock and keep agents disabled")
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/pages/@native-page/craft/tasks?workspace_id="+f.wsID, strings.NewReader(`{"chat_id":"chat","message_id":"message","whole_document":true}`))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("default-off native agent task must refuse explicitly: status=%d body=%s", w.Code, w.Body)
	}
}

func TestCraftNativeRouterClockFactoryErrorFailsClosed(t *testing.T) {
	f, _, _ := craftNativeHTTPFixture(t)
	sentinel := errors.New("deterministic native owner clock unavailable")
	calls := 0
	router, err := NewRouter(f.db, "native-agent-task-original-secret-32", newTestLogger(), WithCraftNativeHTTP(CraftNativeHTTPConfig{Enabled: true, AgentsEnabled: false, newOwnerClock: func() (mooring.OwnerClock, error) { calls++; return nil, sentinel }}))
	if router != nil {
		router.Shutdown()
	}
	if !errors.Is(err, sentinel) || calls != 1 || router != nil {
		t.Fatalf("native clock failure must refuse router startup: calls=%d router=%v err=%v", calls, router != nil, err)
	}
}
