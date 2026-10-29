package mooring

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// MOCK describer, application and sealed ingress; actual FULL SQL, not runtime
// provenance or original-human authority qualification.
func fullBasisFixture(t *testing.T) (*BatchStore, OwnerLease, FullCheckpointBasis) {
	t.Helper()
	owners, db, _, key := ownerFixture(t)
	lease, err := owners.Acquire(t.Context(), key, "target-runtime")
	if err != nil {
		t.Fatal(err)
	}
	adapter := &causalExecutionAdapter{}
	s := NewBatchStore(db.DB, BatchStoreConfig{Enabled: true, Owners: owners, Authority: func(context.Context, *sql.Tx, BatchAccess) error { return nil }, Limits: DefaultBatchLimits(), Targets: &TargetStoreConfig{Enabled: true, Adapter: adapter, Limits: DefaultBatchTargetLimits(), ReadPolicy: CausalTargetReadPolicy, HistoryBudget: DefaultBatchHistoryBudget()}, CheckpointIdentity: &BatchCheckpointIdentityConfig{Enabled: true, Adapter: &checkpointTestDescriber{describe: func(context.Context, []byte) (BatchCheckpointDescription, error) {
		return checkpointTestDescription(), nil
	}}, MaxMetadataBytes: 4096}})
	if err = s.Initialize(t.Context(), lease, []byte("0")); err != nil {
		t.Fatal(err)
	}
	var basis FullCheckpointBasis
	if err = s.transaction(t.Context(), func(tx *sql.Tx) error {
		cp, _, e := s.loadRetainedFullCheckpoint(t.Context(), tx, key, "0", true)
		basis = FullCheckpointBasis{"0", string(cp.Digest)}
		return e
	}); err != nil {
		t.Fatal(err)
	}
	return s, lease, basis
}
func fullTestOutput(_ context.Context, _ *sql.Tx, p SessionPendingOutput) ([]byte, error) {
	if p.FullCheckpoint == nil || p.Targets == nil {
		return nil, ErrInvalid
	}
	changesRaw, e := json.Marshal(p.Result.Changes)
	if e != nil {
		return nil, e
	}
	var changes []protocol.ChangeResult
	if e = json.Unmarshal(changesRaw, &changes); e != nil {
		return nil, e
	}
	targetsRaw, e := json.Marshal(p.Targets)
	if e != nil {
		return nil, e
	}
	var targets []protocol.TargetVersion
	if e = json.Unmarshal(targetsRaw, &targets); e != nil {
		return nil, e
	}
	cp := *p.FullCheckpoint
	return json.Marshal(protocol.SessionCommitResult{Type: "session_commit_result", RequestID: "submit", ControlProfile: "mooring.session-causal.v1", Scope: cp.Scope, SessionID: "session", RuntimeRef: protocol.Identifier(p.Lease.OwnerID), Fence: protocol.Fence{Generation: protocol.Counter(strconv.FormatInt(p.Lease.Generation, 10)), Token: p.Lease.Token}, Versions: cp.Versions, Checkpoint: cp, Targets: targets, Receipt: protocol.CommitResult{Type: "commit_result", RequestID: "submit", Scope: cp.Scope, Generation: protocol.Counter(strconv.FormatInt(p.Lease.Generation, 10)), Revision: protocol.Counter(p.Result.Revision), Issuer: "crewship", Durability: "node", Changes: changes}})
}
func TestFullCheckpointBasisRetainedAcrossHeadAdvanceAndRawDenied(t *testing.T) {
	s, lease, basis := fullBasisFixture(t)
	a := testBatchChange(t, lease.Key, "change-a", "", targetReadOp("a", "0"))
	op := targetReadOp("b", "0")
	op.Dependencies = []string{"a"}
	b := testBatchChange(t, lease.Key, "change-b", "", op)
	committer := sessionTestCommitter(t, s, lease, []BatchChange{a, b}, nil, fullTestOutput)
	if _, err := committer.Commit(t.Context(), sessionRequest(lease, a)); err == nil {
		t.Fatal("raw basis accepted by identity store")
	}
	for i, change := range []BatchChange{a, b} {
		r, e := committer.CommitFull(t.Context(), SessionFullBatchRequest{lease, basis, []BatchChange{change}})
		if e != nil || r.Result.Revision != strconv.Itoa(i+1) || r.Result.Changes[0].Status != "saved" {
			t.Fatal(r.Result, e)
		}
		var message protocol.SessionCommitResult
		if json.Unmarshal(r.EncodedOutput, &message) != nil || string(message.Checkpoint.Revision) != r.Result.Revision {
			t.Fatal("wrong output")
		}
	}
	retry, e := committer.CommitFull(t.Context(), SessionFullBatchRequest{lease, basis, []BatchChange{a}})
	if e != nil || retry.Result.Revision != "2" || retry.Result.Changes[0].Operations[0].Revision != "1" {
		t.Fatal(retry.Result, e)
	}
}
func TestFullCheckpointBasisOutputTamperAndCapacityRollBack(t *testing.T) {
	for _, mode := range []string{"digest", "creator", "state", "assets", "tuple", "target", "receipt", "request", "issuer", "raw-overflow", "canonical-overflow", "callback-error", "valid-copy"} {
		t.Run(mode, func(t *testing.T) {
			s, lease, basis := fullBasisFixture(t)
			change := testBatchChange(t, lease.Key, "change-a", "", targetReadOp("a", "0"))
			output := func(ctx context.Context, tx *sql.Tx, p SessionPendingOutput) ([]byte, error) {
				if mode == "callback-error" {
					return nil, errors.New("uncertain")
				}
				if mode == "raw-overflow" {
					return []byte(strings.Repeat(" ", sessionOutputBytes+1)), nil
				}
				raw, e := fullTestOutput(ctx, tx, p)
				if e != nil {
					return nil, e
				}
				if mode == "canonical-overflow" { // Valid JSON whose tiny exponent expands past the negotiated canonical cap.
					return []byte(`{"padding":"` + strings.Repeat("a", 1800000) + `","x":[` + strings.Repeat("1e20,", 26000) + `1e20]}`), nil
				}
				var m protocol.SessionCommitResult
				if e = json.Unmarshal(raw, &m); e != nil {
					return nil, e
				}
				switch mode {
				case "digest":
					m.Checkpoint.Digest = protocol.Digest(canonicalDigest([]byte("wrong")))
				case "creator":
					m.Checkpoint.Generation = "2"
				case "state":
					m.Checkpoint.State = "Mg=="
				case "assets":
					m.Checkpoint.Assets = []protocol.Asset{{AssetID: "foreign", Digest: protocol.Digest(canonicalDigest(nil)), SizeBytes: "0"}}
				case "tuple":
					m.Checkpoint.Versions.ModelVersion = "0.3.0"
				case "target":
					m.Targets[0].Revision = "0"
				case "receipt":
					m.Receipt.Changes[0].Operations[0] = json.RawMessage(`{"op_id":"a","status":"saved","revision":"2","issuer":"crewship","durability":"node"}`)
				case "request":
					m.Receipt.RequestID = "foreign"
				case "issuer":
					m.Receipt.Issuer = "foreign"
				case "valid-copy":
					p.FullCheckpoint.Assets = append(p.FullCheckpoint.Assets, protocol.Asset{})
					p.Targets[0].Revision = "999"
					p.Result.Changes[0].Operations[0].Revision = "999"
					p.Checkpoint[0] = '9'
				}
				return json.Marshal(m)
			}
			committer := sessionTestCommitter(t, s, lease, []BatchChange{change}, nil, output)
			r, e := committer.CommitFull(t.Context(), SessionFullBatchRequest{lease, basis, []BatchChange{change}})
			if mode == "valid-copy" {
				if e != nil || r.Result.Changes[0].Operations[0].Revision != "1" {
					t.Fatal(r.Result, e)
				}
				return
			}
			if e == nil || len(r.EncodedOutput) != 0 || batchCount(t, s, "mooring_collab_operations") != 0 || batchCount(t, s, "mooring_collab_checkpoints") != 1 {
				t.Fatal("invalid output committed", r.Result, e)
			}
		})
	}
}
func TestFullCheckpointBasisRejectsTransplantsAndUnknownMetadata(t *testing.T) {
	for _, mode := range []string{"content-digest", "wrong-full", "unknown-current", "wrong-original"} {
		t.Run(mode, func(t *testing.T) {
			s, lease, basis := fullBasisFixture(t)
			change := testBatchChange(t, lease.Key, "change-a", "", targetReadOp("a", "0"))
			if mode == "content-digest" {
				basis.Digest = canonicalDigest([]byte("0"))
			}
			if mode == "wrong-full" {
				basis.Digest = canonicalDigest([]byte("different"))
			}
			if mode == "unknown-current" || mode == "wrong-original" {
				// Adversarial database fixture, not an exported production mutation route.
				if _, e := s.db.Exec(`DROP TRIGGER mooring_checkpoint_metadata_immutable`); e != nil {
					t.Fatal(e)
				}
				metadata := "{}"
				if mode == "wrong-original" {
					metadata = `{"assets":[],"format_version":"foreign","versions":{"engine_version":"0.10.0","model_version":"0.2.0","protocol_version":"0.4.0"}}`
				}
				if _, e := s.db.Exec(`UPDATE mooring_collab_checkpoints SET checkpoint_metadata_json=?`, metadata); e != nil {
					t.Fatal(e)
				}
			}
			committer := sessionTestCommitter(t, s, lease, []BatchChange{change}, nil, fullTestOutput)
			if _, e := committer.CommitFull(t.Context(), SessionFullBatchRequest{lease, basis, []BatchChange{change}}); e == nil || batchCount(t, s, "mooring_collab_operations") != 0 {
				t.Fatal("transplant accepted", e)
			}
		})
	}
}
func TestRetainedCheckpointMetadataDecoderIsClosed(t *testing.T) {
	exact, e := canonicalCheckpointDescription(checkpointTestDescription(), 4096)
	if e != nil {
		t.Fatal(e)
	}
	decoded, e := decodeRetainedCheckpointDescription(exact, len(exact))
	if e != nil || !reflect.DeepEqual(decoded, checkpointTestDescription()) {
		t.Fatal(decoded, e)
	}
	for _, raw := range []string{strings.Replace(string(exact), "format_version", "Format_version", 1), strings.Replace(string(exact), `"assets":[]`, `"assets":null`, 1), strings.Replace(string(exact), `"assets":[],`, "", 1), string(exact) + " ", `{"unknown":true}`} {
		if _, e = decodeRetainedCheckpointDescription([]byte(raw), 4096); e == nil {
			t.Fatal("noncanonical/unknown metadata accepted")
		}
	}
	if _, e = decodeRetainedCheckpointDescription(exact, len(exact)-1); e == nil {
		t.Fatal("overbudget metadata accepted")
	}
}

func TestFullCheckpointBasisCreatorSurvivesActualOwnerTakeover(t *testing.T) {
	s, owner1, basis := fullBasisFixture(t)
	if e := s.config.Owners.Release(t.Context(), owner1); e != nil {
		t.Fatal(e)
	}
	owner2, e := s.config.Owners.Acquire(t.Context(), owner1.Key, "replacement-runtime")
	if e != nil || owner2.Generation != 2 {
		t.Fatal(owner2, e)
	}
	if e = s.transaction(t.Context(), func(tx *sql.Tx) error {
		cp, _, err := s.loadRetainedFullCheckpoint(t.Context(), tx, owner2.Key, "0", true)
		if err == nil && (cp.Generation != "1" || string(cp.Digest) != basis.Digest) {
			return ErrCheckpointIdentity
		}
		return err
	}); e != nil {
		t.Fatal("takeover relabeled creator", e)
	}
	change := testBatchChange(t, owner2.Key, "new-owner-change", "", targetReadOp("new-owner-op", "0"))
	committer := sessionTestCommitter(t, s, owner2, []BatchChange{change}, nil, fullTestOutput)
	r, e := committer.CommitFull(t.Context(), SessionFullBatchRequest{owner2, basis, []BatchChange{change}})
	if e != nil {
		t.Fatal(e)
	}
	var message protocol.SessionCommitResult
	if json.Unmarshal(r.EncodedOutput, &message) != nil || message.Checkpoint.Generation != "2" {
		t.Fatal("new checkpoint lacks actual creator", message.Checkpoint.Generation)
	}
}
func TestFullCheckpointBasisFinalFencesAfterOutputRollBack(t *testing.T) {
	for _, mode := range []string{"source-revoked", "owner-expired"} {
		t.Run(mode, func(t *testing.T) {
			s, lease, basis := fullBasisFixture(t)
			change := testBatchChange(t, lease.Key, "final-change", "", targetReadOp("final-op", "0"))
			denied := false
			write := func(SessionBatchAccess) error {
				if denied {
					return &BatchDenied{Code: "permission_revoked"}
				}
				return nil
			}
			output := func(ctx context.Context, tx *sql.Tx, p SessionPendingOutput) ([]byte, error) {
				raw, e := fullTestOutput(ctx, tx, p)
				if mode == "source-revoked" {
					denied = true
				} else {
					s.config.Owners.clock.(*ownerTestClock).advance(6 * time.Second)
				}
				return raw, e
			}
			committer := sessionTestCommitter(t, s, lease, []BatchChange{change}, write, output)
			if r, e := committer.CommitFull(t.Context(), SessionFullBatchRequest{lease, basis, []BatchChange{change}}); e == nil || len(r.EncodedOutput) != 0 || batchCount(t, s, "mooring_collab_operations") != 0 || batchCount(t, s, "mooring_collab_checkpoints") != 1 {
				t.Fatal("final fence did not roll back", e)
			}
		})
	}
}
func TestFullCheckpointBasisSealedAuthorityBeforeMetadataDecoder(t *testing.T) {
	s, lease, basis := fullBasisFixture(t)
	if _, e := s.db.Exec(`DROP TRIGGER mooring_checkpoint_metadata_immutable`); e != nil {
		t.Fatal(e)
	}
	if _, e := s.db.Exec(`UPDATE mooring_collab_checkpoints SET checkpoint_metadata_json='{}'`); e != nil {
		t.Fatal(e)
	}
	change := testBatchChange(t, lease.Key, "unauthorized-change", "", targetReadOp("unauthorized-op", "0"))
	called := false
	sentinel := errors.New("original ingress denied")
	committer, e := NewSessionCommitter(s, lease, func(context.Context, *sql.Tx, SessionBatchAccess) error { return sentinel }, func(context.Context, *sql.Tx, SessionPendingOutput) ([]byte, error) { called = true; return nil, nil })
	if e != nil {
		t.Fatal(e)
	}
	if _, e = committer.CommitFull(t.Context(), SessionFullBatchRequest{lease, basis, []BatchChange{change}}); !errors.Is(e, sentinel) || called {
		t.Fatal("decoder/output preceded original authority", e)
	}
}
func TestFullCheckpointBasisKnownOriginalCannotHideUnknownCurrentHead(t *testing.T) {
	s, lease, basis := fullBasisFixture(t)
	first := testBatchChange(t, lease.Key, "legacy-head", "", targetReadOp("a", "0"))
	s.config.CheckpointIdentity.Enabled = false
	if _, e := s.Commit(t.Context(), BatchRequest{lease, "0", []BatchChange{first}}); e != nil {
		t.Fatal(e)
	}
	s.config.CheckpointIdentity.Enabled = true
	op := targetReadOp("b", "0")
	op.Dependencies = []string{"a"}
	next := testBatchChange(t, lease.Key, "next-change", "", op)
	committer := sessionTestCommitter(t, s, lease, []BatchChange{next}, nil, fullTestOutput)
	if _, e := committer.CommitFull(t.Context(), SessionFullBatchRequest{lease, basis, []BatchChange{next}}); e == nil || batchCount(t, s, "mooring_collab_operations") != 1 {
		t.Fatal("unknown current interpretation admitted", e)
	}
}
