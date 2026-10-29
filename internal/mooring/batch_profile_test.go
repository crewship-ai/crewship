package mooring

import (
	"context"
	"database/sql"
	"encoding/base64"
	"strings"
	"testing"
)

func TestLegacyCollaborationAdaptersRefuseCommandProfile(t *testing.T) {
	owners, db, _, key := ownerFixture(t)
	key.ApplicationID = "wordcraft"
	lease, err := owners.Acquire(t.Context(), key, "command-runtime")
	if err != nil {
		t.Fatal(err)
	}
	bytes := []byte(`{"format":"wordcraft-c1-v2","fixture":"must not be decoded by legacy"}`)
	encoded := base64.StdEncoding.EncodeToString(bytes)
	_, err = db.Exec(`INSERT INTO mooring_collab_documents(page_id,workspace_id,application_id,revision,checkpoint_base64,format_profile,engine_revision,engine_sha256) VALUES('p','w','wordcraft','0',?,'wordcraft-c1-v2','7ef2633a7e84956522d1ca47b99fe6feef16c1db',?)`, encoded, "sha256:"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	adapter := &batchTestAdapter{}
	store := NewBatchStore(db.DB, BatchStoreConfig{Enabled: true, Owners: owners, Authority: func(context.Context, *sql.Tx, BatchAccess) error { return nil }, Adapter: adapter, Limits: DefaultBatchLimits()})
	if _, _, err = store.ReadCheckpoint(t.Context(), key); err == nil {
		t.Fatal("legacy read command checkpoint")
	}
	if _, err = store.Status(t.Context(), key, "operation", "sha256:"+strings.Repeat("b", 64)); err == nil {
		t.Fatal("legacy status command encoding")
	}
	if err = store.RegisterTask(t.Context(), lease, "task", "participant", false); err == nil {
		t.Fatal("legacy registered command task")
	}
	if err = store.Initialize(t.Context(), lease, []byte("0")); err == nil {
		t.Fatal("legacy initialized command document")
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = batchScopeMatches(t.Context(), tx, key); err == nil {
		t.Fatal("legacy direct checkpoint decode")
	}
	tx.Rollback()
	var after string
	if err = db.QueryRow(`SELECT checkpoint_base64 FROM mooring_collab_documents WHERE page_id='p'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != encoded || adapter.calls.Load() != 0 {
		t.Fatal("legacy touched new profile")
	}
	if _, err = db.Exec(`UPDATE mooring_collab_documents SET format_profile='mooring-causal-v1' WHERE page_id='p'`); err == nil {
		t.Fatal("profile converted in-place")
	}
}

func TestLegacyCollaborationProfileDefaultPreservesPayload(t *testing.T) {
	s, _, _, _, lease := batchFixture(t)
	var profile, revision, engine, digest string
	if err := s.db.QueryRow(`SELECT format_profile,revision,engine_revision,engine_sha256 FROM mooring_collab_documents WHERE page_id=?`, lease.Key.ArtifactID).Scan(&profile, &revision, &engine, &digest); err != nil {
		t.Fatal(err)
	}
	if profile != LegacyCollaborationProfile || revision != "0" || engine != "" || digest != "" {
		t.Fatal("legacy profile changed", profile, revision)
	}
	_, raw, err := s.ReadCheckpoint(t.Context(), lease.Key)
	if err != nil || string(raw) != "0" {
		t.Fatal("legacy content did not survive", err)
	}
}
