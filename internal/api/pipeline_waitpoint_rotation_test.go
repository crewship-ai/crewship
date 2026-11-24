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
