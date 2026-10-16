package slides

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestSelectedRequestHumanEditBetweenClaimAndBindDeniesDelegation(t *testing.T) {
	store, key, input, admission := selectedAdmissionFixture(t)
	ctx := t.Context()
	if _, err := store.AdmitSelectedRequest(ctx, key, "claim_then_edit", input, admission, storeAuthority); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimSelectedRequest(ctx, key, "claim_then_edit", storeAuthority); err != nil {
		t.Fatal(err)
	}
	state := currentState(t, store, key)
	notes := "Human edit before run admission"
	hash, _ := SlideHash(state.Deck.Slides[0])
	patch := PatchRequest{PageID: key.PageID, SchemaVersion: state.Deck.SchemaVersion, BaseRev: state.Revision, MutationID: "human_before_bind", Operations: []Operation{{Op: "set_notes", SlideID: state.Deck.Slides[0].ID, IfHash: hash, Notes: &notes}}}
	if _, err := store.Patch(ctx, key, patch, storeAuthority); err != nil {
		t.Fatal(err)
	}
	called := false
	err := store.BindSelectedRun(ctx, key, "claim_then_edit", "run_after_edit", storeAuthority, func(context.Context, *sql.Tx, SelectedAuthorityState) error { called = true; return nil })
	if !errors.Is(err, ErrConflict) || called {
		t.Fatal("stale run/delegation admitted", called, err)
	}
	detail, err := store.ReadSelectedRequest(ctx, key, "claim_then_edit", storeAuthority)
	if err != nil || detail.Request.OriginalRunID != "" || detail.Request.Version != 2 {
		t.Fatal("failed binding mutated history", detail, err)
	}
	if err = store.SettleSelectedRequest(ctx, key, "claim_then_edit", "failed", "Original selection changed before run admission", storeAuthority); err != nil {
		t.Fatal(err)
	}
	if currentState(t, store, key).Revision != 2 {
		t.Fatal("metadata cleanup changed content")
	}
}

func TestSelectedRequestLifecycleOwnedResultReplayAndIdentityRetention(t *testing.T) {
	store, key, input, admission := selectedAdmissionFixture(t)
	ctx := t.Context()
	const id, run = "request_owned", "original_run"
	if _, err := store.AdmitSelectedRequest(ctx, key, id, input, admission, storeAuthority); err != nil {
		t.Fatal(err)
	}
	original := currentState(t, store, key)
	claimed, err := store.ClaimSelectedRequest(ctx, key, id, storeAuthority)
	if err != nil || claimed.Summary.Status != "running" || claimed.Summary.Version != 2 {
		t.Fatal(claimed, err)
	}
	if _, err = store.ClaimSelectedRequest(ctx, key, id, storeAuthority); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate dispatch accepted", err)
	}
	rejected := errors.New("delegation refused")
	if err = store.BindSelectedRun(ctx, key, id, run, storeAuthority, func(context.Context, *sql.Tx, SelectedAuthorityState) error { return rejected }); !errors.Is(err, rejected) {
		t.Fatal(err)
	}
	detail, err := store.ReadSelectedRequest(ctx, key, id, storeAuthority)
	if err != nil || detail.Request.OriginalRunID != "" || detail.Request.Version != 2 {
		t.Fatal("binding failure did not roll back", detail, err)
	}
	bind := func(_ context.Context, _ *sql.Tx, r SelectedAuthorityState) error {
		if r.RunID != run || r.Summary.OriginalRunID != run || r.Admission != admission {
			t.Fatal("original binding changed", r)
		}
		return nil
	}
	for range 2 {
		if err = store.BindSelectedRun(ctx, key, id, run, storeAuthority, bind); err != nil {
			t.Fatal(err)
		}
	}
	if err = store.BindSelectedRun(ctx, key, id, "other_run", storeAuthority, bind); !errors.Is(err, ErrConflict) {
		t.Fatal("run rebound", err)
	}
	if err = store.SettleSelectedRequest(ctx, key, id, "succeeded", "", storeAuthority); !errors.Is(err, ErrConflict) {
		t.Fatal("success without result", err)
	}
	patch, err := SelectedReplacementPatch(original, key.PageID, id, input.BaseRev, claimed.Verified, "Upravený výběr")
	if err != nil {
		t.Fatal(err)
	}
	agentAuthority := func(context.Context, *sql.Tx) (string, error) { return "agent:selected_agent:" + run, nil }
	if _, err = store.PatchWithReceipt(ctx, key, patch, agentAuthority, SelectedResultGuard(key, id, "wrong_run")); !errors.Is(err, ErrConflict) {
		t.Fatal("wrong result owner accepted", err)
	}
	if currentState(t, store, key).Revision != 1 {
		t.Fatal("rejected result committed content")
	}
	result, err := store.PatchWithReceipt(ctx, key, patch, agentAuthority, SelectedResultGuard(key, id, run))
	if err != nil || result.Revision != 2 {
		t.Fatal(result, err)
	}
	for range 2 {
		if err = store.SettleSelectedRequest(ctx, key, id, "succeeded", "", storeAuthority); err != nil {
			t.Fatal(err)
		}
	}
	detail, err = store.ReadSelectedRequest(ctx, key, id, storeAuthority)
	if err != nil || detail.Request.Version != 5 || len(detail.History) != 5 || detail.Request.ResultRevision == nil || *detail.Request.ResultRevision != 2 || detail.Request.ResultHash != result.ContentHash {
		t.Fatal("not exactly one owned result/terminal event", detail, err)
	}
	state := currentState(t, store, key)
	notes := "Later human revision"
	hash, _ := SlideHash(state.Deck.Slides[0])
	human := PatchRequest{PageID: key.PageID, SchemaVersion: state.Deck.SchemaVersion, BaseRev: state.Revision, MutationID: "later_human", Operations: []Operation{{Op: "set_notes", SlideID: state.Deck.Slides[0].ID, IfHash: hash, Notes: &notes}}}
	if _, err = store.Patch(ctx, key, human, storeAuthority); err != nil {
		t.Fatal(err)
	}
	replay, err := store.PatchWithReceipt(ctx, key, patch, agentAuthority, SelectedResultGuard(key, id, run))
	if err != nil || !replay.Duplicate || replay.Revision != 2 || replay.ContentHash != result.ContentHash || currentState(t, store, key).Revision != 3 {
		t.Fatal("replay moved checkpoint to current head", replay, err)
	}
	deny := func(context.Context, *sql.Tx) (string, error) { return "", errors.New("original grant revoked") }
	if _, err = store.PatchWithReceipt(ctx, key, patch, deny, SelectedResultGuard(key, id, run)); err == nil {
		t.Fatal("revoked receipt replay accepted")
	}
	for _, rev := range []int{1, 2} {
		if _, err = store.db.Exec(`DELETE FROM slides_revisions WHERE workspace_id=? AND page_id=? AND rev=?`, key.WorkspaceID, key.PageID, rev); err == nil {
			t.Fatal("pinned revision deleted", rev)
		}
	}
	for _, query := range []string{`DELETE FROM chats WHERE id='selected_chat'`, `DELETE FROM agents WHERE id='selected_agent'`} {
		if _, err = store.db.Exec(query); err != nil {
			t.Fatal(err)
		}
		detail, err = store.ReadSelectedRequest(ctx, key, id, storeAuthority)
		if err != nil || detail.Request.ResultHash != result.ContentHash || detail.Request.AgentActor != "agent:selected_agent" || detail.Request.OriginalRunID != run || len(detail.History) != 5 {
			t.Fatal("identity deletion lost retained result/history", detail, err)
		}
	}
	if _, err = store.db.Exec(`DELETE FROM pages WHERE id=?`, key.PageID); err != nil {
		t.Fatal("whole Page cascade blocked by request pins", err)
	}
	for _, table := range []string{"slides_heads", "slides_revisions", "slides_requests", "slides_request_events", "slides_request_receipts"} {
		var count int
		if err = store.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatal("Page-owned graph retained", table, count, err)
		}
	}
}
