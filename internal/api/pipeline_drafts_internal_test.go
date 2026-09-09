package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

func TestInternalDraftSharesUserCASWithoutPublishing(t *testing.T) {
	h, db, ws, crew := triggerSaveRig(t)
	call := func(handler http.HandlerFunc, in internalDraftRequest, boundCrew string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(in)
		ctx := context.WithValue(context.Background(), ctxInternalTokenWS, ws)
		ctx = context.WithValue(ctx, ctxInternalTokenCrew, boundCrew)
		r := httptest.NewRequest("POST", "/draft", bytes.NewReader(raw)).WithContext(ctx)
		w := httptest.NewRecorder()
		handler(w, r)
		return w
	}
	in := internalDraftRequest{WorkspaceID: ws, Slug: "ai-draft", AuthorCrewID: crew, AuthorAgentID: "a-trigger"}
	w := call(h.InternalGetDraft, in, crew)
	if w.Code != 200 {
		t.Fatalf("baseline %d %s", w.Code, w.Body)
	}
	var out struct {
		Draft     pipeline.Draft `json:"draft"`
		EditorURL string         `json:"editor_url"`
		Published bool           `json:"published"`
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	in.Draft = out.Draft
	in.Draft.Document = json.RawMessage(`{"slug":"ai-draft","name":"AI draft","definition":{"name":"ai-draft","steps":[]},"author_crew_id":"forged","skip_governance_gate":true,"trigger":{"kind":"schedule","cron":"0 9 * * *"}}`)
	w = call(h.InternalSaveDraft, in, crew)
	if w.Code != 200 {
		t.Fatalf("save %d %s", w.Code, w.Body)
	}
	json.Unmarshal(w.Body.Bytes(), &out)
	if out.Published || out.Draft.Revision != 1 || out.EditorURL == "" {
		t.Fatalf("response %+v", out)
	}
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM pipelines WHERE slug='ai-draft'`).Scan(&count)
	if count != 0 {
		t.Fatal("agent draft published a recipe")
	}
	var doc map[string]any
	json.Unmarshal(out.Draft.Document, &doc)
	if doc["author_crew_id"] != crew || doc["author_agent_id"] != "a-trigger" {
		t.Fatal("spoofed identity", doc)
	}
	if _, ok := doc["skip_governance_gate"]; ok {
		t.Fatal("persisted bypass")
	}
	// N6: a replacement document must remove keys omitted by the agent.
	in.Draft = out.Draft
	in.Draft.Document = json.RawMessage(`{"slug":"ai-draft","name":"AI draft","definition":{"name":"ai-draft","steps":[]}}`)
	w = call(h.InternalSaveDraft, in, crew)
	if w.Code != 200 {
		t.Fatalf("replacement %d %s", w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	doc = nil
	if err := json.Unmarshal(out.Draft.Document, &doc); err != nil {
		t.Fatal(err)
	}
	if _, exists := doc["trigger"]; exists {
		t.Fatal("N6: removed trigger survived replacement")
	}
	// N11: ownership is checked at the requested slug; a foreign draft ID
	// must not be usable to move an existing draft through that doorway.
	rename := in
	rename.Slug = "unused-name"
	rename.Draft = out.Draft
	rename.Draft.Slug = rename.Slug
	rename.Draft.Document = json.RawMessage(`{"slug":"unused-name","definition":{"name":"unused-name","steps":[]}}`)
	if w = call(h.InternalSaveDraft, rename, crew); w.Code != 409 {
		t.Fatalf("agent cross-slug rename %d %s", w.Code, w.Body)
	}
	// A human saves the same revision; the agent's later stale save must fail.
	userDraft := out.Draft
	userDraft.UpdatedBy = "reviewer"
	if _, err := h.store.SaveDraft(context.Background(), userDraft); err != nil {
		t.Fatal(err)
	}
	in.Draft = out.Draft
	if w = call(h.InternalSaveDraft, in, crew); w.Code != 409 {
		t.Fatalf("stale agent %d %s", w.Code, w.Body)
	}
	sibling := seedCrewRow(t, db, "draft-sibling", ws, "Sibling", "draft-sibling")
	other := in
	other.AuthorCrewID = sibling
	other.AuthorAgentID = ""
	if w = call(h.InternalGetDraft, other, sibling); w.Code != 403 {
		t.Fatalf("sibling read %d %s", w.Code, w.Body)
	}
	other = in
	other.WorkspaceID = "foreign"
	if w = call(h.InternalGetDraft, other, crew); w.Code != 403 {
		t.Fatalf("foreign workspace %d", w.Code)
	}
	other = in
	other.AuthorCrewID = sibling
	if w = call(h.InternalSaveDraft, other, crew); w.Code != 403 {
		t.Fatalf("forged crew %d", w.Code)
	}
}
