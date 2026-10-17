package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/documents"
)

type documentRichGolden struct {
	Name                              string `json:"name"`
	Before, After                     json.RawMessage
	BeforeProjection, AfterProjection json.RawMessage
	Semantic                          documentSemanticPatch
}

func documentRichGoldens(t *testing.T) []documentRichGolden {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/docs-rich-semantic-v3.json")
	if err != nil {
		t.Fatal(err)
	}
	var values []documentRichGolden
	if err = json.Unmarshal(raw, &values); err != nil {
		t.Fatal(err)
	}
	if len(values) != 5 {
		t.Fatal("missing Node canonical/actual Yjs cases")
	}
	return values
}
func richTestProjection(t *testing.T, root map[string]any) json.RawMessage {
	t.Helper()
	var blocks []DocumentBlockHash
	var visit func(map[string]any)
	visit = func(node map[string]any) {
		if attrs, ok := node["attrs"].(map[string]any); ok {
			blocks = append(blocks, DocumentBlockHash{ID: attrs["id"].(string), Hash: documentRichHash(node)})
		}
		children, _ := node["content"].([]any)
		for _, child := range children {
			visit(child.(map[string]any))
		}
	}
	visit(root)
	raw, err := json.Marshal(blocks)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestDocumentRichSemanticNodeCanonicalAndEdits(t *testing.T) {
	for _, g := range documentRichGoldens(t) {
		t.Run(g.Name, func(t *testing.T) {
			before, err := documentRichTree(g.Before)
			if err != nil {
				t.Fatal(err)
			}
			after, err := documentRichTree(g.After)
			if err != nil {
				t.Fatal(err)
			}
			if !documentRichProjectionMatches(before, g.BeforeProjection) || !documentRichProjectionMatches(after, g.AfterProjection) {
				t.Fatal("Go disagrees with real Node full-subtree canonical hashes")
			}
			semantic := g.Semantic
			semantic.Rich = &documentRichProof{Before: g.Before, After: g.After}
			hashes := map[string]string{"target": documentRichHash(documentRichFind(before, "target"))}
			calls := 0
			next := func(context.Context, *sql.Tx, string, documents.State) error { calls++; return nil }
			err = documentRichSemanticGuard(&semantic, hashes, g.AfterProjection, next)(t.Context(), nil, "actor", documents.State{Schema: 3, Projection: g.BeforeProjection})
			if err != nil || calls != 1 {
				t.Fatalf("actual Node edit rejected or scope bypassed: %v/%d", err, calls)
			}
		})
	}
}
func TestDocumentRichSemanticRejectsNontextAndForgedTree(t *testing.T) {
	goldens := documentRichGoldens(t)
	for _, scenario := range []string{"ancestor attributes", "other code", "outside marks", "insert inherits link", "root reorder", "root attributes", "un-ID node", "forged before", "missing ancestor", "missing proof", "proof too large", "split surrogate"} {
		t.Run(scenario, func(t *testing.T) {
			g := goldens[0]
			if scenario == "split surrogate" {
				g = goldens[3]
			}
			before, _ := documentRichTree(g.Before)
			after, _ := documentRichTree(g.After)
			semantic := g.Semantic
			projection := g.AfterProjection
			switch scenario {
			case "ancestor attributes":
				documentRichFind(after, "ancestor")["attrs"].(map[string]any)["start"] = json.Number("3")
			case "other code":
				documentRichFind(after, "code")["content"].([]any)[0].(map[string]any)["text"] = "foreign change"
			case "outside marks":
				documentRichFind(after, "target")["content"].([]any)[0].(map[string]any)["marks"] = []any{map[string]any{"type": "code"}}
			case "insert inherits link":
				documentRichFind(after, "target")["content"].([]any)[1].(map[string]any)["marks"] = []any{map[string]any{"type": "link", "attrs": map[string]any{"href": "https://example.invalid/"}}}
			case "root reorder":
				children := after["content"].([]any)
				children[0], children[1] = children[1], children[0]
			case "root attributes":
				after["attrs"] = map[string]any{"id": "unhashed-root"}
			case "un-ID node":
				delete(documentRichFind(after, "ancestor"), "attrs")
			case "forged before":
				documentRichFind(before, "ancestor")["attrs"].(map[string]any)["start"] = json.Number("3")
			case "missing ancestor":
				var blocks []DocumentBlockHash
				json.Unmarshal(g.BeforeProjection, &blocks)
				gcopy := append([]DocumentBlockHash(nil), blocks[1:]...)
				raw, _ := json.Marshal(gcopy)
				g.BeforeProjection = raw
			case "split surrogate":
				semantic.Edit = &documentSemanticEdit{From: 2, To: 2, Insert: "bad"}
			}
			if scenario != "un-ID node" {
				projection = richTestProjection(t, after)
			}
			rawBefore, _ := json.Marshal(before)
			rawAfter, _ := json.Marshal(after)
			semantic.Rich = &documentRichProof{Before: rawBefore, After: rawAfter}
			if scenario == "missing proof" {
				semantic.Rich = nil
			}
			if scenario == "proof too large" {
				semantic.Rich.Before = json.RawMessage(strings.Repeat(" ", documentRichProofLimit+1))
			}
			hashes := map[string]string{"target": documentRichHash(documentRichFind(before, "target"))}
			calls := 0
			next := func(context.Context, *sql.Tx, string, documents.State) error { calls++; return nil }
			if err := documentRichSemanticGuard(&semantic, hashes, projection, next)(t.Context(), nil, "actor", documents.State{Schema: 3, Projection: g.BeforeProjection}); err == nil || calls != 0 {
				t.Fatalf("forbidden proof reached scope or accepted: %v/%d", err, calls)
			}
		})
	}
}
func TestDocumentRichSemanticDeniedBeforeDurableReceipt(t *testing.T) {
	rig := pagesTransferSetup(t)
	const page = "rich-semantic-guard"
	rig.seedPage(t, page, page, "Document", rig.adminID, "")
	h := NewPageHandler(rig.db, nil, newTestLogger())
	workspace, user := rig.wsID, rig.adminID
	g := documentRichGoldens(t)[0]
	store := documents.New(h.db, documents.Options{})
	key := documents.Key{WorkspaceID: workspace, PageID: page}
	auth := func(context.Context, *sql.Tx) (string, error) { return "user:" + user, nil }
	if _, err := store.Init(t.Context(), key, 3, []byte{1}, g.BeforeProjection, auth); err != nil {
		t.Fatal(err)
	}
	lease, err := store.Acquire(t.Context(), key, "test", auth)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := documentRichTree(g.Before)
	after, _ := documentRichTree(g.After)
	documentRichFind(after, "ancestor")["attrs"].(map[string]any)["start"] = json.Number("3")
	rawAfter, _ := json.Marshal(after)
	projection := richTestProjection(t, after)
	semantic := g.Semantic
	semantic.Rich = &documentRichProof{Before: g.Before, After: rawAfter}
	hashes := map[string]string{"target": documentRichHash(documentRichFind(before, "target"))}
	commit := documents.Commit{Lease: lease, ID: "forbidden-rich", Fingerprint: "candidate", Schema: 3, Kind: "agent", Update: []byte{1}, CandidateSnapshot: []byte{1}, Projection: projection, IfHashes: hashes, Guard: documentRichSemanticGuard(&semantic, hashes, projection, nil)}
	if _, err = store.Commit(t.Context(), key, commit, auth); err == nil {
		t.Fatal("nontext mutation durable")
	}
	var rev, receipts int
	if err = h.db.QueryRow(`SELECT head_rev FROM documents WHERE page_id=?`, page).Scan(&rev); err != nil {
		t.Fatal(err)
	}
	if err = h.db.QueryRow(`SELECT COUNT(*) FROM document_receipts WHERE page_id=? AND id='forbidden-rich'`, page).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if rev != 0 || receipts != 0 {
		t.Fatalf("denial advanced revision/receipt: %d/%d", rev, receipts)
	}
}
