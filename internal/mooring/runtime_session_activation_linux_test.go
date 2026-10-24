//go:build linux

package mooring

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// The child is a synthetic scoped transport peer. Actual FULL SQL ownership,
// initialized checkpoint and target inventory are used, not participant auth.
func TestSessionActivationRequiresActualTranscriptAndSameSQLStore(t *testing.T) {
	owners, db, clock, key := ownerFixture(t)
	key.ApplicationID = "wordcraft"
	s := sessionPeerKey(t, "activation", key)
	adapter, err := NewSessionCausalAdapter(s, key, sessionTestVersions)
	if err != nil {
		t.Fatal(err)
	}
	store := NewBatchStore(db.DB, BatchStoreConfig{Enabled: true, Owners: owners, Authority: func(context.Context, *sql.Tx, BatchAccess) error { return nil }, Limits: DefaultBatchLimits(), Targets: &TargetStoreConfig{Enabled: true, Adapter: adapter, ReadPolicy: CausalTargetReadPolicy, HistoryBudget: DefaultBatchHistoryBudget(), Limits: DefaultBatchTargetLimits()}})
	if _, err = s.Activate(t.Context(), store, OwnerLease{}, protocol.SessionRestore{}); err == nil {
		t.Fatal("unopened transport activated")
	}
	openRaw := sessionTestOpen(s)
	var open protocol.SessionOpen
	_ = json.Unmarshal(openRaw, &open)
	opened := make(chan error, 1)
	go func() { _, e := s.Open(t.Context(), open); opened <- e }()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	control, err := s.NextControl(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := control.Frame()
	if err != nil {
		t.Fatal(err)
	}
	var acquire protocol.OwnerAcquire
	if json.Unmarshal(raw, &acquire) != nil {
		t.Fatal("acquire")
	}
	lease, err := owners.Acquire(t.Context(), key, s.RuntimeRef())
	if err != nil {
		t.Fatal(err)
	}
	grant := protocol.OwnerGrant{Type: "owner_grant", RequestID: acquire.RequestID, Scope: acquire.Scope, SessionID: open.SessionID, RuntimeRef: acquire.RuntimeRef, Fence: protocol.Fence{Generation: protocol.Counter(strconv.FormatInt(lease.Generation, 10)), Token: lease.Token}, LeaseDurationMs: "5000", Revision: "0", RenewalNonce: acquire.RenewalNonce, PermissionVersion: "1"}
	grantRaw, _ := json.Marshal(grant)
	if err = s.ReplyControl(ctx, control, grantRaw); err != nil {
		t.Fatal(err)
	}
	checkpoint := []byte("synthetic-opaque-checkpoint-not-a-document-engine")
	if err = store.Initialize(ctx, lease, checkpoint); err != nil {
		t.Fatal(err)
	}
	opaque, err := encodePreparedCheckpoint(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	restore := protocol.SessionRestore{Type: "session_restore", RequestID: "restore", ControlProfile: PrivateSessionProfile, Scope: open.Scope, SessionID: open.SessionID, RuntimeRef: open.RuntimeRef, Fence: grant.Fence, Versions: open.Versions, Targets: []protocol.TargetVersion{}, Checkpoint: protocol.Checkpoint{Scope: open.Scope, Revision: "0", Generation: grant.Fence.Generation, FormatVersion: "wordcraft-0.10.0/yrs-0.28.0/v2", Versions: open.Versions, Encoding: "base64", State: opaque.State, Digest: opaque.Digest, Assets: []protocol.Asset{}}}
	if _, err = s.Activate(ctx, store, lease, restore); err == nil {
		t.Fatal("unobserved restore activated")
	}
	if _, err = s.Restore(ctx, restore); err != nil {
		t.Fatal(err)
	}
	if err = <-opened; err != nil {
		t.Fatal(err)
	}
	wrong := restore
	wrong.Checkpoint.State = ""
	if _, err = s.Activate(ctx, store, lease, wrong); err == nil {
		t.Fatal("copied/changed restore activated")
	}
	wrongOwner := lease
	wrongOwner.Token = "0000000000000000000000000000000000000000000000000000000000000000"
	if _, err = s.Activate(ctx, store, wrongOwner, restore); err == nil {
		t.Fatal("different owner activated")
	}
	original := store.config.Targets.Adapter
	store.config.Targets.Adapter = &targetTestAdapter{}
	if _, err = s.Activate(ctx, store, lease, restore); err == nil {
		t.Fatal("fake engine store activated")
	}
	store.config.Targets.Adapter = original
	if _, err = db.Exec(`INSERT INTO mooring_collab_target_versions(page_id,workspace_id,target_id,revision,structure_revision,deleted) VALUES(?,?,'unexpected','0','0',0)`, key.ArtifactID, key.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Activate(ctx, store, lease, restore); err == nil {
		t.Fatal("SQL inventory drift accepted")
	}
	if _, err = db.Exec(`DELETE FROM mooring_collab_target_versions WHERE page_id=? AND target_id='unexpected'`, key.ArtifactID); err != nil {
		t.Fatal(err)
	}
	activation, err := s.Activate(ctx, store, lease, restore)
	if err != nil {
		t.Fatal(err)
	}
	if activation.SessionID() != string(open.SessionID) || activation.RuntimeRef() != s.RuntimeRef() || activation.Scope() != key || activation.InitialBasis().Digest != string(opaque.Digest) {
		t.Fatal("activation bindings changed")
	}
	if _, err = activation.NewCausalAdapter(); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Activate(ctx, store, lease, restore); err == nil {
		t.Fatal("duplicate activation minted")
	}
	var copied SessionActivation
	if _, err = copied.SessionRuntime(); err == nil {
		t.Fatal("zero cap accepted")
	}
	other := NewBatchStore(db.DB, store.config)
	if err = store.transaction(ctx, func(tx *sql.Tx) error { return activation.Check(ctx, tx, other) }); err == nil {
		t.Fatal("another store accepted cap")
	}
	if err = store.transaction(ctx, func(tx *sql.Tx) error { return activation.Check(ctx, tx, store) }); err != nil {
		t.Fatal(err)
	}
	clock.advance(6 * time.Second)
	if err = store.transaction(ctx, func(tx *sql.Tx) error { return activation.Check(ctx, tx, store) }); !errors.Is(err, ErrOwnerStale) {
		t.Fatal("expired owner cap accepted", err)
	}
	_ = s.Close()
	if _, err = activation.SessionRuntime(); err == nil {
		t.Fatal("dead process cap accepted")
	}
}
