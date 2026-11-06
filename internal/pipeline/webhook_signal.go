package pipeline

import (
	"context"
	"database/sql"
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

// SignalDeliveryRetention bounds how long a delivery id suppresses redelivery.
const SignalDeliveryRetention = 30 * 24 * time.Hour

// SignalDeliveryResult is the recorded outcome of one webhook signal delivery.
type SignalDeliveryResult struct {
	Outcome   string   // "delivered" | "ignored"
	Reason    string   // why ignored: "no_correlation_value" | "no_matching_wait"
	RunIDs    []string // runs whose wait was satisfied
	Duplicate bool     // true when this delivery id was already processed
}

// signalDeliveryMu serialises the check-deliver-record sequence so two
// concurrent arrivals of one delivery id cannot both claim waits. The server
// is a single process over one SQLite file; the receipt primary key is the
// backstop.
var signalDeliveryMu sync.Mutex

// DeliverWebhookSignal delivers a verified webhook body to the parked
// wait:event steps of wh's workspace and pipeline whose event type and
// correlation key match. The caller must have verified the signature. It never
// starts a run. A redelivery of deliveryID returns the original outcome without
// signalling anything again. The caller resumes the returned runs.
func DeliverWebhookSignal(ctx context.Context, db *sql.DB, wh *Webhook, deliveryID string, body []byte) (SignalDeliveryResult, error) {
	if wh.SignalEventType == "" || wh.SignalCorrelationPath == "" {
		return SignalDeliveryResult{}, ErrInvalidSignalDelivery
	}
	signalDeliveryMu.Lock()
	defer signalDeliveryMu.Unlock()

	var outcome, reason, runsJSON string
	err := db.QueryRowContext(ctx, `SELECT outcome, reason, run_ids FROM pipeline_signal_deliveries WHERE webhook_id = ? AND delivery_id = ?`,
		wh.ID, deliveryID).Scan(&outcome, &reason, &runsJSON)
	if err == nil {
		res := SignalDeliveryResult{Outcome: outcome, Reason: reason, Duplicate: true}
		_ = json.Unmarshal([]byte(runsJSON), &res.RunIDs)
		return res, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return SignalDeliveryResult{}, fmt.Errorf("signal delivery lookup: %w", err)
	}

	res := SignalDeliveryResult{Outcome: "ignored"}
	payload := string(body)
	key, ok := ExtractPayloadScalar(payload, wh.SignalCorrelationPath)
	switch {
	case !ok || key == "":
		res.Reason = "no_correlation_value"
	default:
		runs, derr := NewSQLSignalWaitStore(db).DeliverCorrelated(ctx, wh.WorkspaceID, wh.TargetPipelineID, wh.SignalEventType, key, payload)
		res.RunIDs = runs
		if derr != nil && len(runs) == 0 {
			return SignalDeliveryResult{}, derr
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
	runsOut, _ := json.Marshal(res.RunIDs)
	now := time.Now().UTC()
	if _, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO pipeline_signal_deliveries (webhook_id, delivery_id, workspace_id, outcome, reason, run_ids, received_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`, wh.ID, deliveryID, wh.WorkspaceID, res.Outcome, res.Reason, string(runsOut), tsformat.Format(now)); err != nil {
		// The signal is already durable; a missing receipt only weakens
		// redelivery dedupe, so report success to the sender.
		return res, nil
	}
	_, _ = db.ExecContext(ctx, `DELETE FROM pipeline_signal_deliveries WHERE webhook_id = ? AND received_at < ?`,
		wh.ID, tsformat.Format(now.Add(-SignalDeliveryRetention)))
	return res, nil
}
