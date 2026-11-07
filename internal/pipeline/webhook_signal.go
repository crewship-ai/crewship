package pipeline

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

// ErrInvalidSignalDelivery reports a webhook signal configuration that is
// incomplete or malformed (both signal_event_type and signal_correlation_path
// are required together, on a crewship-profile webhook).
var ErrInvalidSignalDelivery = errors.New("pipeline_webhooks: invalid signal delivery configuration")

// SignalDeliveryRetention bounds how long a delivered body suppresses redelivery.
const SignalDeliveryRetention = 30 * 24 * time.Hour

// ErrSignalDeliveryConflict reports a delivery id that was already used for a
// different body within the retention window. The original record is kept.
var ErrSignalDeliveryConflict = errors.New("pipeline_webhooks: signal delivery id reused with a different body")

// SignalDeliveryResult is the recorded outcome of one webhook signal delivery.
type SignalDeliveryResult struct {
	Outcome   string   // "delivered" | "ignored"
	Reason    string   // why ignored: "no_correlation_value" | "no_matching_wait"
	RunIDs    []string // runs whose wait was satisfied by THIS call (resume these)
	Duplicate bool     // true when this body was already fully processed
	Retry     bool     // true when nothing was waiting yet: the receipt is non-terminal and the sender should redeliver
}

// signalDeliveryMu serialises the check-deliver-record sequence so two
// concurrent arrivals of one delivery cannot both claim waits. The server
// is a single process over one SQLite file; the receipt unique indexes are the
// backstop.
var signalDeliveryMu sync.Mutex

// DeliverWebhookSignal delivers a verified webhook body to the parked
// wait:event steps of wh's workspace and pipeline whose event type and
// correlation key match. The caller must have verified the signature. It never
// starts a run.
//
// The receipt is keyed on what the signature covers, the webhook and the
// sha256 of the body; deliveryID (an unsigned header) is only used to detect
// reuse for a different body, which returns ErrSignalDeliveryConflict. A body
// seen within SignalDeliveryRetention returns the original outcome as a
// duplicate and signals nothing, so a captured signed request cannot wake a
// later wait by changing its Idempotency-Key.
//
// When claiming fails part way, the runs already claimed are returned together
// with the error and the receipt is stored incomplete: a retry of the same body
// claims only the waits still pending, so no claimed wait is woken twice.
func DeliverWebhookSignal(ctx context.Context, db *sql.DB, wh *Webhook, deliveryID string, body []byte) (SignalDeliveryResult, error) {
	if wh.SignalEventType == "" || wh.SignalCorrelationPath == "" {
		return SignalDeliveryResult{}, ErrInvalidSignalDelivery
	}
	sum := sha256.Sum256(body)
	bodySHA := hex.EncodeToString(sum[:])
	signalDeliveryMu.Lock()
	defer signalDeliveryMu.Unlock()

	// One transaction covers the receipt lookup, the wait claims and the receipt
	// write: they commit or roll back together, so a crash between claiming and
	// recording cannot leave woken waits without a receipt. Callers wake runs in
	// memory only after this function returns, i.e. after commit.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return SignalDeliveryResult{}, fmt.Errorf("signal delivery begin: %w", err)
	}
	defer tx.Rollback()
	res, derr, err := deliverSignalTx(ctx, tx, wh, deliveryID, bodySHA, body)
	if err != nil {
		return SignalDeliveryResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return SignalDeliveryResult{}, fmt.Errorf("signal delivery commit: %w", err)
	}
	return res, derr
}

// signalBeforeReceipt is a test seam between claiming waits and writing the
// receipt; a non-nil error simulates a crash there (the transaction rolls back).
var signalBeforeReceipt func() error

// deliverSignalTx does the work inside tx. The first error is a hard failure
// (roll back, nothing committed); derr is a partial-claim failure whose claims
// and incomplete receipt are still committed.
func deliverSignalTx(ctx context.Context, tx *sql.Tx, wh *Webhook, deliveryID, bodySHA string, body []byte) (SignalDeliveryResult, error, error) {
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `DELETE FROM pipeline_signal_deliveries WHERE webhook_id = ? AND received_at < ?`,
		wh.ID, tsformat.Format(now.Add(-SignalDeliveryRetention))); err != nil {
		return SignalDeliveryResult{}, nil, fmt.Errorf("signal delivery cleanup: %w", err)
	}

	var outcome, reason, runsJSON, recID string
	var complete int
	err := tx.QueryRowContext(ctx, `SELECT delivery_id, outcome, reason, run_ids, complete FROM pipeline_signal_deliveries WHERE webhook_id = ? AND body_sha256 = ?`,
		wh.ID, bodySHA).Scan(&recID, &outcome, &reason, &runsJSON, &complete)
	var prior []string
	resuming := false
	switch {
	case err == nil && complete == 1:
		res := SignalDeliveryResult{Outcome: outcome, Reason: reason, Duplicate: true}
		_ = json.Unmarshal([]byte(runsJSON), &res.RunIDs)
		return res, nil, nil
	case err == nil:
		resuming = true
		_ = json.Unmarshal([]byte(runsJSON), &prior)
	case !errors.Is(err, sql.ErrNoRows):
		return SignalDeliveryResult{}, nil, fmt.Errorf("signal delivery lookup: %w", err)
	default:
		var recSHA string
		err = tx.QueryRowContext(ctx, `SELECT body_sha256, outcome, reason, run_ids FROM pipeline_signal_deliveries WHERE webhook_id = ? AND delivery_id = ?`,
			wh.ID, deliveryID).Scan(&recSHA, &outcome, &reason, &runsJSON)
		switch {
		case err == nil && recSHA == "":
			// Receipt written before bodies were recorded: the delivery id was
			// its only identity.
			res := SignalDeliveryResult{Outcome: outcome, Reason: reason, Duplicate: true}
			_ = json.Unmarshal([]byte(runsJSON), &res.RunIDs)
			return res, nil, nil
		case err == nil:
			return SignalDeliveryResult{}, nil, ErrSignalDeliveryConflict
		case !errors.Is(err, sql.ErrNoRows):
			return SignalDeliveryResult{}, nil, fmt.Errorf("signal delivery lookup: %w", err)
		}
	}

	res := SignalDeliveryResult{Outcome: "ignored"}
	var derr error
	payload := string(body)
	key, ok := ExtractPayloadScalar(payload, wh.SignalCorrelationPath)
	switch {
	case !ok || key == "":
		res.Reason = "no_correlation_value"
	default:
		var runs []string
		runs, derr = deliverCorrelatedOn(ctx, tx, wh.WorkspaceID, wh.TargetPipelineID, wh.SignalEventType, key, payload)
		res.RunIDs = runs
		if derr != nil && len(runs) == 0 && !resuming {
			return SignalDeliveryResult{}, nil, derr
		}
		if len(runs) > 0 {
			res.Outcome = "delivered"
		} else {
			res.Reason = "no_matching_wait"
		}
	}
	if res.RunIDs == nil {
		res.RunIDs = []string{}
	}
	stored := res.RunIDs
	if resuming {
		// Finishing an earlier partial delivery: the receipt keeps every run
		// ever claimed, the result carries only the newly claimed ones.
		stored = dedupeStrings(append(append([]string{}, prior...), res.RunIDs...))
		if len(stored) > 0 {
			res.Outcome, res.Reason = "delivered", ""
		}
	}
	if signalBeforeReceipt != nil {
		if err := signalBeforeReceipt(); err != nil {
			return SignalDeliveryResult{}, nil, err
		}
	}
	runsOut, _ := json.Marshal(stored)
	done := 1
	if derr != nil {
		done = 0
	}
	if res.Outcome == "ignored" && res.Reason == "no_matching_wait" {
		// Nothing was waiting yet. Keep the row for the operator but leave it
		// non-terminal so the same body can still wake a wait armed later.
		done = 0
		res.Retry = true
	}
	if resuming {
		_, err = tx.ExecContext(ctx, `UPDATE pipeline_signal_deliveries SET outcome = ?, reason = ?, run_ids = ?, complete = ?, received_at = ? WHERE webhook_id = ? AND body_sha256 = ?`,
			res.Outcome, res.Reason, string(runsOut), done, tsformat.Format(now), wh.ID, bodySHA)
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO pipeline_signal_deliveries (webhook_id, delivery_id, workspace_id, outcome, reason, run_ids, received_at, body_sha256, complete)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, wh.ID, deliveryID, wh.WorkspaceID, res.Outcome, res.Reason, string(runsOut), tsformat.Format(now), bodySHA, done)
	}
	if err != nil {
		// Claims and receipt are one unit: without the receipt the claims roll
		// back too and the sender retries.
		return SignalDeliveryResult{}, nil, fmt.Errorf("signal delivery receipt: %w", err)
	}
	return res, derr, nil
}

// LookupSignalReceipt reports a completed signal receipt for body sha256 on
// webhook webhookID that is still inside SignalDeliveryRetention. It is how a
// webhook that left signal mode still recognises bodies it already processed.
func LookupSignalReceipt(ctx context.Context, db *sql.DB, webhookID, bodySHA string) (SignalDeliveryResult, bool, error) {
	var outcome, reason, runsJSON string
	err := db.QueryRowContext(ctx, `SELECT outcome, reason, run_ids FROM pipeline_signal_deliveries
		WHERE webhook_id = ? AND body_sha256 = ? AND complete = 1 AND received_at >= ?`,
		webhookID, bodySHA, tsformat.Format(time.Now().UTC().Add(-SignalDeliveryRetention))).Scan(&outcome, &reason, &runsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return SignalDeliveryResult{}, false, nil
	}
	if err != nil {
		return SignalDeliveryResult{}, false, fmt.Errorf("signal receipt lookup: %w", err)
	}
	res := SignalDeliveryResult{Outcome: outcome, Reason: reason, Duplicate: true}
	_ = json.Unmarshal([]byte(runsJSON), &res.RunIDs)
	return res, true, nil
}
