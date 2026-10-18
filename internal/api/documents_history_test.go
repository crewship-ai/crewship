package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// Transport projections are tested separately from the real Node/authority/CLI
// acceptance: this callback is not evidence of authorization or engine behavior.
func TestHumanDocumentHistoryClosedProjection(t *testing.T) {
	for _, tc := range []struct {
		name, checkpoint, response string
		upstream, want             int
	}{
		{"list", "", `{"checkpoints":[{"id":"pin-one","rev":1,"schema":3,"actor":"user:author","created_at":"2026-10-18T00:00:00Z","snapshot":"ENGINE_PRIVATE"}],"epoch":"ENGINE_PRIVATE"}`, 200, 200},
		{"empty list", "", `{"checkpoints":[]}`, 200, 200},
		{"null list", "", `{"checkpoints":null}`, 200, 502},
		{"duplicate pin", "", `{"checkpoints":[{"id":"pin-one","rev":1,"schema":3},{"id":"pin-one","rev":1,"schema":3}]}`, 200, 502},
		{"unsafe revision", "", `{"checkpoints":[{"id":"pin-one","rev":9007199254740992,"schema":3}]}`, 200, 502},
		{"selected", "pin-one", `{"id":"pin-one","rev":1,"schema":3,"content":{"type":"doc","content":[]},"snapshot":"ENGINE_PRIVATE","blocks":"ENGINE_PRIVATE","epoch":"ENGINE_PRIVATE"}`, 200, 200},
		{"wrong pin", "pin-one", `{"id":"other","rev":1,"schema":3,"content":{"type":"doc"}}`, 200, 502},
		{"missing body", "pin-one", `{"id":"pin-one","rev":1,"schema":3}`, 200, 502},
		{"non document", "pin-one", `{"id":"pin-one","rev":1,"schema":3,"content":{"type":"paragraph"}}`, 200, 502},
		{"future schema", "pin-one", `{"id":"pin-one","rev":1,"schema":99,"content":{"type":"doc","content":[{"type":"text","text":"ENGINE_PRIVATE"}]}}`, 200, 409},
		{"denied", "pin-one", `{"error":"ENGINE_PRIVATE"}`, 403, 403},
		{"unavailable", "", `{"error":"ENGINE_PRIVATE"}`, 503, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, ws, user := humanDocumentTransportFixture(t)
			calls := 0
			h.documentCall = func(_ context.Context, workspace, bearer, page, operation string, body any) ([]byte, int, error) {
				calls++
				wantOperation := "checkpoints"
				if tc.checkpoint != "" {
					wantOperation = "checkpoint-read"
				}
				if workspace != ws || page != "human-doc-page" || bearer != "Bearer original-human-credential" || operation != wantOperation {
					t.Fatalf("history changed trusted request identity: %s %s", page, operation)
				}
				input, _ := json.Marshal(body)
				wantInput := `{}`
				if tc.checkpoint != "" {
					wantInput = `{"checkpoint":"pin-one"}`
				}
				if string(input) != wantInput {
					t.Fatalf("history request=%s", input)
				}
				return []byte(tc.response), tc.upstream, nil
			}
			r := humanDocumentRequest(t, "GET", "/history", ws, user, "")
			w := httptest.NewRecorder()
			if tc.checkpoint == "" {
				h.ListDocumentCheckpoints(w, r)
			} else {
				r.SetPathValue("checkpoint", tc.checkpoint)
				h.ReadDocumentCheckpoint(w, r)
			}
			if calls != 1 || w.Code != tc.want || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("history status=%d calls=%d body=%s", w.Code, calls, w.Body)
			}
			if strings.Contains(w.Body.String(), "ENGINE_PRIVATE") {
				t.Fatal("history disclosed private engine fields")
			}
			if tc.checkpoint != "" && tc.want == 200 {
				var value map[string]any
				if json.Unmarshal(w.Body.Bytes(), &value) != nil || value["page_id"] != "human-doc-page" || len(value) != 5 {
					t.Fatalf("checkpoint projection=%s", w.Body)
				}
			}
		})
	}
}

func TestHumanDocumentHistoryRejectsUnboundedIdentityBeforeEngine(t *testing.T) {
	for _, id := range []string{"", "../other", strings.Repeat("x", 129)} {
		h, ws, user := humanDocumentTransportFixture(t)
		h.documentCall = func(context.Context, string, string, string, string, any) ([]byte, int, error) {
			t.Fatal("invalid identity reached engine")
			return nil, 500, nil
		}
		r := humanDocumentRequest(t, "GET", "/history", ws, user, "")
		r.SetPathValue("checkpoint", id)
		w := httptest.NewRecorder()
		h.ReadDocumentCheckpoint(w, r)
		if w.Code != 400 {
			t.Fatalf("invalid checkpoint status=%d", w.Code)
		}
	}
}
