package pipeline

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// F1: a refused waitpoint decision is an authorization event. It lands in the
// chained journal in the same transaction as its Settings Audit row, carries
// the attempted and found state, and never names the waitpoint's callback
// token in the journal.
func TestWaitpointRefusalAuditIsChainedAndAtomic(t *testing.T) {
	for _, fault := range []string{"healthy", "journal_abort"} {
		t.Run(fault, func(t *testing.T) {
			db := testutil.MigratedSQLDB(t)
			const ws = "ws-f1-refusal"
			if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'F1', 'f1')`, ws); err != nil {
				t.Fatal(err)
			}
			key := []byte("waitpoint-refusal-chain-key")
			writer := journal.NewWriter(db, slog.New(slog.NewTextHandler(io.Discard, nil)), journal.WriterOptions{ChainKey: key, FlushInterval: time.Hour})
			t.Cleanup(func() { writer.Close() })
			store := NewSQLWaitpointStore(db)
			t.Cleanup(store.Close)
			store.SetAuditJournal(writer)
			tok, err := store.CreateApproval(context.Background(), WaitpointApprovalRequest{
				WorkspaceID: ws, PipelineRunID: "run-f1", StepID: "gate", Prompt: "May I?", TimeoutSec: 3600,
			})
			if err != nil {
				t.Fatal(err)
			}
			if fault == "journal_abort" {
				if _, err := db.Exec(`CREATE TRIGGER stop_refusal_audit BEFORE INSERT ON journal_entries BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`); err != nil {
					t.Fatal(err)
				}
			}
			err = store.Decide(context.Background(), ws, tok, true, WaitpointDecider{Kind: DeciderAgent, ID: "crew:peer"}, "GO")
			if !errors.Is(err, ErrDeciderNotAllowed) {
				t.Fatalf("agent decider: err = %v", err)
			}
			var legacy, chained int
			if err := db.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action=?`, AuditActionWaitpointDecisionRefused).Scan(&legacy); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE json_extract(payload,'$.action')=?`, AuditActionWaitpointDecisionRefused).Scan(&chained); err != nil {
				t.Fatal(err)
			}
			if fault == "journal_abort" {
				if legacy != 0 || chained != 0 {
					t.Fatalf("half-written refusal audit: legacy=%d chain=%d", legacy, chained)
				}
				return
			}
			if legacy != 1 || chained != 1 {
				t.Fatalf("refusal audit legacy=%d chain=%d, want 1/1", legacy, chained)
			}
			var raw, entryType string
			if err := db.QueryRow(`SELECT entry_type,payload FROM journal_entries WHERE json_extract(payload,'$.action')=?`, AuditActionWaitpointDecisionRefused).Scan(&entryType, &raw); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(raw, tok) {
				t.Errorf("journal payload names the callback token: %s", raw)
			}
			for _, want := range []string{`"attempted":"approve"`, `"waitpoint_status":"pending"`, `"actor_kind":"agent"`, `"pipeline_run_id":"run-f1"`} {
				if !strings.Contains(raw, want) {
					t.Errorf("payload missing %s: %s", want, raw)
				}
			}
			result, err := journal.VerifyChainWithKey(context.Background(), db, ws, key)
			if err != nil || !result.OK {
				t.Fatalf("chain invalid: %+v %v", result, err)
			}
		})
	}
}
