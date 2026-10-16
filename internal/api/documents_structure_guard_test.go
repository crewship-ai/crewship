package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/documents"
)

func structuralFixture(t *testing.T) (documentVerifiedTarget, *documentSemanticPatch, map[string]string, documents.State, json.RawMessage) {
	t.Helper()
	raw := json.RawMessage(`{"type":"doc","content":[{"type":"paragraph","attrs":{"id":"left"},"content":[{"type":"text","text":"outside"}]},{"type":"paragraph","attrs":{"id":"a"},"content":[{"type":"text","text":"A"}]},{"type":"paragraph","attrs":{"id":"b"},"content":[{"type":"text","text":"B"}]},{"type":"paragraph","attrs":{"id":"right"},"content":[{"type":"text","text":"outside"}]}]}`)
	before, err := documentRichTree(raw)
	if err != nil {
		t.Fatal(err)
	}
	pair := func(id string) DocumentBlockHash {
		return DocumentBlockHash{ID: id, Hash: documentRichHash(documentRichFind(before, id))}
	}
	left, right := pair("left"), pair("right")
	scope := &documentStructureScope{Version: 1, Blocks: []DocumentBlockHash{pair("a"), pair("b")}, Before: &left, After: &right, AllowInsertAfter: true}
	target := documentVerifiedTarget{DocumentSelectedTarget: DocumentSelectedTarget{ID: "a", Hash: scope.Blocks[0].Hash}, Schema: 3, Structure: scope}
	after, _ := documentRichTree(raw)
	nodes := after["content"].([]any)
	after["content"] = []any{nodes[0], nodes[2], nodes[3]}
	afterRaw, _ := json.Marshal(after)
	semantic := &documentSemanticPatch{Structure: &documentStructuralProof{RequestID: "request", Before: raw, After: afterRaw, Operations: []documentStructureOperationProof{{Op: "delete", ID: "a", IfHash: scope.Blocks[0].Hash}}}}
	return target, semantic, map[string]string{"a": scope.Blocks[0].Hash, "b": scope.Blocks[1].Hash}, documents.State{Schema: 3, Projection: richTestProjection(t, before)}, richTestProjection(t, after)
}

func TestDocumentStructuralProofAuthenticatesWholeTreeAndScope(t *testing.T) {
	for _, scenario := range []string{"valid delete", "wrong request", "outside delete", "changed outside text", "forged before ancestor", "missing block hash", "old text scope", "schema2", "extra mixed text semantic", "wrong boundary", "reused ID insertion"} {
		t.Run(scenario, func(t *testing.T) {
			target, semantic, hashes, state, projection := structuralFixture(t)
			switch scenario {
			case "wrong request":
				semantic.Structure.RequestID = "other"
			case "outside delete":
				semantic.Structure.Operations[0].ID = "left"
			case "changed outside text":
				after, _ := documentRichTree(semantic.Structure.After)
				documentRichFind(after, "right")["content"].([]any)[0].(map[string]any)["text"] = "foreign"
				semantic.Structure.After, _ = json.Marshal(after)
				projection = richTestProjection(t, after)
			case "forged before ancestor":
				before, _ := documentRichTree(semantic.Structure.Before)
				documentRichFind(before, "left")["content"].([]any)[0].(map[string]any)["text"] = "forged"
				semantic.Structure.Before, _ = json.Marshal(before)
			case "missing block hash":
				delete(hashes, "b")
			case "old text scope":
				target.Structure = nil
			case "schema2":
				state.Schema = 2
			case "extra mixed text semantic":
				semantic.Target = "a"
			case "wrong boundary":
				target.Structure.After = &DocumentBlockHash{ID: "left", Hash: target.Structure.Before.Hash}
			case "reused ID insertion":
				semantic.Structure.Operations = []documentStructureOperationProof{{Op: "insert_after", AfterID: "b", AnchorHash: hashes["b"], Node: json.RawMessage(`{"type":"paragraph","attrs":{"id":"a"}}`)}}
			}
			err := documentStructuralGuard(t.Context(), nil, "workspace", "page", "request", target, semantic, hashes, projection, state)
			if scenario == "valid delete" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, documents.ErrConflict) {
				t.Fatalf("escape accepted: %v", err)
			}
		})
	}
}

func TestDocumentStructuralCommentedMoveFailsWithoutChangingThread(t *testing.T) {
	h, _, _, workspace, _, page := artifactFixture(t)
	target, semantic, hashes, state, _ := structuralFixture(t)
	store := documents.New(h.db, documents.Options{})
	allow := func(_ context.Context, _ *sql.Tx) (string, error) { return "synthetic-actor", nil }
	if _, err := store.Init(t.Context(), documents.Key{WorkspaceID: workspace, PageID: page}, 3, []byte("opaque-guard-fixture"), state.Projection, allow); err != nil {
		t.Fatal(err)
	}
	anchor := `{"id":"comment","block":"b","quote":"B","checkpoint":0,"start":"original-start","end":"original-end"}`
	execOrFatal(t, h.db, `INSERT INTO document_comments(workspace_id,page_id,id,body,anchor_json,actor,rev) VALUES(?,?,'comment','retained body',?,'synthetic-actor',0)`, workspace, page, anchor)
	before, _ := documentRichTree(semantic.Structure.Before)
	nodes := before["content"].([]any)
	before["content"] = []any{nodes[0], nodes[2], nodes[1], nodes[3]}
	semantic.Structure.After, _ = json.Marshal(before)
	semantic.Structure.Operations = []documentStructureOperationProof{{Op: "move_before", ID: "b", BeforeID: "a", IfHash: hashes["b"], AnchorHash: hashes["a"]}}
	tx, err := h.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	err = documentStructuralGuard(t.Context(), tx, workspace, page, "request", target, semantic, hashes, richTestProjection(t, before), state)
	tx.Rollback()
	if !errors.Is(err, documents.ErrConflict) {
		t.Fatalf("commented move accepted: %v", err)
	}
	var actual string
	if err = h.db.QueryRow(`SELECT anchor_json FROM document_comments WHERE id='comment'`).Scan(&actual); err != nil || actual != anchor {
		t.Fatalf("thread changed: %q %v", actual, err)
	}
}
