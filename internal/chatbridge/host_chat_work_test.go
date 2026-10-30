package chatbridge

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHostChatOwnershipAcknowledgementRequired(t *testing.T) {
	for _, receipt := range []string{`{}`, `{"host_chat_work":"unknown"}`, `not json`, `{"host_chat_work":"v1"}`, `{"host_chat_work":"external"}`} {
		t.Run(receipt, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				if payload["host_chat_work"] != true {
					t.Error("host admission marker absent")
				}
				_, _ = w.Write([]byte(receipt))
			}))
			defer server.Close()
			resolver := NewIPCResolver(server.URL, "synthetic-host-token", slog.Default())
			err := resolver.CreateRun(WithHostChatWorkAdmission(t.Context()), "run", "agent", "chat", "ws", "USER", nil)
			valid := receipt == `{"host_chat_work":"v1"}` || receipt == `{"host_chat_work":"external"}`
			if (err == nil) != valid {
				t.Fatalf("acknowledgement err=%v valid=%v", err, valid)
			}
			if resolver.takeHostChatWork("run") != (receipt == `{"host_chat_work":"v1"}`) {
				t.Fatal("ownership acknowledgement mismatched")
			}
			if resolver.takeHostChatWork("run") {
				t.Fatal("ownership capability reusable")
			}
		})
	}
}
