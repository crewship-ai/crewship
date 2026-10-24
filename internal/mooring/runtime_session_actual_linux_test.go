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
	"strconv"
	"testing"
	"time"

	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// Actual Go/Rust/owned WordCraft startup and FULL SQLite/BOOTTIME ownership.
// Participant authority is MOCK_ALLOW_ALL. No listener, participant, Saved,
// live revocation, writer-losslessness or production service claim is made.
func TestSessionActivationActualRustOwnedDocumentAndBootOwner(t *testing.T) {
	required := func(name string) string {
		t.Helper()
		v := os.Getenv(name)
		if v == "" {
			t.Fatalf("explicit integration requires %s", name)
		}
		return v
	}
	readPinned := func(path, digest string, limit int64) []byte {
		t.Helper()
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
			t.Fatal("bounded pinned fixture unavailable", err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(raw)
		if hex.EncodeToString(hash[:]) != digest {
			t.Fatal("fixture digest mismatch")
		}
		return raw
	}
	binary := required("MOORING_SESSION_BINARY")
	binarySHA := required("MOORING_SESSION_BINARY_SHA256")
	readPinned(binary, binarySHA, 64<<20)
	source := required("MOORING_SESSION_RUST_SOURCE")
	if decoded, e := hex.DecodeString(source); e != nil || len(decoded) != 20 {
		t.Fatal("exact Rust source required")
	}
	checkpointSHA := required("MOORING_SESSION_CHECKPOINT_SHA256")
	checkpoint := readPinned(required("MOORING_SESSION_CHECKPOINT"), checkpointSHA, maxPreparedCheckpoint)
	output := required("MOORING_SESSION_OUTPUT_DIR")
	if info, e := os.Stat(output); e != nil || !info.IsDir() {
		t.Fatal("existing output directory required")
	}
	_, db, _, key := ownerFixture(t)
	key.ApplicationID = "wordcraft"
	clock, err := NewBootOwnerClock()
	if err != nil {
		t.Fatal(err)
	}
	owners := NewOwnerStore(db.DB, true, clock, time.Minute)
	runtime, err := StartSessionRuntime(t.Context(), SessionRuntimeConfig{Runtime: RuntimeConfig{Enabled: true, Binary: binary, Versions: sessionTestVersions, ControlProfile: PrivateSessionProfile}, Scope: key})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	adapter, err := NewSessionCausalAdapter(runtime, key, sessionTestVersions)
	if err != nil {
		t.Fatal(err)
	}
	store := NewBatchStore(db.DB, BatchStoreConfig{Enabled: true, Owners: owners, Authority: func(context.Context, *sql.Tx, BatchAccess) error { return nil }, Limits: DefaultBatchLimits(), Targets: &TargetStoreConfig{Enabled: true, Adapter: adapter, ReadPolicy: CausalTargetReadPolicy, HistoryBudget: DefaultBatchHistoryBudget(), Limits: DefaultBatchTargetLimits()}})
	scope := adapter.causal.base.wireScope()
	versions := adapter.causal.base.wireVersions()
	sessionID, err := prepareRequestID()
	if err != nil {
		t.Fatal(err)
	}
	open := protocol.SessionOpen{Type: "session_open", RequestID: "actual-open", ControlProfile: PrivateSessionProfile, Scope: scope, SessionID: sessionID, RuntimeRef: protocol.Identifier(runtime.RuntimeRef()), Versions: versions}
	opened := make(chan error, 1)
	go func() { _, e := runtime.Open(t.Context(), open); opened <- e }()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	control, err := runtime.NextControl(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := control.Frame()
	if err != nil {
		t.Fatal(err)
	}
	var acquire protocol.OwnerAcquire
	if json.Unmarshal(raw, &acquire) != nil || acquire.Type != "owner_acquire" || acquire.Scope != scope || acquire.RuntimeRef != open.RuntimeRef {
		t.Fatal("actual owner acquisition binding")
	}
	lease, err := owners.Acquire(ctx, key, runtime.RuntimeRef())
	if err != nil {
		t.Fatal(err)
	}
	// The real Rust prepare worker must progress even before owner grant/restore.
	if err = store.Initialize(ctx, lease, checkpoint); err != nil {
		t.Fatal("actual initialization while Open pending", err)
	}
	grant := protocol.OwnerGrant{Type: "owner_grant", RequestID: acquire.RequestID, Scope: scope, SessionID: sessionID, RuntimeRef: open.RuntimeRef, Fence: protocol.Fence{Generation: protocol.Counter(strconv.FormatInt(lease.Generation, 10)), Token: lease.Token}, LeaseDurationMs: "60000", Revision: "0", RenewalNonce: acquire.RenewalNonce, PermissionVersion: "1"}
	grantRaw, _ := json.Marshal(grant)
	if err = runtime.ReplyControl(ctx, control, grantRaw); err != nil {
		t.Fatal(err)
	}
	var targets []BatchTargetVersion
	if err = store.transaction(ctx, func(tx *sql.Tx) error {
		if e := owners.Check(ctx, tx, lease); e != nil {
			return e
		}
		// A callback under the actual SQL writer cannot deadlock the actor reader.
		inventory, e := adapter.ValidateTargets(ctx, checkpoint)
		if e != nil {
			return e
		}
		targets, e = initialSessionTargets(ctx, tx, store, key, 0)
		if e != nil {
			return e
		}
		if len(inventory) != len(targets) {
			t.Fatal("actual complete inventory changed")
		}
		return owners.Check(ctx, tx, lease)
	}); err != nil {
		t.Fatal("actual prepare while restoration withheld", err)
	}
	opaque, err := encodePreparedCheckpoint(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	wireTargets := make([]protocol.TargetVersion, 0, len(targets))
	for _, v := range targets {
		wireTargets = append(wireTargets, protocol.TargetVersion{TargetID: protocol.Identifier(v.TargetID), Revision: protocol.Counter(v.Revision), StructureRevision: protocol.Counter(v.StructureRevision), Deleted: v.Deleted})
	}
	restore := protocol.SessionRestore{Type: "session_restore", RequestID: "actual-restore", ControlProfile: PrivateSessionProfile, Scope: scope, SessionID: sessionID, RuntimeRef: open.RuntimeRef, Fence: grant.Fence, Versions: versions, Targets: wireTargets, Checkpoint: protocol.Checkpoint{Scope: scope, Revision: "0", Generation: grant.Fence.Generation, FormatVersion: "wordcraft-0.10.0/yrs-0.28.0/v2", Versions: versions, Encoding: "base64", State: opaque.State, Digest: opaque.Digest, Assets: []protocol.Asset{}}}
	if _, err = runtime.Restore(ctx, restore); err != nil {
		t.Fatal("actual owned document restore", err)
	}
	if err = <-opened; err != nil {
		t.Fatal("actual opened acknowledgement", err)
	}
	activation, err := runtime.Activate(ctx, store, lease, restore)
	if err != nil {
		t.Fatal("actual activation", err)
	}
	if activation.SessionID() != string(sessionID) || activation.RuntimeRef() != runtime.RuntimeRef() || activation.InitialBasis().Digest != string(opaque.Digest) {
		t.Fatal("actual activation identity changed")
	}
	if err = store.transaction(ctx, func(tx *sql.Tx) error { return activation.Check(ctx, tx, store) }); err != nil {
		t.Fatal(err)
	}
	if err = runtime.Close(); err == nil {
		t.Fatal("closed transport lost poison cause")
	}
	if _, err = activation.SessionRuntime(); err == nil {
		t.Fatal("old activation survived actual Rust death")
	}
	var retained []byte
	if err = store.transaction(ctx, func(tx *sql.Tx) error {
		if e := owners.Check(ctx, tx, lease); e != nil {
			return e
		}
		var e error
		retained, e = sessionCheckpoint(ctx, tx, key, "0", true)
		return e
	}); err != nil || !bytes.Equal(retained, checkpoint) {
		t.Fatal("host checkpoint changed after actual child death", err)
	}
	report := map[string]any{"rust_source": source, "binary_sha256": binarySHA, "checkpoint_sha256": checkpointSHA, "owner_clock": "Linux CLOCK_BOOTTIME", "ownership_sql": "FULL", "startup_acknowledged": true, "prepare_while_restore_withheld": true, "target_count": len(targets), "checkpoint_bytes": len(checkpoint), "participant_authority": "MOCK_ALLOW_ALL", "saved_receipts": 0, "production_cli_enabled": false}
	encoded, _ := json.MarshalIndent(report, "", "  ")
	if err = os.WriteFile(filepath.Join(output, "activation-evidence.json"), append(encoded, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(output, "retained-checkpoint.json"), retained, 0600); err != nil {
		t.Fatal(err)
	}
}
