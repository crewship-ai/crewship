package chatbridge

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/runauthority"
)

// Exercise the actual IPC serializer and HTTP transport. The receiver captures
// the wire contract; this test does not claim database admission or agent execution.
func TestCreateRunCarriesCombinedArtifactAndGenericAuthority(t *testing.T) {
	type wireRun struct {
		ID                     string                `json:"id"`
		AgentID                string                `json:"agent_id"`
		ChatID                 string                `json:"chat_id"`
		WorkspaceID            string                `json:"workspace_id"`
		TriggerType            string                `json:"trigger_type"`
		HumanAuthority         *chataudience.Receipt `json:"human_authority"`
		TriggerMessageID       string                `json:"trigger_message_id"`
		ArtifactHumanReceipt   *chataudience.Receipt `json:"artifact_human_receipt"`
		ArtifactHumanMessageID string                `json:"artifact_human_message_id"`
		ArtifactRequestID      string                `json:"artifact_request_id"`
		BoardRequestID         string                `json:"board_request_id"`
		Metadata               map[string]any        `json:"metadata"`
	}
	type capturedRequest struct {
		method, path, token, contentType string
		body                             wireRun
		err                              error
	}
	requests := make(chan capturedRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured := capturedRequest{method: r.Method, path: r.URL.Path, token: r.Header.Get("X-Internal-Token"), contentType: r.Header.Get("Content-Type")}
		captured.err = json.NewDecoder(r.Body).Decode(&captured.body)
		requests <- captured
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(server.Close)

	receipt := &chataudience.Receipt{UserID: "human", ChatID: "chat", WorkspaceID: "workspace", AgentID: "agent", MemberID: "member", MemberRevision: 42, ChatGeneration: "chat-generation", ChatRevision: 17}
	artifactReceipt := *receipt
	metadata := map[string]interface{}{
		"cli_adapter":               "claude",
		"human_authority":           "untrusted metadata cannot replace host context",
		"artifact_human_receipt":    &artifactReceipt,
		"artifact_human_message_id": "persisted-human-message",
		"artifact_request_id":       "explicit-artifact-request",
		"board_request_id":          "",
	}
	ctx := runauthority.WithTriggerMessage(chataudience.WithReceipt(t.Context(), receipt), "persisted-human-message")
	resolver := NewIPCResolver(server.URL, "synthetic-host-token", slog.Default())
	if err := resolver.CreateRun(ctx, "run", "agent", "chat", "workspace", "USER", metadata); err != nil {
		t.Fatal(err)
	}
	var got capturedRequest
	select {
	case got = <-requests:
	default:
		t.Fatal("CreateRun acknowledged without sending its IPC request")
	}
	if got.err != nil || got.method != http.MethodPost || got.path != "/api/v1/internal/runs" || got.token != "synthetic-host-token" || got.contentType != "application/json" {
		t.Fatalf("incorrect IPC request: method=%s path=%s content-type=%s decode=%v", got.method, got.path, got.contentType, got.err)
	}
	body := got.body
	if body.ID != "run" || body.AgentID != "agent" || body.ChatID != "chat" || body.WorkspaceID != "workspace" || body.TriggerType != "USER" {
		t.Fatalf("run identity changed: %+v", body)
	}
	if body.HumanAuthority == nil || body.ArtifactHumanReceipt == nil || *body.HumanAuthority != *receipt || *body.ArtifactHumanReceipt != *receipt {
		t.Fatalf("generic and artifact admission receipts diverged: generic=%+v artifact=%+v", body.HumanAuthority, body.ArtifactHumanReceipt)
	}
	if body.TriggerMessageID != "persisted-human-message" || body.ArtifactHumanMessageID != body.TriggerMessageID || body.ArtifactRequestID != "explicit-artifact-request" || body.BoardRequestID != "" {
		t.Fatalf("trigger or explicit request binding changed: %+v", body)
	}
	if len(body.Metadata) != 1 || body.Metadata["cli_adapter"] != "claude" {
		t.Errorf("ordinary metadata changed or gained reserved authority: %+v", body.Metadata)
	}
	for _, key := range []string{"human_authority", "trigger_message_id", "chat_origin_id"} {
		if _, exists := body.Metadata[key]; exists {
			t.Errorf("reserved authority %q leaked into ordinary metadata", key)
		}
	}
	for _, key := range []string{"artifact_human_receipt", "artifact_human_message_id", "artifact_request_id", "board_request_id"} {
		if _, exists := body.Metadata[key]; exists {
			t.Errorf("reserved authority %q leaked into ordinary metadata", key)
		}
		if _, exists := metadata[key]; !exists {
			t.Errorf("IPC serialization mutated caller metadata %q", key)
		}
	}
}
