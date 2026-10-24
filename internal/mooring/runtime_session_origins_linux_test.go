//go:build linux

package mooring

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

// Synthetic inherited peer tests transport/provenance/fences, NOT app semantics.
// The separately tagged real native test must qualify the actual inspector.
func TestSessionOriginsHelper(t *testing.T) {
	mode := ""
	for i, arg := range os.Args {
		if arg == "--origin-peer" && i+1 < len(os.Args) {
			mode = os.Args[i+1]
		}
	}
	if mode == "" {
		return
	}
	file := os.NewFile(3, "origin-rpc")
	conn, err := net.FileConn(file)
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	rpc := &RPC{conn: conn}
	defer rpc.Close()
	raw, err := rpc.Read()
	if err != nil {
		t.Fatal(err)
	}
	var hello map[string]any
	if json.Unmarshal(raw, &hello) != nil {
		t.Fatal("hello")
	}
	write := func(v any) {
		b, e := json.Marshal(v)
		if e != nil || rpc.Write(b) != nil {
			t.Fatal("bounded reply")
		}
	}
	write(map[string]any{"type": "runtime_ready", "request_id": hello["request_id"], "selected": sessionTestVersions, "selected_control_profile": PrivateSessionProfile})
	rpc.maxFrame = MaxPrivatePrepareRPCFrame
	raw, err = rpc.Read()
	if err != nil {
		return
	}
	var open map[string]any
	if json.Unmarshal(raw, &open) != nil {
		t.Fatal("open")
	}
	write(map[string]any{"type": "owner_acquire", "request_id": "acquire", "scope": open["scope"], "runtime_ref": open["runtime_ref"], "renewal_nonce": "nonce"})
	var retirement *retirementTestPeer
	if strings.HasPrefix(mode, "retirement-") {
		retirement = &retirementTestPeer{t: t, write: write, mode: mode}
	}
	for {
		raw, err = rpc.Read()
		if err != nil {
			return
		}
		var v map[string]any
		if json.Unmarshal(raw, &v) != nil {
			return
		}
		switch v["type"] {
		case "owner_grant":
		case "session_describe_checkpoint":
			write(map[string]any{"type": "session_checkpoint_described", "request_id": v["request_id"], "control_profile": PrivateSessionProfile, "scope": v["scope"], "session_id": v["session_id"], "runtime_ref": v["runtime_ref"], "versions": v["versions"], "input_digest": v["checkpoint"].(map[string]any)["digest"], "description": map[string]any{"format_version": "wordcraft-0.10.0/yrs-0.28.0/v2", "versions": sessionTestVersions, "assets": []any{}}})
		case "validate_checkpoint_causal":
			write(map[string]any{"type": "checkpoint_validated_causal", "request_id": v["request_id"], "scope": v["scope"], "selected": sessionTestVersions, "control_profile": PrivateCausalPrepareProfile, "input_checkpoint_digest": v["checkpoint"].(map[string]any)["digest"], "inventory": []any{}})
		case "session_restore":
			cp := v["checkpoint"].(map[string]any)
			result := map[string]any{"type": "session_restored", "request_id": v["request_id"], "control_profile": PrivateSessionProfile, "scope": v["scope"], "session_id": v["session_id"], "runtime_ref": v["runtime_ref"], "versions": v["versions"], "fence": v["fence"], "confirmed_basis": map[string]any{"revision": cp["revision"], "digest": cp["digest"]}}
			write(result)
			result["type"] = "session_opened"
			result["request_id"] = open["request_id"]
			result["capabilities"] = open["required_capabilities"]
			if mode == "missing-echo" {
				result["capabilities"] = []string{"checkpoint_identity_v1"}
			}
			write(result)
			if mode == "participant-notification-stall" {
				time.Sleep(20 * time.Second)
				return
			}
		case "ticket_validated", "protocol_error":
			if retirement != nil {
				retirement.handle(v)
			}
		case "session_participant_open", "session_participant_input":
			if retirement != nil {
				retirement.handle(v)
				continue
			}
			if mode != "participant-notifications" {
				return
			}
			participant, gate, permission := v["participant_ref"], v["gate_id"], v["permission_version"]
			if v["type"] == "session_participant_open" {
				participant, gate, permission = "synthetic-participant", "synthetic-gate", "1"
			}
			fence := v["fence"].(map[string]any)
			message := map[string]any{"type": "session_joined", "request_id": "joined", "scope": v["scope"], "session_id": v["session_id"], "participant_ref": participant, "generation": fence["generation"], "permission_version": permission, "versions": sessionTestVersions, "gate_id": gate}
			write(map[string]any{"type": "session_participant_output", "request_id": "output-" + v["request_id"].(string), "control_profile": PrivateSessionProfile, "scope": v["scope"], "session_id": v["session_id"], "runtime_ref": v["runtime_ref"], "fence": v["fence"], "connection_ref": v["connection_ref"], "participant_ref": participant, "gate_id": gate, "permission_version": permission, "message": message})
		case "session_inspect_origins":
			cp := v["checkpoint"].(map[string]any)
			var full protocol.Checkpoint
			_ = convertPrepareValue(cp, &full)
			identity, e := VerifyCheckpointIdentity(full, CheckpointIdentityLimits{DecodedBytes: maxPreparedCheckpoint, PreimageBytes: sessionOutputBytes})
			if e != nil {
				t.Fatal(e)
			}
			result := map[string]any{"type": "session_origins_inspected", "request_id": v["request_id"], "control_profile": PrivateSessionProfile, "scope": v["scope"], "session_id": v["session_id"], "runtime_ref": v["runtime_ref"], "fence": v["fence"], "versions": v["versions"], "checkpoint_basis": map[string]any{"revision": cp["revision"], "digest": cp["digest"]}, "checkpoint_generation": cp["generation"], "content_digest": identity.ContentDigest, "allocated": []any{map[string]any{"origin": "0", "clock": "1"}, map[string]any{"origin": "9007199254740991", "clock": "4294967295"}}}
			switch mode {
			case "wrong-fence":
				result["fence"] = map[string]any{"generation": "9", "token": "x234567890123456789012345678901234"}
			case "wrong-digest":
				result["content_digest"] = "sha256:" + string(makeDigestZeros())
			case "wrong-session":
				result["session_id"] = "foreign"
			case "wrong-creator":
				result["checkpoint_generation"] = "9"
			case "wide-origin":
				result["allocated"] = []any{map[string]any{"origin": "9007199254740992", "clock": "1"}}
			case "wide-clock":
				result["allocated"] = []any{map[string]any{"origin": "1", "clock": "4294967296"}}
			case "duplicate":
				result["allocated"] = []any{map[string]any{"origin": "1", "clock": "1"}, map[string]any{"origin": "1", "clock": "2"}}
			}
			write(result)
		default:
			return
		}
	}
}

var errOriginSourceDenied = errors.New("synthetic original source changed")

type originFixture struct {
	store      *BatchStore
	activation *SessionActivation
	owner      OwnerLease
	basis      FullCheckpointBasis
	clock      *ownerTestClock
	runtime    *SessionRuntime
}

func sessionOriginsFixture(t *testing.T, mode string, optin bool) originFixture {
	t.Helper()
	owners, db, clock, key := ownerFixture(t)
	key.ApplicationID = "wordcraft"
	for _, query := range []string{`CREATE TABLE origin_test_source (generation INTEGER NOT NULL)`, `INSERT INTO origin_test_source VALUES(0)`, `CREATE TABLE origin_test_visits (origin TEXT PRIMARY KEY, clock TEXT NOT NULL)`} {
		if _, e := db.Exec(query); e != nil {
			t.Fatal(e)
		}
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestSessionOriginsHelper$", "--", "--origin-peer", mode)
	runtime, e := startSessionRuntimeCommand(t.Context(), SessionRuntimeConfig{Runtime: RuntimeConfig{Versions: sessionTestVersions, ControlProfile: PrivateSessionProfile}, Scope: key}, cmd)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	adapter, e := NewSessionCausalAdapter(runtime, key, sessionTestVersions)
	if e != nil {
		t.Fatal(e)
	}
	authority := func(ctx context.Context, tx *sql.Tx, access BatchAccess) error {
		var generation int
		if e := tx.QueryRowContext(ctx, `SELECT generation FROM origin_test_source`).Scan(&generation); e != nil {
			return e
		}
		if generation != 0 {
			return errOriginSourceDenied
		}
		return nil
	}
	store := NewBatchStore(db.DB, BatchStoreConfig{Enabled: true, Owners: owners, CheckpointIdentity: &BatchCheckpointIdentityConfig{Enabled: true, Adapter: adapter, MaxMetadataBytes: 65536}, Authority: authority, Limits: DefaultBatchLimits(), Targets: &TargetStoreConfig{Enabled: true, Adapter: adapter, ReadPolicy: CausalTargetReadPolicy, HistoryBudget: DefaultBatchHistoryBudget(), Limits: DefaultBatchTargetLimits()}})
	if strings.HasPrefix(mode, "retirement-decline") {
		// MOCK native transport only. Actual application callbacks always DENY;
		// these mandatory hooks confer no source/application write qualification.
		store.config.Targets.SessionHumanPrepare = func(context.Context, *sql.Tx, SessionHumanPrepareAccess) (*protocol.SessionHumanPrepareContext, error) {
			return nil, errOriginSourceDenied
		}
		store.config.Targets.SessionNativeCandidate = func(context.Context, *sql.Tx, SessionNativeCandidateAccess) error { return errOriginSourceDenied }
	}
	var open protocol.SessionOpen
	_ = json.Unmarshal(sessionTestOpen(runtime), &open)
	if strings.HasPrefix(mode, "retirement-decline") {
		open.RequiredCapabilities = append(open.RequiredCapabilities, "human_native_prepare_v1")
	}
	if optin {
		open.RequiredCapabilities = append(open.RequiredCapabilities, "origin_inventory_v1")
	}
	opened := make(chan error, 1)
	go func() { _, e := runtime.Open(t.Context(), open); opened <- e }()
	control, e := runtime.NextControl(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	raw, e := control.Frame()
	if e != nil {
		t.Fatal(e)
	}
	var acquire protocol.OwnerAcquire
	if json.Unmarshal(raw, &acquire) != nil {
		t.Fatal("acquire")
	}
	owner, e := owners.Acquire(t.Context(), key, runtime.RuntimeRef())
	if e != nil {
		t.Fatal(e)
	}
	checkpoint := []byte("synthetic origins fixture, not an application engine")
	if e = store.Initialize(t.Context(), owner, checkpoint); e != nil {
		t.Fatal(e)
	}
	grant := protocol.OwnerGrant{Type: "owner_grant", RequestID: acquire.RequestID, Scope: acquire.Scope, SessionID: open.SessionID, RuntimeRef: acquire.RuntimeRef, Fence: protocol.Fence{Generation: protocol.Counter(strconv.FormatInt(owner.Generation, 10)), Token: owner.Token}, LeaseDurationMs: "5000", Revision: "0", RenewalNonce: acquire.RenewalNonce, PermissionVersion: "1"}
	raw, _ = json.Marshal(grant)
	if e = runtime.ReplyControl(t.Context(), control, raw); e != nil {
		t.Fatal(e)
	}
	snapshot, e := store.ReadFullCheckpoint(t.Context(), owner)
	if e != nil {
		t.Fatal(e)
	}
	restore := protocol.SessionRestore{Type: "session_restore", RequestID: "restore", ControlProfile: PrivateSessionProfile, Scope: open.Scope, SessionID: open.SessionID, RuntimeRef: open.RuntimeRef, Fence: grant.Fence, Versions: open.Versions, Targets: []protocol.TargetVersion{}, Checkpoint: snapshot.Checkpoint}
	if _, e = runtime.Restore(t.Context(), restore); e != nil {
		t.Fatal(e)
	}
	if e = <-opened; e != nil {
		t.Fatal(e)
	}
	activation, e := runtime.Activate(t.Context(), store, owner, restore)
	if mode == "missing-echo" {
		if e == nil {
			t.Fatal("missing capability echo activated")
		}
		return originFixture{store: store, owner: owner, runtime: runtime, clock: clock}
	}
	if e != nil {
		t.Fatal(e)
	}
	basis, e := activation.InitialFullBasis()
	if e != nil {
		t.Fatal(e)
	}
	return originFixture{store: store, activation: activation, owner: owner, basis: basis, clock: clock, runtime: runtime}
}

func TestSessionOriginInventoryCallbackCopiesAndEscapedContextsDeny(t *testing.T) {
	f := sessionOriginsFixture(t, "valid", true)
	var escaped SessionOriginInventory
	var escapedCtx context.Context
	var escapedTx *sql.Tx
	e := f.store.WithSessionOrigins(t.Context(), f.activation, f.basis, func(ctx context.Context, tx *sql.Tx, p *SessionOriginInventory) error {
		escaped = *p
		escapedCtx = ctx
		escapedTx = tx
		if e := p.Visit(context.Background(), tx, f.store, f.owner, f.basis, func(uint64, uint32) error { return nil }); e == nil {
			t.Fatal("foreign context used proof")
		}
		foreign := NewBatchStore(f.store.db, f.store.config)
		if e := p.Visit(ctx, tx, foreign, f.owner, f.basis, func(uint64, uint32) error { return nil }); e == nil {
			t.Fatal("foreign store used proof")
		}
		owner := f.owner
		owner.Generation++
		if e := p.Visit(ctx, tx, f.store, owner, f.basis, func(uint64, uint32) error { return nil }); e == nil {
			t.Fatal("fresh fence used old proof")
		}
		return p.Visit(ctx, tx, f.store, f.owner, f.basis, func(origin uint64, clock uint32) error {
			_, e := tx.ExecContext(ctx, `INSERT INTO origin_test_visits VALUES(?,?)`, strconv.FormatUint(origin, 10), strconv.FormatUint(uint64(clock), 10))
			return e
		})
	})
	if e != nil {
		t.Fatal(e)
	}
	if e = escaped.Visit(escapedCtx, escapedTx, f.store, f.owner, f.basis, func(uint64, uint32) error { return nil }); e == nil {
		t.Fatal("escaped copied evidence survived callback")
	}
	var count int
	if e = f.store.db.QueryRow(`SELECT count(*) FROM origin_test_visits`).Scan(&count); e != nil || count != 2 {
		t.Fatal(count, e)
	}
}
func TestSessionOriginInventoryFinalSourceOwnerAndBasisFencesRollback(t *testing.T) {
	for _, failure := range []string{"source-aba", "owner-expiry", "head-drift"} {
		t.Run(failure, func(t *testing.T) {
			f := sessionOriginsFixture(t, "valid", true)
			entered := false
			e := f.store.WithSessionOrigins(t.Context(), f.activation, f.basis, func(ctx context.Context, tx *sql.Tx, p *SessionOriginInventory) error {
				if e := p.Visit(ctx, tx, f.store, f.owner, f.basis, func(origin uint64, clock uint32) error {
					_, e := tx.ExecContext(ctx, `INSERT INTO origin_test_visits VALUES(?,?)`, strconv.FormatUint(origin, 10), strconv.FormatUint(uint64(clock), 10))
					return e
				}); e != nil {
					return e
				}
				entered = true
				switch failure {
				case "source-aba":
					_, e := tx.ExecContext(ctx, `UPDATE origin_test_source SET generation=2`)
					return e
				case "head-drift":
					_, e := tx.ExecContext(ctx, `UPDATE mooring_collab_documents SET revision='1' WHERE page_id=?`, f.owner.Key.ArtifactID)
					return e
				default:
					f.clock.advance(6 * time.Second)
					return nil
				}
			})
			want := error(ErrCheckpointIdentity)
			if failure == "source-aba" {
				want = errOriginSourceDenied
			}
			if failure == "owner-expiry" {
				want = ErrOwnerStale
			}
			if !entered || !errors.Is(e, want) {
				t.Fatal("final fence did not exercise expected source/owner/basis failure", entered, e)
			}
			var count int
			if e = f.store.db.QueryRow(`SELECT count(*) FROM origin_test_visits`).Scan(&count); e != nil || count != 0 {
				t.Fatal("writer changes survived failed fence", count, e)
			}
		})
	}
}
func TestSessionOriginInventoryForgedRepliesNeverReachConsumer(t *testing.T) {
	for _, mode := range []string{"wrong-fence", "wrong-digest", "wrong-session", "wrong-creator", "wide-origin", "wide-clock", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			f := sessionOriginsFixture(t, mode, true)
			called := false
			e := f.store.WithSessionOrigins(t.Context(), f.activation, f.basis, func(context.Context, *sql.Tx, *SessionOriginInventory) error { called = true; return nil })
			if e == nil || called {
				t.Fatal("forged response reached writer consumer")
			}
			select {
			case <-f.runtime.Done():
			case <-time.After(2 * time.Second):
				t.Fatal("poisoned inventory response kept process alive")
			}
		})
	}
}
func TestSessionOriginInventoryRequiresExactOptinEchoAndFullBasis(t *testing.T) {
	t.Run("missing-echo", func(t *testing.T) { _ = sessionOriginsFixture(t, "missing-echo", true) })
	t.Run("identity-only", func(t *testing.T) {
		f := sessionOriginsFixture(t, "valid", false)
		called := false
		if e := f.store.WithSessionOrigins(t.Context(), f.activation, f.basis, func(context.Context, *sql.Tx, *SessionOriginInventory) error { called = true; return nil }); e == nil || called {
			t.Fatal("identity-only acquired inventory")
		}
	})
	t.Run("content-not-full", func(t *testing.T) {
		f := sessionOriginsFixture(t, "valid", true)
		basis := f.basis
		basis.Digest = f.activation.InitialBasis().Digest
		called := false
		if e := f.store.WithSessionOrigins(t.Context(), f.activation, basis, func(context.Context, *sql.Tx, *SessionOriginInventory) error { called = true; return nil }); e == nil || called {
			t.Fatal("content digest replaced full identity")
		}
	})
	var zero SessionOriginInventory
	if e := zero.Visit(t.Context(), nil, nil, OwnerLease{}, FullCheckpointBasis{}, func(uint64, uint32) error { return nil }); e == nil {
		t.Fatal("zero evidence accepted")
	}
}

func TestSessionOriginInventoryCurrentSourceDeniesBeforePoisonedCheckpoint(t *testing.T) {
	f := sessionOriginsFixture(t, "valid", true)
	if _, e := f.store.db.Exec(`UPDATE origin_test_source SET generation=2`); e != nil {
		t.Fatal(e)
	}
	if _, e := f.store.db.Exec(`UPDATE mooring_collab_checkpoints SET checkpoint_base64='!!!!'`); e != nil {
		t.Fatal(e)
	}
	called := false
	e := f.store.WithSessionOrigins(t.Context(), f.activation, f.basis, func(context.Context, *sql.Tx, *SessionOriginInventory) error { called = true; return nil })
	if !errors.Is(e, errOriginSourceDenied) || called {
		t.Fatal("checkpoint was interpreted before current source denial", e)
	}
	for _, missing := range []context.Context{nil} {
		if e = f.store.WithSessionOrigins(missing, f.activation, f.basis, func(context.Context, *sql.Tx, *SessionOriginInventory) error { return nil }); e == nil {
			t.Fatal("nil source context accepted")
		}
	}
}
