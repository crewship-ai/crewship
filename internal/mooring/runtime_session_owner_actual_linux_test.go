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

// Actual Go/Rust owned-document startup and repeated FULL SQLite/BOOTTIME renewal.
// Participant authority is MOCK_ALLOW_ALL. No listener, participant, Saved,
// live revocation, writer-losslessness or production service claim is made.
func TestSessionOwnerMaintenanceActualRustAndBootOwner(t *testing.T) {
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
	owners := NewOwnerStore(db.DB, true, clock, 8*time.Second)
	runtime, err := StartSessionRuntime(t.Context(), SessionRuntimeConfig{Runtime: RuntimeConfig{Enabled: true, Binary: binary, Versions: sessionTestVersions, ControlProfile: PrivateSessionProfile}, Scope: key})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	adapter, err := NewSessionCausalAdapter(runtime, key, sessionTestVersions)
	if err != nil {
		t.Fatal(err)
	}
	store := NewBatchStore(db.DB, BatchStoreConfig{Enabled: true, Owners: owners, CheckpointIdentity: &BatchCheckpointIdentityConfig{Enabled: true, Adapter: adapter, MaxMetadataBytes: 65536}, Authority: func(context.Context, *sql.Tx, BatchAccess) error { return nil }, Limits: DefaultBatchLimits(), Targets: &TargetStoreConfig{Enabled: true, Adapter: adapter, ReadPolicy: CausalTargetReadPolicy, HistoryBudget: DefaultBatchHistoryBudget(), Limits: DefaultBatchTargetLimits()}})
	scope := adapter.causal.base.wireScope()
	versions := adapter.causal.base.wireVersions()
	sessionID, err := prepareRequestID()
	if err != nil {
		t.Fatal(err)
	}
	open := protocol.SessionOpen{Type: "session_open", RequiredCapabilities: []protocol.SessionOpenRequiredCapabilitiesItem{"checkpoint_identity_v1"}, RequestID: "actual-open", ControlProfile: PrivateSessionProfile, Scope: scope, SessionID: sessionID, RuntimeRef: protocol.Identifier(runtime.RuntimeRef()), Versions: versions}
	opened := make(chan error, 1)
	go func() { _, e := runtime.Open(t.Context(), open); opened <- e }()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
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
	phaseStarted := time.Now()
	initialOwners := NewOwnerStore(db.DB, true, clock, time.Minute)
	initial, err := initialOwners.Acquire(ctx, key, "prior-runtime")
	if err != nil {
		t.Fatal(err)
	}
	// The real Rust prepare worker must progress even before owner grant/restore.
	if err = store.Initialize(ctx, initial, checkpoint); err != nil {
		t.Fatal("actual initialization while Open pending", err)
	}
	t.Log("actual initialized", time.Since(phaseStarted))
	// Voluntary host release uses the real BOOTTIME owner writer and retains
	// creator provenance. Waiting for an old lease inside Open would consume
	// its fixed five-second startup budget rather than qualify checkpoint I/O.
	if err = initialOwners.Release(ctx, initial); err != nil {
		t.Fatal("actual owner release", err)
	}
	lease, err := owners.Acquire(ctx, key, runtime.RuntimeRef())
	if err != nil || lease.Generation != initial.Generation+1 {
		t.Fatal("actual owner takeover", lease.Generation, err)
	}
	grant := protocol.OwnerGrant{Type: "owner_grant", RequestID: acquire.RequestID, Scope: scope, SessionID: sessionID, RuntimeRef: open.RuntimeRef, Fence: protocol.Fence{Generation: protocol.Counter(strconv.FormatInt(lease.Generation, 10)), Token: lease.Token}, LeaseDurationMs: "8000", Revision: "0", RenewalNonce: acquire.RenewalNonce, PermissionVersion: "1"}
	grantRaw, _ := json.Marshal(grant)
	if err = runtime.ReplyControl(ctx, control, grantRaw); err != nil {
		t.Fatal(err)
	}
	t.Log("actual owner granted", time.Since(phaseStarted))
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
		t.Fatal("actual prepare while restoration withheld", err, "process cause", runtime.Err())
	}
	t.Log("actual target validation complete", time.Since(phaseStarted))
	opaque, err := encodePreparedCheckpoint(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	wireTargets := make([]protocol.TargetVersion, 0, len(targets))
	for _, v := range targets {
		wireTargets = append(wireTargets, protocol.TargetVersion{TargetID: protocol.Identifier(v.TargetID), Revision: protocol.Counter(v.Revision), StructureRevision: protocol.Counter(v.StructureRevision), Deleted: v.Deleted})
	}
	restore := protocol.SessionRestore{Type: "session_restore", RequestID: "actual-restore", ControlProfile: PrivateSessionProfile, Scope: scope, SessionID: sessionID, RuntimeRef: open.RuntimeRef, Fence: grant.Fence, Versions: versions, Targets: wireTargets, Checkpoint: protocol.Checkpoint{Scope: scope, Revision: "0", Generation: protocol.Counter(strconv.FormatInt(initial.Generation, 10)), FormatVersion: "wordcraft-0.10.0/yrs-0.28.0/v2", Versions: versions, Encoding: "base64", State: opaque.State, Digest: opaque.Digest, Assets: []protocol.Asset{}}}
	snapshot, err := store.ReadFullCheckpoint(ctx, lease)
	if err != nil {
		t.Fatal("authorized full checkpoint startup read", err)
	}
	restore.Checkpoint = snapshot.Checkpoint
	t.Log("actual retained full checkpoint read", time.Since(phaseStarted))

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
	fullBasis, e := activation.InitialFullBasis()
	if e != nil || fullBasis.Revision != string(snapshot.Checkpoint.Revision) || fullBasis.Digest != string(snapshot.Checkpoint.Digest) || fullBasis.Digest == activation.InitialBasis().Digest {
		t.Fatal("full/content activation identities were conflated", e)
	}
	if err = store.transaction(ctx, func(tx *sql.Tx) error { return activation.Check(ctx, tx, store) }); err != nil {
		t.Fatal(err)
	}
	// The SQL lease and advertised duration agree. After the first real eight-
	// second grant, renewal switches to two seconds without changing its fence.
	shortOwners := NewOwnerStore(db.DB, true, clock, 2*time.Second)
	seen := make(map[protocol.Identifier]bool)
	completedReplies := make(map[protocol.Identifier][]byte)
	nextFresh := func() (SessionControl, protocol.OwnerRenew) {
		t.Helper()
		for {
			token, e := runtime.NextControl(ctx)
			if e != nil {
				t.Fatal("actual renewal missing", e, runtime.Err())
			}
			frame, e := token.Frame()
			if e != nil {
				t.Fatal(e)
			}
			var renew protocol.OwnerRenew
			if json.Unmarshal(frame, &renew) != nil || renew.Type != "owner_renew" || renew.Scope != scope || renew.SessionID != sessionID || renew.Fence != grant.Fence {
				t.Fatal("renewal lost exact owner binding")
			}
			if reply, ok := completedReplies[renew.RenewalNonce]; ok {
				// The host replays its original duration without renewing SQL or
				// resetting the runtime's original request-send bound.
				if e = runtime.ReplyControl(ctx, token, reply); e != nil {
					t.Fatal(e)
				}
				continue
			}
			return token, renew
		}
	}
	renewalStarted := time.Now()
	for cycle := 0; cycle < 4; cycle++ {
		token, renew := nextFresh()
		if seen[renew.RenewalNonce] {
			t.Fatal("renewal nonce reused", cycle)
		}
		seen[renew.RenewalNonce] = true
		if cycle > 0 && renew.PermissionVersion != "2" {
			t.Fatal("owner permission floor did not advance")
		}
		if _, e := shortOwners.Renew(ctx, lease); e != nil {
			t.Fatal("real BOOTTIME SQL renewal", cycle, e)
		}
		grant.RequestID, grant.RenewalNonce = renew.RequestID, renew.RenewalNonce
		grant.LeaseDurationMs, grant.PermissionVersion = "2000", "2"
		reply, _ := json.Marshal(grant)
		completedReplies[renew.RenewalNonce] = reply
		if e := runtime.ReplyControl(ctx, token, reply); e != nil {
			t.Fatal(e)
		}
		// A real stateless application request progresses on the same FD while
		// the actor is maintaining its owner, without changing the checkpoint.
		if _, e := adapter.ValidateTargets(ctx, checkpoint); e != nil {
			t.Fatal("independent prepare lane", cycle, e)
		}
		if e := store.transaction(ctx, func(tx *sql.Tx) error { return activation.Check(ctx, tx, store) }); e != nil {
			t.Fatal("renewed activation", cycle, e)
		}
	}
	// Withhold the next reply. Retries cannot prolong the original send bound;
	// a later valid grant cannot reopen the expired child.
	held, late := nextFresh()
	if seen[late.RenewalNonce] {
		t.Fatal("fresh withheld renewal missing")
	}
	select {
	case <-runtime.Done():
	case <-time.After(4 * time.Second):
		t.Fatal("unanswered renewal kept expired child alive")
	}
	grant.RequestID, grant.RenewalNonce = late.RequestID, late.RenewalNonce
	lateReply, _ := json.Marshal(grant)
	if e := runtime.ReplyControl(ctx, held, lateReply); e == nil {
		t.Fatal("late grant reopened expired transport")
	}
	if _, e := activation.SessionRuntime(); e == nil {
		t.Fatal("expired child retained activation")
	}
	if time.Since(phaseStarted) < 8*time.Second {
		t.Fatal("proof did not outlive initial advertised duration")
	}
	// Fixture-only SQL inspection after expiry proves no content changed; it is
	// deliberately not an authorized external read through the dead activation.
	var retained []byte
	if err = store.transaction(ctx, func(tx *sql.Tx) error {
		var e error
		retained, e = sessionCheckpoint(ctx, tx, key, "0", true)
		return e
	}); err != nil || !bytes.Equal(retained, checkpoint) {
		t.Fatal("host checkpoint changed after actual child death", err)
	}
	report := map[string]any{"rust_source": source, "binary_sha256": binarySHA, "checkpoint_sha256": checkpointSHA, "owner_clock": "Linux CLOCK_BOOTTIME", "takeover": "explicit host Release followed by fresh Acquire", "checkpoint_generation": initial.Generation, "active_owner_generation": lease.Generation, "checkpoint_digest_profile": "full-tuple-jcs-v0.4", "content_digest": canonicalDigest(checkpoint), "ownership_sql": "FULL", "startup_acknowledged": true, "renewal_cycles": 4, "short_owner_ttl_ms": 2000, "renewal_elapsed_ms": time.Since(renewalStarted).Milliseconds(), "acquire_to_expiry_ms": time.Since(phaseStarted).Milliseconds(), "late_grant_denied": true, "prepare_while_restore_withheld": true, "target_count": len(targets), "checkpoint_bytes": len(checkpoint), "participant_authority": "MOCK_ALLOW_ALL", "saved_receipts": 0, "production_cli_enabled": false}
	encoded, _ := json.MarshalIndent(report, "", "  ")
	if err = os.WriteFile(filepath.Join(output, "owner-maintenance-evidence.json"), append(encoded, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(output, "retained-checkpoint.json"), retained, 0600); err != nil {
		t.Fatal(err)
	}
}
