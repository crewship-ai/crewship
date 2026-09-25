package sidecar

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPageWebhookRelay_CapabilityOnlyAndRevocation(t *testing.T) {
	token := "pgw_" + strings.Repeat("a", 64)
	for _, status := range []int{200, 403, 429} {
		var got capturedPush
		be := upstream(t, status, `{"result":"from upstream"}`, &got)
		s := newPagePushServer(t, be.URL)
		s.ipc.CrewOnly = true
		req := httptest.NewRequest(http.MethodPost, "http://localhost:9119/page-webhooks/"+token, strings.NewReader(`{"value":42}`))
		req.RemoteAddr = "127.0.0.1:1234"
		w := httptest.NewRecorder()
		s.buildHandler(nil).ServeHTTP(w, req)
		be.Close()
		if w.Code != status {
			t.Fatalf("status=%d want %d: %s", w.Code, status, w.Body.String())
		}
		if got.path != "/api/v1/page-webhooks/"+token || got.method != "POST" || string(got.rawBody) != `{"value":42}` {
			t.Fatalf("wrong relay: %+v", got)
		}
		if got.token != "" {
			t.Fatal("relay must not attach privileged internal authorization")
		}
	}
}
func TestPageWebhookRelay_InvalidCapabilitiesNeverForward(t *testing.T) {
	var got capturedPush
	be := upstream(t, 200, `{}`, &got)
	defer be.Close()
	s := newPagePushServer(t, be.URL)
	for _, token := range []string{"", "pgw_short", "pgw_" + strings.Repeat("z", 64), "pgw_" + strings.Repeat("a", 64) + "/../admin"} {
		w := httptest.NewRecorder()
		s.handlePageWebhook(w, httptest.NewRequest("POST", "/page-webhooks/"+token, strings.NewReader(`{}`)))
		if w.Code != 400 {
			t.Errorf("malformed capability status %d", w.Code)
		}
	}
	if got.path != "" {
		t.Fatal("invalid capability reached upstream")
	}
}
