package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestDocumentStructuralAdmissionFreezesExplicitScopeAndRejectsForgedRoot(t *testing.T) {
	for _, scenario := range []string{"valid", "text scope cannot promote", "mixed selection", "forged boundary", "forged root order", "older content"} {
		t.Run(scenario, func(t *testing.T) {
			h, workspace, user, page := documentRequestFixture(t)
			target, semantic, _, state, _ := structuralFixture(t)
			execOrFatal(t, h.db, `UPDATE documents SET schema_version=3,projection=? WHERE page_id=?`, []byte(state.Projection), page)
			body := `{"page_id":"PAGE_ID","structure":{"block_ids":["a","b"],"allow_insert_after":true},"chat_id":"artifact-chat","instruction":"Reorganize these blocks","base_rev":0,"submission_id":"structure-first"}`
			wanted := 202
			if scenario == "mixed selection" {
				body = strings.Replace(body, `"structure":`, `"targets":[{"id":"a","hash":"fake","from":0,"to":1,"quote":"A"}],"structure":`, 1)
				wanted = 400
			}
			if scenario == "text scope cannot promote" {
				target.Structure = nil
				wanted = 409
			}
			if scenario == "forged boundary" {
				target.Structure.After = target.Structure.Before
				wanted = 409
			}
			if scenario == "forged root order" {
				tree, _ := documentRichTree(semantic.Structure.Before)
				nodes := tree["content"].([]any)
				nodes[0], nodes[3] = nodes[3], nodes[0]
				semantic.Structure.Before, _ = json.Marshal(tree)
				wanted = 409
			}
			if scenario == "older content" {
				execOrFatal(t, h.db, `UPDATE documents SET schema_version=2 WHERE page_id=?`, page)
				wanted = 409
			}
			h.documentCall = func(_ context.Context, _, _, actualPage, operation string, _ any) ([]byte, int, error) {
				if operation != "verify-structure" {
					t.Fatalf("wrong verifier %s", operation)
				}
				raw, _ := json.Marshal(map[string]any{"page_id": actualPage, "schema": 3, "rev": 0, "targets": []documentVerifiedTarget{target}, "tree": json.RawMessage(semantic.Structure.Before)})
				return raw, 200, nil
			}
			id := createDocumentRequestTest(t, h, workspace, user, body, wanted)
			if wanted == 202 {
				var raw string
				if err := h.db.QueryRow(`SELECT targets_json FROM document_requests WHERE id=?`, id).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var targets []documentVerifiedTarget
				if json.Unmarshal([]byte(raw), &targets) != nil || len(targets) != 1 || !validDocumentStructureTarget(targets[0]) {
					t.Fatalf("immutable structural scope missing: %s", raw)
				}
			}
		})
	}
}
