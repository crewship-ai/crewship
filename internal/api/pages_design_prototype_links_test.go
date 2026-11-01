package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/crewship-ai/crewship/internal/design"
)

// Prototype links (#4449, #4737) are an optional linkTo screen ID on a layer's
// Y.Map. These tests cover the REST roundtrips the independent review asked
// for: a saved link, including one whose target screen was deleted, must be
// readable again by a fresh reader and by a retained version, and restore must
// bring a version's links back exactly (present when it had them, absent when
// it did not).

const designLinkScreens = `{"operations":[
	{"op":"upsert","node":{"id":"home","type":"frame","name":"Home","x":0,"y":0,"width":360,"height":640}},
	{"op":"upsert","node":{"id":"detail","type":"frame","name":"Detail","x":400,"y":0,"width":360,"height":640}},
	{"op":"upsert","node":{"id":"gone","type":"frame","name":"Gone","x":800,"y":0,"width":360,"height":640}},
	{"op":"upsert","node":{"id":"to-detail","type":"rect","parentId":"home","x":24,"y":24,"width":120,"height":48}},
	{"op":"upsert","node":{"id":"to-gone","type":"rect","parentId":"home","x":24,"y":96,"width":120,"height":48}}
],"message":"screens without links"}`

const designLinkSet = `{"operations":[
	{"op":"upsert","node":{"id":"to-detail","linkTo":"detail"}},
	{"op":"upsert","node":{"id":"to-gone","linkTo":"gone"}},
	{"op":"delete","id":"gone"}
],"message":"prototype links"}`

// designLinkMap decodes nodes into id -> node fields.
func designLinkMap(t *testing.T, nodes []json.RawMessage) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, raw := range nodes {
		var node map[string]any
		if err := json.Unmarshal(raw, &node); err != nil {
			t.Fatalf("decode node %s: %v", raw, err)
		}
		id, _ := node["id"].(string)
		out[id] = node
	}
	return out
}

// designGetWith reads the canvas through a given handler, optionally at a
// retained version. It reuses the token designFixture minted for the user.
func designGetWith(t *testing.T, h *PageHandler, ws, user string, version int64) designSnapshotResponse {
	t.Helper()
	target := "/api/v1/pages/board/design"
	if version > 0 {
		target += "?version=" + strconv.FormatInt(version, 10)
	}
	r := httptest.NewRequest("GET", target, nil)
	r.Header.Set("Authorization", "Bearer crewship_cli_design-test-"+user)
	ctx := withWorkspace(withUser(r.Context(), &AuthUser{ID: user}), ws, "MEMBER")
	ctx = context.WithValue(ctx, ctxAuthKind, AuthKindCLIToken)
	r = r.WithContext(ctx)
	r.SetPathValue("slug", "board")
	w := httptest.NewRecorder()
	h.GetDesign(w, r)
	if w.Code != 200 {
		t.Fatalf("get version %d: %d %s", version, w.Code, w.Body)
	}
	var value designSnapshotResponse
	if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode get: %v %s", err, w.Body)
	}
	return value
}

func designReceiptVersion(t *testing.T, w *httptest.ResponseRecorder) int64 {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("mutation %d %s", w.Code, w.Body)
	}
	var receipt struct {
		Version int64 `json:"version"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil || receipt.Version < 1 {
		t.Fatalf("receipt %s (%v)", w.Body, err)
	}
	return receipt.Version
}

func assertDesignLinks(t *testing.T, label string, nodes []json.RawMessage, want map[string]string) {
	t.Helper()
	got := designLinkMap(t, nodes)
	if _, exists := got["gone"]; exists {
		t.Errorf("%s: deleted screen came back: %s", label, nodes)
	}
	for _, id := range []string{"home", "detail", "to-detail", "to-gone"} {
		node, ok := got[id]
		if !ok {
			t.Errorf("%s: node %s missing: %s", label, id, nodes)
			continue
		}
		link, has := node["linkTo"]
		if expected, linked := want[id]; linked {
			if link != expected {
				t.Errorf("%s: %s linkTo = %v, want %q", label, id, link, expected)
			}
		} else if has {
			t.Errorf("%s: %s carries linkTo %v, want no field", label, id, link)
		}
	}
}

func TestDesignPrototypeLinksSurviveSaveAndFreshRead(t *testing.T) {
	h, ws, owner, call := designFixture(t)
	if w := call(owner, "create", `{"slug":"board","name":"Prototype"}`); w.Code != 201 {
		t.Fatalf("create %d %s", w.Code, w.Body)
	}
	designReceiptVersion(t, call(owner, "apply", designLinkScreens))
	linked := designReceiptVersion(t, call(owner, "apply", designLinkSet))
	want := map[string]string{"to-detail": "detail", "to-gone": "gone"}

	// Same handler, live canvas.
	w := call(owner, "get", "")
	if w.Code != 200 {
		t.Fatalf("get %d %s", w.Code, w.Body)
	}
	var live designSnapshotResponse
	if err := json.Unmarshal(w.Body.Bytes(), &live); err != nil {
		t.Fatal(err)
	}
	assertDesignLinks(t, "live read", live.Nodes, want)

	// A new handler has no in-memory room: it can only read persisted state.
	fresh := NewPageHandler(h.db, nil, newTestLogger())
	assertDesignLinks(t, "fresh handler live read", designGetWith(t, fresh, ws, owner, 0).Nodes, want)
	assertDesignLinks(t, "fresh handler retained version", designGetWith(t, fresh, ws, owner, linked).Nodes, want)

	// The persisted Yjs bytes decode to the same links without the API.
	var state []byte
	if err := h.db.QueryRow(`SELECT state FROM artifact_design_documents`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	reopened, err := design.Run(t.Context(), design.Request{State: state})
	if err != nil {
		t.Fatal(err)
	}
	assertDesignLinks(t, "persisted state", reopened.Nodes, want)
}

func TestDesignRestoreReturnsExactPrototypeLinksOfVersion(t *testing.T) {
	h, ws, owner, call := designFixture(t)
	if w := call(owner, "create", `{"slug":"board","name":"Prototype"}`); w.Code != 201 {
		t.Fatalf("create %d %s", w.Code, w.Body)
	}
	unlinked := designReceiptVersion(t, call(owner, "apply", designLinkScreens))
	linked := designReceiptVersion(t, call(owner, "apply", designLinkSet))
	cleared := designReceiptVersion(t, call(owner, "apply", `{"operations":[{"op":"upsert","node":{"id":"to-detail","linkTo":""}}],"message":"clear one link"}`))
	if !(unlinked < linked && linked < cleared) {
		t.Fatalf("unexpected version order %d %d %d", unlinked, linked, cleared)
	}
	fresh := NewPageHandler(h.db, nil, newTestLogger())
	assertDesignLinks(t, "cleared head", designGetWith(t, fresh, ws, owner, 0).Nodes, map[string]string{"to-detail": "", "to-gone": "gone"})

	restoredLinked := designReceiptVersion(t, call(owner, "restore", `{"version":`+strconv.FormatInt(linked, 10)+`}`))
	want := map[string]string{"to-detail": "detail", "to-gone": "gone"}
	assertDesignLinks(t, "restore of linked version", designGetWith(t, fresh, ws, owner, 0).Nodes, want)
	assertDesignLinks(t, "retained restore version", designGetWith(t, fresh, ws, owner, restoredLinked).Nodes, want)

	restoredUnlinked := designReceiptVersion(t, call(owner, "restore", `{"version":`+strconv.FormatInt(unlinked, 10)+`}`))
	// The unlinked version still had the "gone" screen; restore brings it back.
	value := designGetWith(t, fresh, ws, owner, 0)
	nodes := designLinkMap(t, value.Nodes)
	if _, ok := nodes["gone"]; !ok {
		t.Fatalf("restore of unlinked version lost its screen: %s", value.Nodes)
	}
	for id, node := range nodes {
		if link, has := node["linkTo"]; has {
			t.Errorf("restore of unlinked version kept %s linkTo %v", id, link)
		}
	}
	retained := designLinkMap(t, designGetWith(t, fresh, ws, owner, restoredUnlinked).Nodes)
	for id, node := range retained {
		if link, has := node["linkTo"]; has {
			t.Errorf("retained unlinked restore kept %s linkTo %v", id, link)
		}
	}
	// History still holds the linked version untouched.
	assertDesignLinks(t, "history after restores", designGetWith(t, fresh, ws, owner, linked).Nodes, want)
}
