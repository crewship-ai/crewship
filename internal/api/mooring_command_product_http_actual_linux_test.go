//go:build linux && wordcraft_c1_integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The first receipt is deliberately followed by a second real native/SQL
// commit before HTTP delivery/reconciliation. Its old selection must never
// acknowledge the newer projection. No synthetic receipt or peer state.
func TestCommandProductActualHTTPStaleOwnReceipt(t *testing.T) {
	rig := newActualCommandNativeRig(t)
	rig.actor.actor = "human:" + rig.a.source.UserID
	request := httptest.NewRequest("POST", "https://studio.example/command-test", nil).WithContext(context.WithValue(rig.f.ctx, ctxWorkspaceID, rig.f.r.wsID))
	first := rig.input("http-first", 0, "A")
	if _, err := rig.s.Submit(request, rig.a, rig.actor, first); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.s.Submit(request, rig.a, rig.actor, rig.input("http-peer", 1, "B")); err != nil {
		t.Fatal(err)
	}
	manager := &collaborationHTTPManager{pages: rig.s.pages, owners: rig.s.owners, fence: rig.s.humans.fence, clock: rig.s.humans.clock, ctx: t.Context(), config: MooringHTTPConfig{PublicOrigin: "https://studio.example"}}
	factory, err := newCommandProductFactory(true, manager, func(context.Context, string) (commandProductPort, error) {
		t.Fatal("must reuse actual owned runtime")
		return nil, errCommandStore
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(factory.Close)
	document := &commandProductDocument{ready: make(chan struct{}), store: rig.s, port: rig.p, owner: rig.s.owner}
	close(document.ready)
	factory.docs[rig.s.owner.Key] = document
	call := func(body any) *collaborationSessionIOWriter {
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", "https://studio.example/command-test", strings.NewReader(string(raw))).WithContext(request.Context())
		r.SetPathValue("slug", "@admission-page")
		r.Header.Set("Origin", "https://studio.example")
		w := &collaborationSessionIOWriter{ResponseRecorder: httptest.NewRecorder(), fence: manager.fence}
		manager.fence.Wrap(http.HandlerFunc(factory.serve)).ServeHTTP(w, r)
		return w
	}
	opened := call(map[string]any{"action": "open", "write": true})
	var view struct {
		View string `json:"view"`
	}
	if opened.Code != 200 || json.Unmarshal(opened.Body.Bytes(), &view) != nil || view.View == "" {
		t.Fatal("open", opened.Code, opened.Body.String())
	}
	for _, action := range []string{"submit", "outcome"} {
		result := call(map[string]any{"action": action, "view": view.View, "raw": string(first)})
		if result.Code != 409 || strings.Contains(result.Body.String(), "native_json") || strings.Contains(result.Body.String(), "receipt") {
			t.Fatal("old receipt acknowledged newer native P", action, result.Code, result.Body.String())
		}
	}
	unknown := call(map[string]any{"action": "outcome", "view": view.View, "raw": string(rig.input("never-submitted", 2, "C"))})
	var result struct {
		Outcome    *commandSQLOutcome       `json:"outcome"`
		Projection commandProductProjection `json:"projection"`
	}
	if unknown.Code != 200 || json.Unmarshal(unknown.Body.Bytes(), &result) != nil || result.Outcome != nil || result.Projection.Revision != 2 {
		t.Fatal("missing SQL outcome became Saved", unknown.Code, unknown.Body.String())
	}
	if rig.p.stages != 2 {
		t.Fatal("lookup/retry replayed native command", rig.p.stages)
	}
	var revision string
	if err = rig.f.r.db.QueryRow(`SELECT revision FROM mooring_collab_documents WHERE page_id='admission-page'`).Scan(&revision); err != nil || revision != "2" {
		t.Fatal("lookup mutated SQL", revision, err)
	}
}
