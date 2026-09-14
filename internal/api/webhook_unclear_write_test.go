package api

import (
	"context"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/dispatch"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/webhook"
)

// "The run record could not be written" is not the same fact as "the run record
// does not exist", and only the second one is safe to repeat.
//
// A write that returns an error may well have landed — the row is there and the
// response was lost. Retrying on the error alone would produce a second record
// for one attempt, and then a second agent turn behind it. The run id is stable
// precisely so this question can be ASKED rather than assumed, and these tests
// pin all three answers: absent, present, and unreadable.
//
// The lookup itself is easy to get silently wrong: the first version asked
// `agent_runs`, a table unified-journal phase J removed. Every call errored, so
// every unclear write reported "could not be established" — fail-safe by
// accident, and a rule that never once said "yes" is not a rule.

// failingCreateRunResolver reports failure from CreateRun while the test
// controls whether the record is actually there.
type failingCreateRunResolver struct {
	fakeChatResolver
	err error
}

func (r *failingCreateRunResolver) CreateRun(_ context.Context, _, _, _, _, _ string, _ map[string]interface{}) error {
	return r.err
}

func TestRunRecordAbsent_AnswersTheQuestionItIsAskedAboutTheRealRecord(t *testing.T) {
	db := setupTestDB(t)
	wsID := seedTestWorkspace(t, db, seedTestUser(t, db))
	h := NewWebhookHandler(db, newTestLogger(), &fakeChatResolver{}, nil, nil, nil, nil)
	ctx := context.Background()

	// Nothing written: a confirmed absence.
	absent, err := h.runRecordAbsent(ctx, "run-never-written")
	if err != nil {
		t.Fatalf("lookup of an unwritten run: %v — an error here makes the rule unusable, "+
			"because 'could not be established' is never a retry", err)
	}
	if !absent {
		t.Error("a run nothing ever wrote is reported as present")
	}

	// The record IS the run.started journal entry, traced by the run id.
	if _, err := db.Exec(`
		INSERT INTO journal_entries (id, workspace_id, ts, entry_type, severity, actor_type, summary, trace_id)
		VALUES ('je-1', ?, '2026-09-11T00:00:00.000Z', ?, 'info', 'sidecar', 'run started', 'run-written')`,
		wsID, string(journal.EntryRunStarted)); err != nil {
		t.Fatalf("seed the run record: %v", err)
	}
	absent, err = h.runRecordAbsent(ctx, "run-written")
	if err != nil {
		t.Fatalf("lookup of a written run: %v", err)
	}
	if absent {
		t.Error("a run whose record exists is reported as absent — a retry would write a second one")
	}

	// A different entry type under the same trace is not a run record.
	if _, err := db.Exec(`
		INSERT INTO journal_entries (id, workspace_id, ts, entry_type, severity, actor_type, summary, trace_id)
		VALUES ('je-2', ?, '2026-09-11T00:00:01.000Z', 'llm.call', 'info', 'sidecar', 'thinking', 'run-other')`,
		wsID); err != nil {
		t.Fatalf("seed a non-run entry: %v", err)
	}
	absent, err = h.runRecordAbsent(ctx, "run-other")
	if err != nil {
		t.Fatal(err)
	}
	if !absent {
		t.Error("an unrelated journal entry was mistaken for a run record")
	}
}

// And the classification that rests on it: only a confirmed absence is
// retryable.
func TestWebhookRun_AnUnclearRunRecordWriteIsNotRetried(t *testing.T) {
	setTestEncryptionKey(t)

	newHandler := func(t *testing.T) (*WebhookHandler, string) {
		t.Helper()
		db := setupTestDB(t)
		wsID := seedTestWorkspace(t, db, seedTestUser(t, db))
		resolver := &failingCreateRunResolver{err: errors.New("the write timed out")}
		h := NewWebhookHandler(db, newTestLogger(), resolver, nil, nil, verticalContainer{}, nil)
		return h, wsID
	}

	info := func(wsID string) *chatbridge.ChatInfo {
		return &chatbridge.ChatInfo{
			AgentID: "agent-1", AgentSlug: "ag", CrewID: "crew-1", CrewSlug: "c", WorkspaceID: wsID,
		}
	}
	payload := webhook.WebhookPayload{Event: "deploy", Source: "gh"}

	t.Run("a confirmed absence is retryable", func(t *testing.T) {
		h, wsID := newHandler(t)
		rt := NewWebhookRuntime(h)
		err := h.runWebhookAgent(context.Background(), info(wsID), "agent-1", "run-absent", payload, nil, nil)
		if err == nil {
			t.Fatal("the run reported success although its record was never written")
		}
		if got := rt.Classify(dispatch.Assignment{}, err); got != dispatch.OutcomeRetryable {
			t.Errorf("outcome = %v, want retryable — nothing was created, so a retry repeats nothing", got)
		}
	})

	t.Run("a record that exists makes the outcome unclear", func(t *testing.T) {
		h, wsID := newHandler(t)
		if _, err := h.db.Exec(`
			INSERT INTO journal_entries (id, workspace_id, ts, entry_type, severity, actor_type, summary, trace_id)
			VALUES ('je-x', ?, '2026-09-11T00:00:00.000Z', ?, 'info', 'sidecar', 'run started', 'run-present')`,
			wsID, string(journal.EntryRunStarted)); err != nil {
			t.Fatalf("seed the run record: %v", err)
		}
		rt := NewWebhookRuntime(h)
		err := h.runWebhookAgent(context.Background(), info(wsID), "agent-1", "run-present", payload, nil, nil)
		if err == nil {
			t.Fatal("the run reported success although CreateRun failed")
		}
		if got := rt.Classify(dispatch.Assignment{}, err); got != dispatch.OutcomeUnclear {
			t.Errorf("outcome = %v, want unclear — the record is there, so a retry would make a second", got)
		}
	})
}
