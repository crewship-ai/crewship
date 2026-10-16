package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/ws"
)

func TestDocumentRequestFreezesCanonicalContentSchema(t *testing.T) {
	for _, scenario := range []struct {
		name                             string
		stored, verified, target, status int
	}{
		{"legacy1", 1, 0, 0, 202}, {"legacy2", 2, 0, 0, 202}, {"rich3", 3, 3, 3, 202},
		{"missing rich capability", 3, 0, 0, 409}, {"false upgrade", 2, 3, 3, 409}, {"foreign target capability", 3, 3, 0, 409}, {"future", 3, 4, 4, 409},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			h, workspace, user, page := documentRequestFixture(t)
			execOrFatal(t, h.db, `UPDATE documents SET schema_version=? WHERE page_id=?`, scenario.stored, page)
			original := h.documentCall
			h.documentCall = func(ctx context.Context, w, b, p, op string, input any) ([]byte, int, error) {
				raw, status, err := original(ctx, w, b, p, op, input)
				var value map[string]any
				json.Unmarshal(raw, &value)
				if scenario.verified != 0 {
					value["schema"] = scenario.verified
				}
				if scenario.target != 0 {
					value["targets"].([]any)[0].(map[string]any)["schema"] = scenario.target
				}
				raw, _ = json.Marshal(value)
				return raw, status, err
			}
			id := createDocumentRequestTest(t, h, workspace, user, documentRequestBody, scenario.status)
			if scenario.status != 202 {
				return
			}
			var raw string
			if err := h.db.QueryRow(`SELECT targets_json FROM document_requests WHERE id=?`, id).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var targets []documentVerifiedTarget
			if err := json.Unmarshal([]byte(raw), &targets); err != nil || len(targets) != 1 || targets[0].Schema != scenario.target {
				t.Fatalf("immutable schema lost: %s / %v", raw, err)
			}
			// A later upgrade/restore cannot rewrite the original worker instruction.
			execOrFatal(t, h.db, `UPDATE documents SET schema_version=2,head_rev=head_rev+1 WHERE page_id=?`, page)
			called := false
			h.artifactDispatch = func(_ context.Context, _, _ string, prompt string, _ ws.ChatMessageOption) error {
				called = true
				schema := "schema:2"
				if scenario.target == 3 {
					schema = "schema:3"
				}
				if !strings.Contains(prompt, schema+",page_id:\""+page+"\"") || strings.Contains(prompt, "%!") {
					t.Fatalf("worker refreshed schema or lost original identity: %s", prompt)
				}
				return nil
			}
			h.dispatchNextDocumentRequest(t.Context())
			if !called {
				t.Fatal("worker did not receive immutable schema")
			}
		})
	}
}
