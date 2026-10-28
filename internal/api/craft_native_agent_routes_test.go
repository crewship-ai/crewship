package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCraftNativeAgentTaskDefaultOffOverRealRouter(t *testing.T) {
	f, _, _ := craftNativeHTTPFixture(t)
	token := mintIngressScopedToken(t, f.db, f.userID, "native-agent-task-off", "pages:*")
	router, err := NewRouter(f.db, "native-agent-task-original-secret-32", newTestLogger(), WithCraftNativeHTTP(CraftNativeHTTPConfig{Enabled: true}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/pages/@native-page/craft/tasks?workspace_id="+f.wsID, strings.NewReader(`{"chat_id":"chat","message_id":"message","whole_document":true}`))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("default-off native agent task must refuse explicitly: status=%d body=%s", w.Code, w.Body)
	}
}
