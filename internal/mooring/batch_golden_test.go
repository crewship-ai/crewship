package mooring

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"testing"
)

func TestBatchSharedResolutionVectors(t *testing.T) {
	raw, err := os.ReadFile("../../mooring/protocol/batch-resolution-fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	type failure struct {
		ID   string `json:"op_id"`
		Code string `json:"code"`
	}
	var fixture struct {
		Cases []struct {
			ID      string `json:"id"`
			Changes []struct {
				ID         string `json:"change_id"`
				Operations []struct {
					ID           string   `json:"op_id"`
					Dependencies []string `json:"dependencies"`
				} `json:"operations"`
			} `json:"changes"`
			Direct   []failure `json:"direct_failures"`
			Prior    []failure `json:"prior_outcomes"`
			Adapter  []failure `json:"adapter_failures"`
			Known    []string  `json:"known_change_ids"`
			Expected []struct {
				ID       string `json:"op_id"`
				Status   string `json:"status"`
				Code     string `json:"code"`
				Root     string `json:"root_op_id"`
				RootCode string `json:"root_code"`
			} `json:"expected"`
		} `json:"cases"`
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Cases) != 11 {
		t.Fatal("shared fixture cases unexpectedly changed")
	}
	store, adapter, owners, _, original := batchFixture(t)
	for number, tc := range fixture.Cases {
		t.Run(tc.ID, func(t *testing.T) {
			key := original.Key
			key.ArtifactID = fmt.Sprintf("golden-%d", number)
			if _, err := store.db.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES(?,'w',?,'Golden','u','{}')`, key.ArtifactID, key.ArtifactID); err != nil {
				t.Fatal(err)
			}
			lease, err := owners.Acquire(t.Context(), key, "runtime")
			if err != nil {
				t.Fatal(err)
			}
			store.config.Authority = func(context.Context, *sql.Tx, BatchAccess) error { return nil }
			adapter.apply = nil
			if err = store.Initialize(t.Context(), lease, []byte("0")); err != nil {
				t.Fatal(err)
			}
			changes := []BatchChange{}
			for _, c := range tc.Changes {
				ops := []BatchOperation{}
				for _, op := range c.Operations {
					ops = append(ops, testBatchOp(op.ID, op.Dependencies...))
				}
				changes = append(changes, testBatchChange(t, key, c.ID, "", ops...))
			}
			for _, prior := range tc.Prior {
				c := testBatchChange(t, key, "prior-"+prior.ID, "", testBatchOp(prior.ID))
				adapter.apply = func(_ context.Context, _ []byte, c BatchChange) ([]byte, *PreparedFailure, error) {
					return nil, &PreparedFailure{c.Operations[0].ID, batchReason(prior.Code)}, nil
				}
				if _, err = store.Commit(t.Context(), BatchRequest{lease, "0", []BatchChange{c}}); err != nil {
					t.Fatal(err)
				}
			}
			adapter.apply = nil
			for _, known := range tc.Known {
				found := false
				for _, c := range changes {
					if c.ID == known {
						found = true
						if _, err = store.Commit(t.Context(), BatchRequest{lease, "0", []BatchChange{c}}); err != nil {
							t.Fatal(err)
						}
					}
				}
				if !found {
					t.Fatal("unknown known_change_id in fixture")
				}
			}
			direct := map[string]string{}
			for _, f := range tc.Direct {
				direct[f.ID] = f.Code
			}
			store.config.Authority = func(_ context.Context, _ *sql.Tx, a BatchAccess) error {
				if a.Intent == "write" && a.Operation != nil {
					if code := direct[a.Operation.ID]; code != "" {
						return &BatchDenied{code}
					}
				}
				return nil
			}
			adapter.apply = func(_ context.Context, state []byte, c BatchChange) ([]byte, *PreparedFailure, error) {
				for _, op := range c.Operations {
					for _, f := range tc.Adapter {
						if f.ID == op.ID {
							return nil, &PreparedFailure{op.ID, batchReason(f.Code)}, nil
						}
					}
				}
				var n int
				if err := json.Unmarshal(state, &n); err != nil {
					return nil, nil, err
				}
				return []byte(strconv.Itoa(n + len(c.Operations))), nil, nil
			}
			result, err := store.Commit(t.Context(), BatchRequest{lease, "0", changes})
			if err != nil {
				t.Fatal(err)
			}
			observed := map[string]BatchOperationResult{}
			for _, c := range result.Changes {
				for _, op := range c.Operations {
					observed[op.ID] = op
				}
			}
			if len(observed) != len(tc.Expected) {
				t.Fatal("golden result count differs")
			}
			for _, want := range tc.Expected {
				got, ok := observed[want.ID]
				if !ok || got.Status != want.Status {
					t.Fatalf("%s status %+v want%s", want.ID, got, want.Status)
				}
				if want.Status == "saved" {
					if got.Reason != nil {
						t.Fatal("saved has rejection")
					}
					continue
				}
				if got.Reason == nil {
					t.Fatal("missing rejection")
				}
				root, rootCode := got.ID, got.Reason.Code
				if got.Reason.Code == "dependency_rejected" {
					if got.Reason.RootCause == nil {
						t.Fatal("missing original cause")
					}
					root, rootCode = got.Reason.RootOperationID, got.Reason.RootCause.Code
				}
				if got.Reason.Code != want.Code || root != want.Root || rootCode != want.RootCode {
					t.Fatalf("%s reason%+v actualroot%s/%s want%s/%s/%s", want.ID, got.Reason, root, rootCode, want.Code, want.Root, want.RootCode)
				}
			}
		})
	}
}

func TestBatchAmbiguousPreparedFailureIsNonterminal(t *testing.T) {
	store, adapter, _, _, lease := batchFixture(t)
	for _, id := range []string{"", "foreign"} {
		c := testBatchChange(t, lease.Key, "ambiguous-"+id, "", testBatchOp("actual-"+id))
		adapter.apply = func(context.Context, []byte, BatchChange) ([]byte, *PreparedFailure, error) {
			return nil, &PreparedFailure{OperationID: id, Reason: batchReason("target_deleted")}, nil
		}
		result, err := store.Commit(t.Context(), BatchRequest{lease, "0", []BatchChange{c}})
		if err != ErrInvalid || len(result.Changes) != 0 || batchCount(t, store, "mooring_collab_operations") != 0 {
			t.Fatalf("ambiguous engine failure persisted: %+v %v", result, err)
		}
	}
}

func TestBatchMalformedStoredResultFailsClosed(t *testing.T) {
	store, _, _, _, lease := batchFixture(t)
	c := testBatchChange(t, lease.Key, "stored", "", testBatchOp("stored"))
	if _, err := store.Commit(t.Context(), BatchRequest{lease, "0", []BatchChange{c}}); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"op_id":"stored","status":"rejected"}`, `{"op_id":"stored","status":"saved","revision":"18446744073709551616","issuer":"crewship","durability":"node"}`} {
		if _, err := store.db.Exec(`UPDATE mooring_collab_operations SET result_json=? WHERE op_id='stored'`, raw); err != nil {
			t.Fatal(err)
		}
		if result, err := store.Status(t.Context(), lease.Key, "stored", c.Operations[0].Fingerprint); err != ErrInvalid || result != nil {
			t.Fatal("malformed durable result trusted", result, err)
		}
	}
}

func TestBatchIdentityConflictKeepsIndependentDraftPending(t *testing.T) {
	store, adapter, _, _, lease := batchFixture(t)
	emitted := 0
	store.config.AfterCommit = func(BatchCommitEvent) { emitted++ }
	original := testBatchChange(t, lease.Key, "original", "", testBatchOp("same-id"))
	if _, err := store.Commit(t.Context(), BatchRequest{lease, "0", []BatchChange{original}}); err != nil {
		t.Fatal(err)
	}
	modified := testBatchOp("same-id")
	modified.Payload = json.RawMessage(`{"value":2}`)
	conflict := testBatchChange(t, lease.Key, "different-change", "", modified)
	independent := testBatchChange(t, lease.Key, "independent", "", testBatchOp("independent"))
	result, err := store.Commit(t.Context(), BatchRequest{lease, "1", []BatchChange{conflict, independent}})
	if err != ErrOperationConflict || len(result.Changes) != 0 || emitted != 1 || adapter.calls.Load() != 1 {
		t.Fatal("conflict admitted fresh work", result, err, emitted, adapter.calls.Load())
	}
	pending, err := store.Status(t.Context(), lease.Key, "independent", independent.Operations[0].Fingerprint)
	if err != nil || pending != nil {
		t.Fatal("unadmitted independent draft became terminal", pending, err)
	}
	old, err := store.Status(t.Context(), lease.Key, "same-id", original.Operations[0].Fingerprint)
	if err != nil || old == nil || old.Status != "saved" || old.Revision != "1" {
		t.Fatal("conflict changed original ledger", old, err)
	}
	// Retrying the untouched immutable independent change separately is safe.
	result, err = store.Commit(t.Context(), BatchRequest{lease, "1", []BatchChange{independent}})
	if err != nil || result.Revision != "2" || result.Changes[0].Status != "saved" || emitted != 2 {
		t.Fatal("pending draft could not exact-retry independently", result, err)
	}
}
