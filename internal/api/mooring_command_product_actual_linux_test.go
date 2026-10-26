//go:build linux

package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
)

// This is an actual native/SQL lifecycle test when BOTH explicit pinned
// artifacts are supplied. It is not browser, peer delivery or chat evidence.
func TestCommandProductActualNativeCreateCommitReopen(t *testing.T) {
	config := actualCommandNativeConfig(t, "factory-artifact-qualification")
	f, manager, request := commandProductTestManager(t)
	mustExec(t, f.r.db, `DELETE FROM mooring_collab_documents WHERE page_id='admission-page'`)
	var ports []*actualCommandNativeObservedPort
	start := func(ctx context.Context, runtime string) (commandProductPort, error) {
		c := config
		c.RuntimeRef = runtime
		port, err := startCommandNativePort(ctx, c)
		if err != nil {
			return nil, err
		}
		observed := &actualCommandNativeObservedPort{commandNativePort: port}
		ports = append(ports, observed)
		return observed, nil
	}
	factory, err := newCommandProductFactory(true, manager, start)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(factory.Close)
	document, err := factory.Open(request, true)
	if err != nil {
		t.Fatal("actual factory creation", err)
	}
	projection, err := ports[0].Projection(request.Context())
	if err != nil {
		t.Fatal(err)
	}
	admission, err := document.store.Admit(request.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	actor := commandEngineActorBinding{transportParticipant: admission.participant, sourceUser: admission.source.UserID, actor: "human:" + admission.source.UserID}
	raw, _ := json.Marshal(map[string]any{"op_id": projection.Generation + ":factory-proof", "generation": projection.Generation, "participant": actor.actor, "base_revision": 0, "target": map[string]any{"block_id": projection.IDs[0], "unit": "utf8_bytes", "start": 0, "end": 0}, "command": "text.insert", "params": map[string]any{"text": "Factory proof"}})
	outcome, err := document.store.Submit(request, admission, actor, raw)
	if err != nil || outcome.Revision != 1 {
		t.Fatal("actual factory SQL commit", err)
	}
	confirmed, err := ports[0].Projection(request.Context())
	if err != nil || actualNativeText(t, confirmed) != "Factory proof" {
		t.Fatal("native P not confirmed", err)
	}
	var delivered commandProductProjection
	if err = document.DeliverProjection(request, admission, func(_ context.Context, raw []byte) error {
		return json.Unmarshal(raw, &delivered)
	}); err != nil || delivered.Revision != outcome.Revision || actualNativeText(t, delivered) != "Factory proof" {
		t.Fatal("actual SQL-authorized projection", err)
	}
	var before string
	if err = f.r.db.QueryRow(`SELECT checkpoint_base64 FROM mooring_collab_documents WHERE page_id='admission-page'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := base64.StdEncoding.DecodeString(before)
	if err != nil {
		t.Fatal(err)
	}
	factory.Close()
	reopened, err := newCommandProductFactory(true, manager, start)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	if _, err = reopened.Open(request, false); err != nil {
		t.Fatal("actual factory canonical reopen", err)
	}
	restored, err := ports[1].Projection(request.Context())
	if err != nil || actualNativeText(t, restored) != "Factory proof" || restored.Revision != 1 || restored.Generation == projection.Generation || restored.IDs[0] != projection.IDs[0] {
		t.Fatal("reopen changed content or block identity", err)
	}
	var after string
	if err = f.r.db.QueryRow(`SELECT checkpoint_base64 FROM mooring_collab_documents WHERE page_id='admission-page'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(after)
	if err != nil || !bytes.Equal(checkpoint, decoded) || ports[0].stages != 1 || ports[1].stages != 0 {
		t.Fatal("reopen rewrote SQL or replayed command", err)
	}
}
