package api

// The workspace-scoped read surface over routine webhook receipts.
//
// A routine webhook delivery is not agent work: the pipeline engine executes
// it directly, so it never had a work item to point at, and the delivery
// ledger that /webhook-deliveries reads is the agent-webhook ledger. What a
// routine delivery leaves behind is a RECEIPT — its identity, the body's
// fingerprint, and the run it produced — and before these two routes existed
// that receipt could only be seen by re-sending the delivery and reading the
// 202. These routes are where "did you receive this one, and what ran" gets
// answered without a re-send.
//
// The run is the real pipeline run, not an invented work id. `run_status` is
// read from pipeline_runs at request time and is null when no run record is
// available. The receipt is written and the 202 sent BEFORE execution starts,
// so an absent record can mean "not started yet" as easily as "failed before
// a record was written" or "retained away"; the absence carries no reason,
// and neither does the response — null is the honest answer rather than an
// empty string that reads like a status or a cause nobody established.
//
// Like /webhook-deliveries, nothing here returns the payload. `body_sha256`
// and `body_bytes` identify it without publishing whatever the sender put
// inside.

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strings"
)

// RoutineReceiptsHandler reads routine webhook receipts.
type RoutineReceiptsHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

func NewRoutineReceiptsHandler(db *sql.DB, logger *slog.Logger) *RoutineReceiptsHandler {
	return &RoutineReceiptsHandler{db: db, logger: logger}
}

// routineReceiptView is one recorded routine webhook delivery. Every field is
// emitted on every response, so the derived `required` list is the truth.
type routineReceiptView struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	// WebhookID is the routine webhook (the `/pipeline-webhooks` row) that
	// accepted the delivery. Identity is (workspace, webhook, source delivery
	// id) and never the signature.
	WebhookID        string `json:"webhook_id"`
	SourceDeliveryID string `json:"source_delivery_id"`
	// Profile is the legacy routine signature shape the request presented —
	// recorded, never inferred from a header at read time.
	Profile string `json:"profile"`

	BodySHA256 string `json:"body_sha256"`
	BodyBytes  int64  `json:"body_bytes"`

	// RunID is the pipeline run this delivery produced, and the id a
	// redelivery inside the dedup window is answered with.
	RunID string `json:"run_id"`
	// RunStatus is the run's current status, or null when no run record is
	// available (not started yet, failed before a record, or retained away —
	// the absence does not say which).
	RunStatus *string `json:"run_status"`

	ReceivedAt     string `json:"received_at"`
	DedupExpiresAt string `json:"dedup_expires_at"`
}

type routineReceiptPage struct {
	Items      []routineReceiptView `json:"items"`
	NextCursor *string              `json:"next_cursor"`
}

const routineReceiptColumns = `r.id, r.workspace_id, r.endpoint_id, r.source_delivery_id, r.profile,
	r.body_sha256, r.body_bytes, r.run_id, pr.status, r.received_at, r.dedup_expires_at`

const routineReceiptFrom = ` FROM routine_webhook_receipts r
	LEFT JOIN pipeline_runs pr ON pr.id = r.run_id AND pr.workspace_id = r.workspace_id`

func scanRoutineReceiptView(row interface{ Scan(...any) error }) (routineReceiptView, error) {
	var v routineReceiptView
	var runStatus sql.NullString
	if err := row.Scan(
		&v.ID, &v.WorkspaceID, &v.WebhookID, &v.SourceDeliveryID, &v.Profile,
		&v.BodySHA256, &v.BodyBytes, &v.RunID, &runStatus, &v.ReceivedAt, &v.DedupExpiresAt,
	); err != nil {
		return routineReceiptView{}, err
	}
	v.RunStatus = workNullString(runStatus)
	return v, nil
}

// List is a bounded, keyset-paginated read of routine webhook receipts.
//
// Naming both `webhook_id` and `source_delivery_id` asks the identity question
// — "did you receive this one" — and answers with a page of zero or one, so a
// client polling for it parses one shape either way. A sender's delivery id is
// only unique within a webhook, so `source_delivery_id` on its own is refused.
func (h *RoutineReceiptsHandler) List(w http.ResponseWriter, r *http.Request) {
	ws, ok := workReadContext(w, r)
	if !ok {
		return
	}

	webhookID := r.URL.Query().Get("webhook_id")
	sourceDeliveryID := r.URL.Query().Get("source_delivery_id")
	if sourceDeliveryID != "" && webhookID == "" {
		replyError(w, http.StatusBadRequest, "source_delivery_id identifies a delivery within one webhook; pass webhook_id too")
		return
	}

	query := `SELECT ` + routineReceiptColumns + routineReceiptFrom + ` WHERE r.workspace_id=? AND r.id>?`
	args := []any{ws, r.URL.Query().Get("after")}
	if webhookID != "" {
		query += ` AND r.endpoint_id=?`
		args = append(args, webhookID)
	}
	if sourceDeliveryID != "" {
		query += ` AND r.source_delivery_id=?`
		args = append(args, sourceDeliveryID)
	}
	query += ` ORDER BY r.id LIMIT 101`

	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		replyInternalError(w, h.logger, "list routine webhook receipts", err)
		return
	}
	defer rows.Close()
	items := make([]routineReceiptView, 0)
	for rows.Next() {
		item, err := scanRoutineReceiptView(rows)
		if err != nil {
			replyInternalError(w, h.logger, "scan routine webhook receipt", err)
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		replyInternalError(w, h.logger, "iterate routine webhook receipts", err)
		return
	}
	var next *string
	if len(items) > 100 {
		items = items[:100]
		next = &items[99].ID
	}
	writeJSON(w, http.StatusOK, routineReceiptPage{Items: items, NextCursor: next})
}

// Get returns one receipt. A receipt in another workspace is absent, not
// forbidden.
func (h *RoutineReceiptsHandler) Get(w http.ResponseWriter, r *http.Request) {
	ws, ok := workReadContext(w, r)
	if !ok {
		return
	}
	receiptID := strings.TrimSpace(r.PathValue("receiptId"))
	if receiptID == "" {
		replyError(w, http.StatusNotFound, "Routine webhook receipt not found")
		return
	}
	row := h.db.QueryRowContext(r.Context(),
		`SELECT `+routineReceiptColumns+routineReceiptFrom+` WHERE r.workspace_id=? AND r.id=?`, ws, receiptID)
	item, err := scanRoutineReceiptView(row)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "Routine webhook receipt not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "read routine webhook receipt", err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}
