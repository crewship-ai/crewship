package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

func TestCapturedCompletionProjectsExactFinishedAtAndStableRetry(t *testing.T) {
	fixture := newRunsTestFixture(t)
	const run = "captured-completion-api"
	started := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	fixture.emitRunRow(t, run, "", "USER", started)
	handler := NewInternalHandler(fixture.h.db, "fixture", newTestLogger())
	writer := wireTestJournalForHandler(t, fixture.h.db, handler)
	var deliveries atomic.Int32
	writer.SetCommitObserver(func(entries []journal.Entry) {
		for _, entry := range entries {
			if entry.Type == journal.EntryRunCompleted {
				deliveries.Add(1)
			}
		}
	})
	complete := func() {
		request := httptest.NewRequest("PATCH", "/api/v1/internal/runs/"+run, strings.NewReader(`{"status":"COMPLETED","exit_code":0,"completed_at":"2026-10-19T10:00:42.125000000Z","metadata":{"captured_completed_at":"legacy arbitrary metadata"}}`))
		request.SetPathValue("runId", run)
		response := httptest.NewRecorder()
		handler.UpdateRun(response, request)
		if response.Code != 200 {
			t.Fatalf("complete: HTTP%d %s", response.Code, response.Body.String())
		}
	}
	complete()
	complete()
	request := httptest.NewRequest("GET", "/api/v1/runs/"+run, nil)
	request = request.WithContext(context.WithValue(request.Context(), ctxWorkspaceID, fixture.wsID))
	request.SetPathValue("id", run)
	response := httptest.NewRecorder()
	fixture.h.Get(response, request)
	var body runResponse
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &body) != nil || body.StartedAt == nil || body.FinishedAt == nil {
		t.Fatalf("run projection: HTTP%d %s", response.Code, response.Body.String())
	}
	finished, err := time.Parse(time.RFC3339Nano, *body.FinishedAt)
	if err != nil || *body.FinishedAt != "2026-10-19T10:00:42Z" || finished.Sub(started) != 42*time.Second {
		t.Fatalf("run finished at admission instead of captured exit: %q %v", *body.FinishedAt, err)
	}
	const terminal = "run-terminal:" + run
	var terminalTime string
	if err := fixture.h.db.QueryRow(`SELECT ts FROM journal_entries WHERE id=?`, terminal).Scan(&terminalTime); err != nil {
		t.Fatal(err)
	}
	exact, err := time.Parse(time.RFC3339Nano, terminalTime)
	if err != nil || exact.Sub(started) != 42125*time.Millisecond {
		t.Fatalf("durable completion duration changed: %s %v", terminalTime, err)
	}
	var fingerprint string
	if err := fixture.h.db.QueryRow(`SELECT fingerprint FROM journal_replay_receipts WHERE id=?`, terminal).Scan(&fingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.h.db.Exec(`DELETE FROM journal_entries WHERE id=?`, terminal); err != nil {
		t.Fatal(err)
	}
	complete()
	var after string
	if err := fixture.h.db.QueryRow(`SELECT fingerprint FROM journal_replay_receipts WHERE id=?`, terminal).Scan(&after); err != nil || after != fingerprint || deliveries.Load() != 1 {
		t.Fatalf("retained replay fingerprint or delivery changed: deliveries=%d %v", deliveries.Load(), err)
	}
}

func TestLegacyCompletionMetadataCannotChangeTimeOrReceipt(t *testing.T) {
	for _, metadata := range []string{`"2026-10-19T10:00:42.125Z"`, `123`} {
		t.Run(metadata, func(t *testing.T) {
			fixture := newRunsTestFixture(t)
			const run = "legacy-completion-api"
			fixture.emitRunRow(t, run, "", "USER", time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC))
			handler := NewInternalHandler(fixture.h.db, "fixture", newTestLogger())
			wireTestJournalForHandler(t, fixture.h.db, handler)
			complete := func() {
				request := httptest.NewRequest("PATCH", "/", strings.NewReader(`{"status":"COMPLETED","exit_code":0,"metadata":{"captured_completed_at":`+metadata+`}}`))
				request.SetPathValue("runId", run)
				response := httptest.NewRecorder()
				handler.UpdateRun(response, request)
				if response.Code != 200 {
					t.Fatalf("legacy update: %d %s", response.Code, response.Body.String())
				}
			}
			complete()
			var timestamp, payload, before, after string
			if err := fixture.h.db.QueryRow(`SELECT ts,payload FROM journal_entries WHERE id=?`, "run-terminal:"+run).Scan(&timestamp, &payload); err != nil {
				t.Fatal(err)
			}
			if timestamp != "2026-10-19T10:00:00.000Z" || !strings.Contains(payload, `"captured_completed_at":`+metadata) {
				t.Fatalf("legacy time/metadata changed: %s %s", timestamp, payload)
			}
			if err := fixture.h.db.QueryRow(`SELECT fingerprint FROM journal_replay_receipts WHERE id=?`, "run-terminal:"+run).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.h.db.Exec(`DELETE FROM journal_entries WHERE id=?`, "run-terminal:"+run); err != nil {
				t.Fatal(err)
			}
			complete()
			if err := fixture.h.db.QueryRow(`SELECT fingerprint FROM journal_replay_receipts WHERE id=?`, "run-terminal:"+run).Scan(&after); err != nil || before != after {
				t.Fatalf("legacy MAC changed: %v", err)
			}
		})
	}
}
