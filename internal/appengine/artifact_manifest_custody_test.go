package appengine

import (
	"errors"
	"testing"
)

func TestArtifactManifestPreventsWorkbookContentTakeover(t *testing.T) {
	s, db, a, k := fixture(t)
	if _, err := db.Exec(`INSERT INTO artifact_version_heads(workspace_id,page_id,latest_version) VALUES('ws','page',1)`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	empty, err := PageHasNoContent(t.Context(), tx, k.PageID)
	_ = tx.Rollback()
	if err != nil || empty {
		t.Fatalf("manifest project treated as empty: empty=%t err=%v", empty, err)
	}
	engine := &fakeEngine{}
	_, err = s.Execute(t.Context(), k, Request{MutationID: "takeover", MandateID: "human-import", Command: Command{Kind: "import", Snapshot: []byte(`{"sheets":[{}]}`)}}, a, engine)
	if !errors.Is(err, ErrForbidden) || engine.calls.Load() != 0 {
		t.Fatalf("manifest custody bypassed: %v, engine calls %d", err, engine.calls.Load())
	}
}
