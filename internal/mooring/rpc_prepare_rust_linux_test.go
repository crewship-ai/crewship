//go:build linux && mooring_rust_integration

package mooring

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Explicit process/SQL qualification, separate from default Go CI and from
// real participant authorization or UI acceptance. Missing fixtures fail.
func TestRPCPrepareActualRustSQLCommitAndRecovery(t *testing.T) {
	required := func(name string) string {
		t.Helper()
		v := os.Getenv(name)
		if v == "" {
			t.Fatalf("explicit integration requires %s", name)
		}
		return v
	}
	binary := required("MOORING_TEST_RUST_BINARY")
	binaryBytes, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	binaryDigest := sha256.Sum256(binaryBytes)
	if hex.EncodeToString(binaryDigest[:]) != required("MOORING_TEST_RUST_SHA256") {
		t.Fatal("actual Rust binary digest mismatch")
	}
	readFixture := func(name string) []byte {
		t.Helper()
		raw, err := os.ReadFile(required(name))
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	initial := readFixture("MOORING_TEST_WORDCRAFT_CHECKPOINT")
	payload := readFixture("MOORING_TEST_WORDCRAFT_PAYLOAD")
	var reads []BatchReadPrecondition
	if json.Unmarshal(readFixture("MOORING_TEST_WORDCRAFT_READS"), &reads) != nil {
		t.Fatal("invalid read fixture")
	}
	owners, db, _, key := ownerFixture(t)
	key.ApplicationID = "wordcraft"
	lease, err := owners.Acquire(t.Context(), key, "actual-rust-runtime")
	if err != nil {
		t.Fatal(err)
	}
	versions := RuntimeVersions{Protocol: "0.4.0", Model: "0.2.0", Engine: "0.10.0"}
	runtime, err := StartRuntime(t.Context(), RuntimeConfig{Enabled: true, Binary: binary, Versions: versions, ControlProfile: PrivatePrepareProfile})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtime.Close() }()
	adapter, err := NewRPCPrepareAdapter(runtime, key, versions)
	if err != nil {
		t.Fatal(err)
	}
	config := BatchStoreConfig{Enabled: true, Owners: owners, Authority: func(context.Context, *sql.Tx, BatchAccess) error { return nil }, Limits: DefaultBatchLimits(), Targets: &TargetStoreConfig{Enabled: true, Adapter: adapter, Limits: DefaultBatchTargetLimits()}}
	store := NewBatchStore(db.DB, config)
	if err = store.Initialize(t.Context(), lease, initial); err != nil {
		t.Fatal("actual Rust checkpoint validation", err)
	}
	operation := testBatchOp("actual-agent-op")
	operation.Payload = payload
	operation.Reads = reads
	change := testBatchChange(t, key, "actual-agent-change", "", operation)
	events := 0
	store.config.AfterCommit = func(event BatchCommitEvent) {
		events++
		if batchCount(t, store, "mooring_collab_target_effects") != 1 {
			t.Error("event before durable target effects")
		}
	}
	request := BatchRequest{lease, "0", []BatchChange{change}}
	result, err := store.Commit(t.Context(), request)
	if err != nil || result.Revision != "1" || len(result.Changes) != 1 || result.Changes[0].Status != "saved" || events != 1 {
		t.Fatal("actual Rust SQL commit", result, events, err)
	}
	revision, saved, err := store.ReadCheckpoint(t.Context(), key)
	if err != nil || revision != "1" || bytes.Equal(saved, initial) {
		t.Fatal("candidate not persisted", revision, err)
	}
	if _, err = adapter.ValidateTargets(t.Context(), saved); err != nil {
		t.Fatal("actual Rust persisted candidate restore", err)
	}
	// Stop the actual engine, then recover status/checkpoint from a fresh host
	// store without using it. Durable outcomes cannot depend on a Rust ledger.
	_ = runtime.Close()
	recovered := NewBatchStore(db.DB, config)
	status, err := recovered.Status(t.Context(), key, operation.ID, change.Operations[0].Fingerprint)
	if err != nil || status == nil || status.Status != "saved" || status.Revision != "1" || status.Durability != "node" {
		t.Fatal("host-only durable status recovery", status, err)
	}
	_, restored, err := recovered.ReadCheckpoint(t.Context(), key)
	if err != nil || !bytes.Equal(restored, saved) {
		t.Fatal("host-only checkpoint recovery", err)
	}
	if retry, err := store.Commit(t.Context(), request); err != nil || retry.Revision != "1" || events != 1 {
		t.Fatal("immutable retry reapplied after engine death", retry, events, err)
	}
	if output := os.Getenv("MOORING_TEST_OUTPUT_DIRECTORY"); output != "" {
		info, err := os.Stat(output)
		if err != nil || !info.IsDir() {
			t.Fatal("explicit output directory must exist")
		}
		if err = os.WriteFile(filepath.Join(output, "saved-checkpoint.json"), saved, 0600); err != nil {
			t.Fatal(err)
		}
		receipt, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(output, "host-result.json"), receipt, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
