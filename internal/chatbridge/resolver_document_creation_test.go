package chatbridge

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/documents"
	"github.com/crewship-ai/crewship/internal/runauthority"
)

func TestCreateRunPromotesOnlyBoundTypedDocumentCreation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		receipt bool
		origin  bool
		intent  any
		want    bool
	}{
		{"bound", true, false, &documents.CreationIntent{SubmissionID: "original-message"}, true},
		{"no receipt", false, false, &documents.CreationIntent{SubmissionID: "original-message"}, false},
		{"origin", true, true, &documents.CreationIntent{SubmissionID: "original-message"}, false},
		{"raw metadata", true, false, map[string]any{"submission_id": "forged"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var payload map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				w.WriteHeader(http.StatusCreated)
			}))
			defer server.Close()
			ctx := runauthority.WithTriggerMessage(t.Context(), "original-message")
			if tc.receipt {
				ctx = chataudience.WithReceipt(ctx, &chataudience.Receipt{UserID: "human", ChatID: "chat", AgentID: "agent", WorkspaceID: "workspace"})
			}
			if tc.origin {
				ctx = runauthority.WithOriginID(ctx, "origin")
			}
			meta := map[string]any{"document_creation_intent": tc.intent, "document_request_id": "forged", "artifact_human_receipt": "forged", "cli_adapter": "test"}
			if err := NewIPCResolver(server.URL, "host", slog.Default()).CreateRun(ctx, "run", "agent", "chat", "workspace", "USER", meta); err != nil {
				t.Fatal(err)
			}
			_, present := payload["document_creation_intent"]
			if present != tc.want {
				t.Fatalf("creation promoted=%v want%v", present, tc.want)
			}
			metadata := payload["metadata"].(map[string]any)
			for _, key := range []string{"document_creation_intent", "document_request_id", "artifact_human_receipt"} {
				if _, ok := metadata[key]; ok {
					t.Errorf("reserved %s leaked into generic metadata", key)
				}
			}
			if metadata["cli_adapter"] != "test" {
				t.Fatal("ordinary metadata lost")
			}
			if _, ok := payload["document_request_id"]; ok {
				t.Fatal("selected request promoted")
			}
		})
	}
}
