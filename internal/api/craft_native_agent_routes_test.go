package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/craftapps"
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

// With only downloadable release components (#5315), a system without a
// suspend-aware clock still starts: Word and Sheet are unavailable and say
// why, and nothing can be installed. An operator's explicit native profile
// keeps refusing to start (above).
func TestCraftNativeClockFailureWithReleaseComponentsStartsUnavailable(t *testing.T) {
	f, _, _ := craftNativeHTTPFixture(t)
	install := func(context.Context, string, io.Reader) (CraftNativeDocumentProfile, craftapps.Component, error) {
		t.Fatal("install called without a clock")
		return CraftNativeDocumentProfile{}, craftapps.Component{}, nil
	}
	router, err := NewRouter(f.db, "native-agent-task-original-secret-32", newTestLogger(), WithCraftNativeHTTP(CraftNativeHTTPConfig{
		Enabled: true, AgentsEnabled: true, Install: install,
		Components:    map[string]craftapps.Component{craftapps.Word: {Reason: craftapps.ReasonNotInstalled}},
		newOwnerClock: func() (mooring.OwnerClock, error) { return nil, errors.New("no boot clock") }}))
	if err != nil {
		t.Fatalf("release components without a clock refused startup: %v", err)
	}
	t.Cleanup(router.Shutdown)
	if router.craftNativeHTTP.enabled || router.craftNativeHTTPConfig.Install != nil || router.craftNativeHTTPConfig.Components[craftapps.Word].Reason != craftapps.ReasonClockUnsupported {
		t.Fatalf("without a clock: enabled=%v components=%+v", router.craftNativeHTTP.enabled, router.craftNativeHTTPConfig.Components)
	}
	if (&CraftAppsAdminHandler{r: router}).installable(craftapps.Word) {
		t.Fatal("installable without a clock")
	}
}
