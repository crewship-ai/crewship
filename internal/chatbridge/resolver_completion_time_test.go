package chatbridge

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUpdateRunCompletionIsTypedAndLegacyMetadataIsOpaque(t *testing.T) {
	for _, completed := range []string{"", "2026-10-19T10:00:42.125000000Z"} {
		t.Run(completed, func(t *testing.T) {
			var payload map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()
			resolver := NewIPCResolver(server.URL, "synthetic", slog.Default())
			metadata := map[string]any{"captured_completed_at": float64(123)}
			var err error
			if completed == "" {
				err = resolver.UpdateRun(t.Context(), "run", "COMPLETED", nil, nil, metadata)
			} else {
				err = resolver.UpdateRunWithCompletion(t.Context(), "run", "COMPLETED", nil, nil, metadata, completed)
			}
			if err != nil {
				t.Fatal(err)
			}
			got, present := payload["completed_at"]
			if completed == "" && present || completed != "" && got != completed {
				t.Fatalf("typed completion wire: %v", payload)
			}
			if payload["metadata"].(map[string]any)["captured_completed_at"] != float64(123) {
				t.Fatalf("legacy metadata changed: %v", payload)
			}
		})
	}
}
