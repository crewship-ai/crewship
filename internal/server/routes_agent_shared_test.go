package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/conversation"
)

func TestSharedChatIPCIsBoundedAndFailsClosed(t *testing.T) {
	s := newTestServerForT(t)
	s.convStore = nil
	request := func(id string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/chats/"+id+"/shared-messages", nil)
		w := httptest.NewRecorder()
		s.ipcMux.ServeHTTP(w, r)
		return w
	}
	if got := request("missing"); got.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil store = %d", got.Code)
	}
	s.convStore = conversation.NewStore(t.TempDir(), s.logger)
	t.Cleanup(func() { s.convStore.Close() })
	if got := request("missing"); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"messages":[]`) {
		t.Fatalf("missing conversation = %d %s", got.Code, got.Body.String())
	}
	if err := s.convStore.Append(context.Background(), "one", conversation.Message{ID: "m1", Role: conversation.RoleUser, Content: "hello"}); err != nil {
		t.Fatal(err)
	}
	if got := request("one"); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), "hello") {
		t.Fatalf("valid conversation = %d %s", got.Code, got.Body.String())
	}
	if got := request("x..y"); got.Code != http.StatusInternalServerError {
		t.Fatalf("invalid id degraded to success: %d %s", got.Code, got.Body.String())
	}
	for i := 0; i <= sharedTranscriptMaxMessages; i++ {
		if err := s.convStore.Append(context.Background(), "large", conversation.Message{ID: "m", Role: conversation.RoleUser, Content: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if got := request("large"); got.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized conversation status = %d", got.Code)
	}
}
