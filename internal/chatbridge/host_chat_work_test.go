package chatbridge

import (
	"encoding/json"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/runauthority"
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

func TestHostChatInheritedOriginKeepsExistingOwnership(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if _, present := payload["host_chat_work"]; present {
			t.Error("inherited origin acquired ordinary host-chat admission flag")
		}
		if _, present := payload["human_authority"]; present {
			t.Error("origin provenance replaced by human receipt")
		}
		if payload["chat_origin_id"] != "existing-dispatcher-origin" {
			t.Errorf("origin lost: %+v", payload)
		}
		// Existing owner does not implement the new ownership handshake.
		_, _ = w.Write([]byte(`{"id":"inherited-run","status":"RUNNING"}`))
	}))
	defer server.Close()
	resolver := NewIPCResolver(server.URL, "synthetic-host-token", slog.Default())
	ctx := chataudience.WithReceipt(t.Context(), &chataudience.Receipt{UserID: "human", WorkspaceID: "ws", AgentID: "agent", ChatID: "chat"})
	ctx = WithHostChatWorkAdmission(runauthority.WithOriginID(ctx, "existing-dispatcher-origin"))
	if err := resolver.CreateRun(ctx, "inherited-run", "agent", "chat", "ws", "USER", nil); err != nil {
		t.Fatalf("inherited execution rejected for new handshake: %v", err)
	}
	if resolver.takeHostChatWork("inherited-run") {
		t.Fatal("inherited dispatcher attempt adopted by chat lifecycle")
	}
}
