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

func TestNativeGridCraftHistoryOnlyCoexistsWithItsOwnEngine(t *testing.T) {
	for _, tc := range []struct {
		name, contentType string
		ownHead, allowed  bool
	}{
		{"own-native-history", "gridcraft", true, true},
		{"orphan-native-history", "gridcraft", false, false},
		{"foreign-project-history", "artifact_project", true, false},
		{"foreign-wordcraft-history", "wordcraft", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db, a, k := fixture(t)
			if tc.ownHead {
				imported(t, s, a, k, &fakeEngine{})
			}
			if _, err := db.Exec(`INSERT INTO artifact_version_heads(workspace_id,page_id,latest_version,content_type) VALUES('ws','page',1,?)`, tc.contentType); err != nil {
				t.Fatal(err)
			}
			_, err := s.Read(t.Context(), k, a)
			if tc.allowed && err != nil {
				t.Fatalf("own native history blocked: %v", err)
			}
			if !tc.allowed && !errors.Is(err, ErrForbidden) {
				t.Fatalf("foreign/orphan history admitted: %v", err)
			}
		})
	}
}
