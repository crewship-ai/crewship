package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/slides"
	"github.com/crewship-ai/crewship/internal/ws"
)

func admitSelectedHTTP(t *testing.T, f *instanceFixture, base string, input slides.SelectedRequestInput) string {
	t.Helper()
	response := f.slideDo(f.boss, "POST", base+"/requests", slideJSON(t, input))
	wantCode(t, response, 202, "admit request")
	var admitted SlidesSelectedAdmissionResponse
	if err := json.Unmarshal(response.Body.Bytes(), &admitted); err != nil || admitted.ID == "" {
		t.Fatal(response.Body.String(), err)
	}
	return admitted.ID
}

func TestSlidesSelectedWorkerStartupSnapshotPreservesFreshIntakeAndCannotSucceedFromText(t *testing.T) {
	f, base, input := selectedSlidesHTTPFixture(t)
	old := admitSelectedHTTP(t, f, base, input)
	worker := NewPageHandler(f.db, nil, newTestLogger())
	if worker.slidesStartupError != nil || len(worker.slidesStartupRequests) != 1 || worker.slidesStartupRequests[0].ID != old {
		t.Fatal("startup snapshot not captured before fresh intake", worker.slidesStartupError, worker.slidesStartupRequests)
	}
	input.SubmissionID = "fresh_after_construction"
	fresh := admitSelectedHTTP(t, f, base, input)
	if err := worker.recoverSelectedStartupRequests(t.Context()); err != nil {
		t.Fatal(err)
	}
	var oldStatus, freshStatus string
	if err := f.db.QueryRow(`SELECT status FROM slides_requests WHERE id=?`, old).Scan(&oldStatus); err != nil || oldStatus != "interrupted" {
		t.Fatal(oldStatus, err)
	}
	if err := f.db.QueryRow(`SELECT status FROM slides_requests WHERE id=?`, fresh).Scan(&freshStatus); err != nil || freshStatus != "queued" {
		t.Fatal("fresh intake was interrupted by first tick", freshStatus, err)
	}
	calls := 0
	worker.artifactDispatch = func(_ context.Context, user, chat, prompt string, opts ws.ChatMessageOption) error {
		calls++
		if user != "boss" || chat != input.ChatID || opts.SlidesRequestID != fresh || opts.HumanAuthority == nil || !opts.HumanResume || opts.ArtifactRequestID != "" || opts.DocumentRequestID != "" || opts.BoardRequestID != "" || !strings.Contains(prompt, "crewship slide agent read") || !strings.Contains(prompt, "crewship slide agent replace") || strings.Contains(prompt, f.boss) {
			t.Error("trusted selected-only handoff lost", user, chat, opts)
		}
		return nil // Model prose/completion alone is deliberately insufficient.
	}
	worker.dispatchNextSelectedSlidesRequest(t.Context())
	worker.dispatchNextSelectedSlidesRequest(t.Context())
	if err := worker.recoverSelectedStartupRequests(t.Context()); err != nil {
		t.Fatal(err)
	}
	var status string
	var version, head int
	if err := f.db.QueryRow(`SELECT status,version FROM slides_requests WHERE id=?`, fresh).Scan(&status, &version); err != nil || status != "failed" || version != 3 || calls != 1 {
		t.Fatal("dispatch text manufactured success or duplicate execution", status, version, calls, err)
	}
	if err := f.db.QueryRow(`SELECT head_rev FROM slides_heads WHERE page_id=?`, input.PageID).Scan(&head); err != nil || head != 1 {
		t.Fatal("startup/worker metadata changed content", head, err)
	}
}

func TestSlidesSelectedWorkerRevokedCredentialAndStaleHeadTerminalizeWithoutDispatch(t *testing.T) {
	for _, cause := range []string{"credential", "head"} {
		t.Run(cause, func(t *testing.T) {
			f, base, input := selectedSlidesHTTPFixture(t)
			id := admitSelectedHTTP(t, f, base, input)
			calls := 0
			f.r.pages.artifactDispatch = func(context.Context, string, string, string, ws.ChatMessageOption) error { calls++; return nil }
			if cause == "credential" {
				mustExec(t, f.db, `UPDATE cli_tokens SET revoked_at='2026-01-01T00:00:00Z' WHERE token_hash=?`, sha256Hex(f.boss))
			} else {
				var state slides.State
				response := f.slideDo(f.boss, "GET", base, "")
				if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
					t.Fatal(err)
				}
				notes := "Concurrent human change before dispatch"
				hash, _ := slides.SlideHash(state.Deck.Slides[0])
				patch := slides.PatchRequest{PageID: input.PageID, SchemaVersion: 1, BaseRev: 1, MutationID: "human_before_dispatch", Operations: []slides.Operation{{Op: "set_notes", SlideID: state.Deck.Slides[0].ID, IfHash: hash, Notes: &notes}}}
				wantCode(t, f.slideDo(f.boss, "POST", base+"/patch", slideJSON(t, patch)), 200, "intervening human edit")
			}
			f.r.pages.dispatchNextSelectedSlidesRequest(t.Context())
			var status string
			var version int
			if err := f.db.QueryRow(`SELECT status,version FROM slides_requests WHERE id=?`, id).Scan(&status, &version); err != nil || status != "failed" || version != 2 || calls != 0 || f.r.pages.slidesRequestsUnavailable.Load() {
				t.Fatal("unsafe or stuck rejected dispatch", status, version, calls, err)
			}
		})
	}
}

func TestSlidesSelectedWorkerActualHumanCancelStopsContextWithoutResurrection(t *testing.T) {
	f, base, input := selectedSlidesHTTPFixture(t)
	id := admitSelectedHTTP(t, f, base, input)
	entered := make(chan struct{})
	f.r.pages.artifactDispatch = func(ctx context.Context, _, _, _ string, opts ws.ChatMessageOption) error {
		if opts.SlidesRequestID != id {
			t.Error("wrong request")
		}
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}
	done := make(chan struct{})
	go func() { defer close(done); f.r.pages.dispatchNextSelectedSlidesRequest(t.Context()) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not enter dispatch")
	}
	cancel := slides.CancelSelectedRequestInput{PageID: input.PageID, SchemaVersion: 1, MutationID: "human_cancel", IfVersion: 2}
	wantCode(t, f.slideDo(f.boss, "POST", base+"/requests/"+id+"/cancel", slideJSON(t, cancel)), 200, "actual cancellation")
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not stop owned dispatch context")
	}
	var status string
	var version int
	if err := f.db.QueryRow(`SELECT status,version FROM slides_requests WHERE id=?`, id).Scan(&status, &version); err != nil || status != "cancelled" || version != 3 {
		t.Fatal("worker cleanup changed explicit terminal outcome", status, version, err)
	}
}

func TestSlidesSelectedStartupRecoveryFencesCapturedVersion(t *testing.T) {
	f, base, input := selectedSlidesHTTPFixture(t)
	id := admitSelectedHTTP(t, f, base, input)
	worker := NewPageHandler(f.db, nil, newTestLogger())
	key := slides.Key{WorkspaceID: "ws-old", PageID: input.PageID}
	h := &slideHTTP{pages: f.r.pages}
	authorize := func(ctx context.Context, tx *sql.Tx) (string, error) {
		record, err := slides.ReadSelectedAuthorityTx(ctx, tx, key, id)
		if err != nil {
			return "", err
		}
		if _, err = h.selectedAdmissionLive(ctx, tx, key, record.Admission); err != nil {
			return "", err
		}
		return record.Summary.HumanActor, nil
	}
	if _, err := f.r.pages.newSlidesStore().ClaimSelectedRequest(t.Context(), key, id, authorize); err != nil {
		t.Fatal(err)
	}
	if err := worker.recoverSelectedStartupRequests(t.Context()); err == nil {
		t.Fatal("startup cleanup ignored changed captured version")
	}
	var status string
	var version int
	if err := f.db.QueryRow(`SELECT status,version FROM slides_requests WHERE id=?`, id).Scan(&status, &version); err != nil || status != "running" || version != 2 {
		t.Fatal("cleanup interrupted changed active owner", status, version, err)
	}
}
