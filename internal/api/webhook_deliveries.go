package api

// The workspace-scoped read surface over the webhook delivery ledger
// (docs/prd/WEBHOOKS-AGENT-PARALLELISM-IMPLEMENTATION-1-0.md §9).
//
// Before the ledger existed the only record of a delivery was four columns on
// the endpoint row — last fired, last status, last run, a counter — which
// cannot answer "did you receive this one" or "what happened to it". These two
// routes are where those questions get answered.
//
// What they do NOT return is the raw body. The ledger keeps it so a REPLAY can
// reproduce the original input; handing it back over a read API is a different
// decision with a different blast radius — a signed third-party payload can
// carry anything the sender put in it, including their own secrets, and §9's
// rule that these responses carry no credentials is not something this
// endpoint can enforce over bytes it did not write. So the detail says whether
// the payload is still held (`raw_body_available`) and when it expires, which
// is what the UI actually needs: whether to offer the replay button at all.

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/work"
)

// WebhookDeliveriesHandler reads the delivery ledger.
type WebhookDeliveriesHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

func NewWebhookDeliveriesHandler(db *sql.DB, logger *slog.Logger) *WebhookDeliveriesHandler {
	return &WebhookDeliveriesHandler{db: db, logger: logger}
}

// webhookDeliveryView is one recorded delivery. Every field is emitted on
// every response, so the derived `required` list is the truth.
type webhookDeliveryView struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`

	EndpointID   string `json:"endpoint_id"`
	EndpointKind string `json:"endpoint_kind"`
	// Profile is recorded rather than inferred: §5 forbids an endpoint
	// switching signature profile on the presence of a header, and this is
	// where an operator sees which one actually verified the request.
	Profile string `json:"profile"`

	SourceDeliveryID string `json:"source_delivery_id"`
	EventType        string `json:"event_type"`
	EventAction      string `json:"event_action"`
	SigningKeyID     string `json:"signing_key_id"`

	BodySHA256 string `json:"body_sha256"`
	BodyBytes  int64  `json:"body_bytes"`

	// FilterDecision is "accepted" or "ignored", with the reason. An ignored
	// delivery is audited rather than dropped, so a ping or a filtered event is
	// recoverable instead of invisible.
	FilterDecision string `json:"filter_decision"`
	FilterReason   string `json:"filter_reason"`
	TargetRevision string `json:"target_revision"`

	// WorkID is null for an ignored delivery: nothing was dispatched, and that
	// is the honest answer rather than an empty string that reads like an id.
	WorkID *string `json:"work_id"`

	ReceivedAt     string `json:"received_at"`
	DedupExpiresAt string `json:"dedup_expires_at"`

	// RawBodyAvailable is whether a replay can still reproduce this delivery.
	RawBodyAvailable bool    `json:"raw_body_available"`
	RawBodyExpiresAt *string `json:"raw_body_expires_at"`
}

type webhookDeliveryPage struct {
	Items      []webhookDeliveryView `json:"items"`
	NextCursor *string               `json:"next_cursor"`
}

const webhookDeliveryColumns = `id, workspace_id, endpoint_id, endpoint_kind, profile,
	source_delivery_id, event_type, event_action, signing_key_id,
	body_sha256, body_bytes, filter_decision, filter_reason, target_revision,
	work_id, received_at, dedup_expires_at,
	CASE WHEN raw_body IS NULL THEN 0 ELSE 1 END, raw_body_expires_at`

func scanWebhookDeliveryView(row interface{ Scan(...any) error }) (webhookDeliveryView, error) {
	var v webhookDeliveryView
	var workID, rawExpires sql.NullString
	var rawPresent int
	if err := row.Scan(
		&v.ID, &v.WorkspaceID, &v.EndpointID, &v.EndpointKind, &v.Profile,
		&v.SourceDeliveryID, &v.EventType, &v.EventAction, &v.SigningKeyID,
		&v.BodySHA256, &v.BodyBytes, &v.FilterDecision, &v.FilterReason, &v.TargetRevision,
		&workID, &v.ReceivedAt, &v.DedupExpiresAt, &rawPresent, &rawExpires,
	); err != nil {
		return webhookDeliveryView{}, err
	}
	v.WorkID = workNullString(workID)
	v.RawBodyExpiresAt = workNullString(rawExpires)
	v.RawBodyAvailable = rawPresent == 1
	return v, nil
}

var webhookFilterDecisions = map[string]bool{
	work.FilterAccepted: true,
	work.FilterIgnored:  true,
}

// List is a bounded, keyset-paginated read of the delivery ledger.
//
// Naming both `endpoint_id` and `source_delivery_id` asks the identity
// question — "did you receive this one" — and that goes through
// work.Store.LookupDelivery rather than a WHERE clause of our own.
//
// Identity is (workspace, endpoint, source delivery id) and never the
// signature, which changes with a fresh timestamp for the same logical
// delivery. Keeping the rule in one place matters because it has a second
// half: the ledger also merges byte-identical bodies on one endpoint, for
// profiles whose signature does not cover a delivery id. That half needs the
// body to compute a content key, so a caller who only has an id cannot reach
// it — but a WHERE clause here would be a copy that could not follow the rule
// when it changes, and this call site inherits it instead.
func (h *WebhookDeliveriesHandler) List(w http.ResponseWriter, r *http.Request) {
	ws, ok := workReadContext(w, r)
	if !ok {
		return
	}

	endpointID := r.URL.Query().Get("endpoint_id")
	sourceDeliveryID := r.URL.Query().Get("source_delivery_id")
	if sourceDeliveryID != "" {
		if endpointID == "" {
			replyError(w, http.StatusBadRequest, "source_delivery_id identifies a delivery within one endpoint; pass endpoint_id too")
			return
		}
		h.lookupOne(w, r, ws, endpointID, sourceDeliveryID)
		return
	}

	query := `SELECT ` + webhookDeliveryColumns + ` FROM webhook_deliveries WHERE workspace_id=? AND id>?`
	args := []any{ws, r.URL.Query().Get("after")}
	if endpointID != "" {
		query += ` AND endpoint_id=?`
		args = append(args, endpointID)
	}
	if decision := r.URL.Query().Get("decision"); decision != "" {
		if !webhookFilterDecisions[decision] {
			replyError(w, http.StatusBadRequest, "Unknown decision filter: "+decision)
			return
		}
		query += ` AND filter_decision=?`
		args = append(args, decision)
	}
	if eventType := r.URL.Query().Get("event_type"); eventType != "" {
		query += ` AND event_type=?`
		args = append(args, eventType)
	}
	query += ` ORDER BY id LIMIT 101`

	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		replyInternalError(w, h.logger, "list webhook deliveries", err)
		return
	}
	defer rows.Close()
	items := make([]webhookDeliveryView, 0)
	for rows.Next() {
		item, err := scanWebhookDeliveryView(rows)
		if err != nil {
			replyInternalError(w, h.logger, "scan webhook delivery", err)
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		replyInternalError(w, h.logger, "iterate webhook deliveries", err)
		return
	}
	var next *string
	if len(items) > 100 {
		items = items[:100]
		next = &items[99].ID
	}
	writeJSON(w, http.StatusOK, webhookDeliveryPage{Items: items, NextCursor: next})
}

// lookupOne answers the identity question as a page of zero or one, so a
// client polling "did you receive this" parses one shape either way.
func (h *WebhookDeliveriesHandler) lookupOne(w http.ResponseWriter, r *http.Request, workspaceID, endpointID, sourceDeliveryID string) {
	receipt, err := work.NewStore(h.db).LookupDelivery(r.Context(), workspaceID, endpointID, sourceDeliveryID)
	if errors.Is(err, work.ErrNotFound) {
		writeJSON(w, http.StatusOK, webhookDeliveryPage{Items: []webhookDeliveryView{}, NextCursor: nil})
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "look up webhook delivery", err)
		return
	}
	item, found, err := h.readDelivery(r, workspaceID, receipt.DeliveryID)
	if err != nil {
		replyInternalError(w, h.logger, "read webhook delivery", err)
		return
	}
	items := make([]webhookDeliveryView, 0, 1)
	if found {
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, webhookDeliveryPage{Items: items, NextCursor: nil})
}

// Get returns one delivery.
func (h *WebhookDeliveriesHandler) Get(w http.ResponseWriter, r *http.Request) {
	ws, ok := workReadContext(w, r)
	if !ok {
		return
	}
	item, found, err := h.readDelivery(r, ws, r.PathValue("deliveryId"))
	if err != nil {
		replyInternalError(w, h.logger, "read webhook delivery", err)
		return
	}
	if !found {
		replyError(w, http.StatusNotFound, "Webhook delivery not found")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

// readDelivery fetches one delivery fenced to the workspace. A delivery in
// another workspace is absent, not forbidden.
func (h *WebhookDeliveriesHandler) readDelivery(r *http.Request, workspaceID, deliveryID string) (webhookDeliveryView, bool, error) {
	if strings.TrimSpace(deliveryID) == "" {
		return webhookDeliveryView{}, false, nil
	}
	row := h.db.QueryRowContext(r.Context(),
		`SELECT `+webhookDeliveryColumns+` FROM webhook_deliveries WHERE workspace_id=? AND id=?`,
		workspaceID, deliveryID)
	item, err := scanWebhookDeliveryView(row)
	if errors.Is(err, sql.ErrNoRows) {
		return webhookDeliveryView{}, false, nil
	}
	if err != nil {
		return webhookDeliveryView{}, false, err
	}
	return item, true, nil
}
