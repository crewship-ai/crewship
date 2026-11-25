package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// A callback must use the store's atomic decision method even when a
// separate identity lookup is available on the same store.
type atomicCallbackStore struct{ *pipeline.SQLWaitpointStore }

func (atomicCallbackStore) ResolveCallbackToken(context.Context, string) (string, string, error) {
	return "", "", errors.New("standalone lookup is not a decision")
}

func TestCallbackUsesAtomicDecision(t *testing.T) {
	h, user, ws := newPipelineHandlerForCRUDTest(t)
	store, id := seedPendingWaitpoint(t, h, ws)
	h.SetWaitpointStore(atomicCallbackStore{store})
	secret, _, err := store.MintCallbackToken(t.Context(), ws, id, user)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/", nil)
	req.SetPathValue("token", secret)
	rr := httptest.NewRecorder()
	h.CompleteWaitpointToken(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("atomic callback returned HTTP %d", rr.Code)
	}
}

func TestCallbackMintAuditFailureRollsBack(t *testing.T) {
	h, user, ws := newPipelineHandlerForCRUDTest(t)
	_, id := seedPendingWaitpoint(t, h, ws)
	_, err := h.db.Exec(`CREATE TRIGGER reject_mint BEFORE INSERT ON audit_logs WHEN NEW.action='waitpoint.callback_token.minted' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	if err != nil {
		t.Fatal(err)
	}
	rr := mintWaitpointCallbackAs(t, h, user, ws, "MANAGER", id)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("HTTP %d, want 500", rr.Code)
	}
	var empty bool
	if err := h.db.QueryRow("SELECT callback_token_hash IS NULL FROM pipeline_waitpoints WHERE token=?", id).Scan(&empty); err != nil || !empty {
		t.Fatalf("digest changed despite missing audit: %v", err)
	}
}

func TestCallbackConcurrentMintReceipts(t *testing.T) {
	h, user, ws := newPipelineHandlerForCRUDTest(t)
	store, id := seedPendingWaitpoint(t, h, ws)
	type grant struct {
		secret  string
		rotated bool
		err     error
	}
	start := make(chan struct{})
	grants := make(chan grant, 2)
	for range 2 {
		go func() {
			<-start
			secret, rotated, err := store.MintCallbackToken(t.Context(), ws, id, user)
			grants <- grant{secret, rotated, err}
		}()
	}
	close(start)
	first, second := <-grants, <-grants
	if first.err != nil || second.err != nil || first.rotated == second.rotated {
		t.Fatalf("expected one mint and one rotation: %t (%v), %t (%v)", first.rotated, first.err, second.rotated, second.err)
	}
	if first.rotated {
		first, second = second, first
	}
	var receipts, rotations int
	err := h.db.QueryRow(`SELECT count(*),sum(json_extract(metadata,'$.rotated')) FROM audit_logs
		WHERE action='waitpoint.callback_token.minted' AND entity_id=? AND user_id=? AND workspace_id=?`, id, user, ws).Scan(&receipts, &rotations)
	if err != nil || receipts != 2 || rotations != 1 {
		t.Fatalf("receipts=%d rotations=%d (%v)", receipts, rotations, err)
	}
	if _, _, err := store.DecideCallback(t.Context(), first.secret, true, ""); !errors.Is(err, pipeline.ErrAlreadyDecided) {
		t.Fatalf("retired grant: %v", err)
	}
	if _, _, err := store.DecideCallback(t.Context(), second.secret, true, ""); err != nil {
		t.Fatal(err)
	}
}

func TestCallbackChainFailurePreservesPreviousGrant(t *testing.T) {
	h, user, ws := newPipelineHandlerForCRUDTest(t)
	store, id := seedPendingWaitpoint(t, h, ws)
	previous, _, err := store.MintCallbackToken(t.Context(), ws, id, user)
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.db.Exec(`CREATE TRIGGER reject_grant_chain BEFORE INSERT ON journal_entries
		WHEN json_extract(NEW.payload,'$.action')='waitpoint.callback_token.minted'
		BEGIN SELECT RAISE(ABORT,'journal unavailable'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if secret, _, err := store.MintCallbackToken(t.Context(), ws, id, user); err == nil || secret != "" {
		t.Error("rotation returned a grant without its chained receipt")
	}
	if active, _, err := store.ResolveCallbackToken(t.Context(), previous); err != nil || active != id {
		t.Errorf("previous grant lost: %v", err)
	}
	for _, q := range []string{
		`SELECT count(*) FROM audit_logs WHERE action='waitpoint.callback_token.minted' AND entity_id=?`,
		`SELECT count(*) FROM journal_entries WHERE json_extract(payload,'$.action')='waitpoint.callback_token.minted' AND json_extract(refs,'$.entity_id')=?`,
	} {
		var count int
		if err := h.db.QueryRow(q, id).Scan(&count); err != nil || count != 1 {
			t.Errorf("committed receipts=%d (%v)", count, err)
		}
	}
}
