package mooring

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

type checkpointTestDescriber struct {
	describe func(context.Context, []byte) (BatchCheckpointDescription, error)
}

func (d *checkpointTestDescriber) DescribeCheckpoint(ctx context.Context, state []byte) (BatchCheckpointDescription, error) {
	return d.describe(ctx, state)
}
func checkpointTestDescription() BatchCheckpointDescription {
	return BatchCheckpointDescription{FormatVersion: "test-format", Versions: protocol.Versions{ProtocolVersion: "0.4.0", ModelVersion: "0.2.0", EngineVersion: "0.10.0"}, Assets: []protocol.Asset{}}
}
func checkpointMetadataFixture(t *testing.T) (*BatchStore, *checkpointTestDescriber, *ownerTestClock, OwnerLease) {
	t.Helper()
	owners, db, clock, key := ownerFixture(t)
	lease, err := owners.Acquire(t.Context(), key, "test-runtime")
	if err != nil {
		t.Fatal(err)
	}
	describer := &checkpointTestDescriber{describe: func(context.Context, []byte) (BatchCheckpointDescription, error) {
		return checkpointTestDescription(), nil
	}}
	store := NewBatchStore(db.DB, BatchStoreConfig{Enabled: true, Owners: owners, Authority: func(context.Context, *sql.Tx, BatchAccess) error { return nil }, Adapter: &batchTestAdapter{}, Limits: DefaultBatchLimits(), CheckpointIdentity: &BatchCheckpointIdentityConfig{Enabled: true, Adapter: describer, MaxMetadataBytes: 4096}})
	return store, describer, clock, lease
}
func TestCheckpointMetadataInitialSavedRetryAndLegacyUnknown(t *testing.T) {
	store, describer, _, lease := checkpointMetadataFixture(t)
	store.config.CheckpointIdentity.Enabled = false
	if err := store.Initialize(t.Context(), lease, []byte("0")); err != nil {
		t.Fatal(err)
	}
	var legacy sql.NullString
	if err := store.db.QueryRow(`SELECT checkpoint_metadata_json FROM mooring_collab_checkpoints WHERE revision='0'`).Scan(&legacy); err != nil || legacy.Valid {
		t.Fatal("legacy inferred", legacy, err)
	}
	store.config.CheckpointIdentity.Enabled = true
	calls := 0
	describer.describe = func(_ context.Context, state []byte) (BatchCheckpointDescription, error) {
		calls++
		if string(state) != "1" {
			t.Fatalf("wrong candidate %q", state)
		}
		state[0] = '9' // descriptor receives ownership-isolated bytes.
		return checkpointTestDescription(), nil
	}
	change := testBatchChange(t, lease.Key, "meta-change", "", testBatchOp("meta-op"))
	request := BatchRequest{lease, "0", []BatchChange{change}}
	saved, err := store.Commit(t.Context(), request)
	if err != nil || saved.Revision != "1" || saved.Changes[0].Status != "saved" {
		t.Fatal(saved, err)
	}
	expected, err := canonicalCheckpointDescription(checkpointTestDescription(), 4096)
	if err != nil {
		t.Fatal(err)
	}
	var retained string
	if err = store.db.QueryRow(`SELECT checkpoint_metadata_json FROM mooring_collab_checkpoints WHERE revision='1'`).Scan(&retained); err != nil || retained != string(expected) {
		t.Fatal(retained, err)
	}
	_, state, err := store.ReadCheckpoint(t.Context(), lease.Key)
	if err != nil || string(state) != "1" {
		t.Fatal("descriptor mutated state", string(state), err)
	}
	describer.describe = func(context.Context, []byte) (BatchCheckpointDescription, error) {
		t.Fatal("known retry re-described")
		return BatchCheckpointDescription{}, ErrInvalid
	}
	retried, err := store.Commit(t.Context(), request)
	if err != nil || retried.Revision != "1" || calls != 1 {
		t.Fatal(retried, err, calls)
	}
	if err = store.db.QueryRow(`SELECT checkpoint_metadata_json FROM mooring_collab_checkpoints WHERE revision='0'`).Scan(&legacy); err != nil || legacy.Valid {
		t.Fatal("legacy backfilled", legacy, err)
	}
	if _, err = store.db.Exec(`UPDATE mooring_collab_checkpoints SET checkpoint_metadata_json=? WHERE revision='0'`, string(expected)); err == nil {
		t.Fatal("NULL guessed via UPDATE")
	}
	if _, err = store.db.Exec(`UPDATE mooring_collab_checkpoints SET checkpoint_metadata_json=NULL WHERE revision='1'`); err == nil {
		t.Fatal("retained metadata erased")
	}
}
func TestCheckpointMetadataInitializationCapacityAndFinalOwnerFence(t *testing.T) {
	canonical, err := canonicalCheckpointDescription(checkpointTestDescription(), 4096)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"exact", "one-byte-over", "expired-during-description", "final-authority-denied"} {
		t.Run(mode, func(t *testing.T) {
			store, describer, clock, lease := checkpointMetadataFixture(t)
			store.config.Limits.CheckpointBytes = 1
			store.config.Limits.TotalCheckpointBytes = int64(len(canonical) + 1)
			if mode == "one-byte-over" {
				store.config.Limits.TotalCheckpointBytes--
			}
			if mode == "expired-during-description" {
				describer.describe = func(context.Context, []byte) (BatchCheckpointDescription, error) {
					clock.advance(6 * time.Second)
					return checkpointTestDescription(), nil
				}
			}
			deny := errors.New("final original source denied")
			if mode == "final-authority-denied" {
				store.config.Authority = func(_ context.Context, _ *sql.Tx, a BatchAccess) error {
					if a.IsCommitFence() {
						return deny
					}
					return nil
				}
			}
			err := store.Initialize(t.Context(), lease, []byte("0"))
			if mode == "exact" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatal("invalid initialization committed")
			}
			if mode == "one-byte-over" && !errors.Is(err, ErrBatchCapacity) {
				t.Fatal(err)
			}
			if mode == "expired-during-description" && !errors.Is(err, ErrOwnerStale) {
				t.Fatal(err)
			}
			if mode == "final-authority-denied" && !errors.Is(err, deny) {
				t.Fatal(err)
			}
			for _, table := range []string{"mooring_collab_documents", "mooring_collab_checkpoints", "mooring_collab_audit"} {
				if batchCount(t, store, table) != 0 {
					t.Fatal("partial initialize", table)
				}
			}
		})
	}
}
func TestCheckpointMetadataSavedAggregateQuotaRejectsBeforeCommit(t *testing.T) {
	store, _, _, lease := checkpointMetadataFixture(t)
	if err := store.Initialize(t.Context(), lease, []byte("0")); err != nil {
		t.Fatal(err)
	}
	canonical, err := canonicalCheckpointDescription(checkpointTestDescription(), 4096)
	if err != nil {
		t.Fatal(err)
	}
	store.config.Limits.CheckpointBytes = 1
	store.config.Limits.TotalCheckpointBytes = 2*int64(len(canonical)+1) - 1
	change := testBatchChange(t, lease.Key, "capacity-change", "", testBatchOp("capacity-op"))
	result, err := store.Commit(t.Context(), BatchRequest{lease, "0", []BatchChange{change}})
	if err != nil || result.Revision != "0" || result.Changes[0].Status != "rejected" || result.Changes[0].Operations[0].Reason.Code != "quota_exceeded" {
		t.Fatal(result, err)
	}
	if batchCount(t, store, "mooring_collab_checkpoints") != 1 {
		t.Fatal("checkpoint quota overflow committed")
	}
}
func TestCheckpointMetadataCanonicalBoundsAndNoDefaultManifest(t *testing.T) {
	description := checkpointTestDescription()
	digest := protocol.Digest("sha256:" + strings.Repeat("0", 64))
	description.Assets = []protocol.Asset{{AssetID: "\ue000", Digest: digest, SizeBytes: "1"}, {AssetID: "😀", Digest: digest, SizeBytes: "18446744073709551615"}}
	canonical, err := canonicalCheckpointDescription(description, 4096)
	if err != nil || strings.Index(string(canonical), "😀") > strings.Index(string(canonical), "\ue000") || description.Assets[0].AssetID != "\ue000" {
		t.Fatal(string(canonical), err)
	}
	for _, mode := range []string{"nil", "duplicate", "bad-counter", "nul-id", "bad-version"} {
		value := checkpointTestDescription()
		switch mode {
		case "nil":
			value.Assets = nil
		case "duplicate":
			value.Assets = []protocol.Asset{{AssetID: "same", Digest: digest, SizeBytes: "1"}, {AssetID: "same", Digest: digest, SizeBytes: "1"}}
		case "bad-counter":
			value.Assets = []protocol.Asset{{AssetID: "a", Digest: digest, SizeBytes: "18446744073709551616"}}
		case "nul-id":
			value.FormatVersion = "bad\x00format"
		case "bad-version":
			value.Versions.ModelVersion = "00.2.0"
		}
		if _, err := canonicalCheckpointDescription(value, 4096); err == nil {
			t.Fatal("accepted", mode)
		}
	}
	if _, err := canonicalCheckpointDescription(description, len(canonical)-1); !errors.Is(err, ErrBatchCapacity) {
		t.Fatal("metadata byte cap", err)
	}
}

func TestCheckpointMetadataFinalUsageIncludesTriggerEffects(t *testing.T) {
	for _, mode := range []string{"initialize", "saved"} {
		t.Run(mode, func(t *testing.T) {
			store, _, _, lease := checkpointMetadataFixture(t)
			metadata, err := canonicalCheckpointDescription(checkpointTestDescription(), 4096)
			if err != nil {
				t.Fatal(err)
			}
			store.config.Limits.CheckpointBytes = 1
			store.config.Limits.TotalCheckpointBytes = int64(len(metadata) + 1)
			if mode == "saved" {
				if err = store.Initialize(t.Context(), lease, []byte("0")); err != nil {
					t.Fatal(err)
				}
				store.config.Limits.TotalCheckpointBytes *= 2
			}
			kind := "initialized"
			if mode == "saved" {
				kind = "change_saved"
			}
			_, err = store.db.Exec(`CREATE TRIGGER test_metadata_quota AFTER INSERT ON mooring_collab_audit WHEN NEW.kind='` + kind + `' BEGIN
            INSERT INTO mooring_collab_checkpoints(page_id,workspace_id,revision,checkpoint_base64,decoded_bytes,checkpoint_generation,checkpoint_metadata_json)
            VALUES('p','w','99','MA==',1,'1','` + string(metadata) + `'); END`)
			if err != nil {
				t.Fatal(err)
			}
			events := 0
			store.config.AfterCommit = func(BatchCommitEvent) { events++ }
			if mode == "initialize" {
				err = store.Initialize(t.Context(), lease, []byte("0"))
			} else {
				change := testBatchChange(t, lease.Key, "trigger-change", "", testBatchOp("trigger-op"))
				_, err = store.Commit(t.Context(), BatchRequest{lease, "0", []BatchChange{change}})
			}
			if !errors.Is(err, ErrBatchCapacity) {
				t.Fatal("final metadata overflow escaped", err)
			}
			want := 0
			if mode == "saved" {
				want = 1
			}
			if batchCount(t, store, "mooring_collab_checkpoints") != want || batchCount(t, store, "mooring_collab_operations") != 0 || events != 0 {
				t.Fatal("partial state or event escaped")
			}
		})
	}
}
