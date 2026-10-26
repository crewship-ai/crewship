package api

import (
	"context"
	"errors"
	"log/slog"
	"net"

	"github.com/gorilla/websocket"

	"github.com/crewship-ai/crewship/internal/mooring"
)

// Error text may contain peer-controlled close reasons or network details.
// Only the fixed classification below is eligible for production logs.
func collaborationSocketCloseReason(err error) string {
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		return "read_timeout"
	}
	var closed *websocket.CloseError
	if errors.As(err, &closed) {
		return "peer_closed"
	}
	return "transport_error"
}

func collaborationLogSocketTermination(logger *slog.Logger, ref, phase, reason string) {
	if logger == nil {
		return
	}
	// Bound diagnostics to their fixed vocabulary even if a future caller passes
	// an untrusted error string by mistake. Connection refs are host-generated.
	if phase != "join" && phase != "active" {
		phase = "join"
	}
	switch reason {
	case "read_timeout", "peer_closed", "session_closed", "transport_error", "ping_write_failed":
	default:
		reason = "transport_error"
	}
	logger.Info("mooring websocket terminated", "connection_ref", ref, "phase", phase, "closed_reason", reason)
}

// Per-call observations only: no authority, shared connection state, or error
// wrapping. Nested capture/reserve steps annotate the same synchronous ingress.
type collaborationIngressDiagnostic struct {
	stage    string
	capacity string
}
type collaborationIngressDiagnosticKey struct{}

func collaborationIngressDiagnosticFrom(ctx context.Context) *collaborationIngressDiagnostic {
	if ctx == nil {
		return nil
	}
	d, _ := ctx.Value(collaborationIngressDiagnosticKey{}).(*collaborationIngressDiagnostic)
	return d
}
func (d *collaborationIngressDiagnostic) at(stage string) {
	if d != nil {
		d.stage = stage
		d.capacity = ""
	}
}
func (d *collaborationIngressDiagnostic) capacityAt(stage, predicate string) {
	if d != nil {
		d.stage = stage
		d.capacity = predicate
	}
}
func collaborationIngressCategory(err error, ctx context.Context, d *collaborationIngressDiagnostic) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline"
	}
	if errors.Is(err, mooring.ErrBatchCapacity) {
		if d != nil && (d.capacity == "capacity_bytes" || d.capacity == "capacity_records") {
			return d.capacity
		}
		return "capacity"
	}
	if errors.Is(err, context.Canceled) && ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "deadline"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	return "rejected"
}
func collaborationLogIngressFailure(logger *slog.Logger, connectionRef, admissionRef, runtimeRef, stage, category string) {
	if logger == nil {
		return
	}
	switch stage {
	case "active_read", "active_schema", "active_header", "active_current_gate", "active_checkpoint", "active_reject", "active_type",
		"submit_binding", "submit_admission", "capture_public_validate", "capture_public_schema", "capture_public_envelope", "capture_public_changes", "capture_public_normalize", "capture_public_encode", "capture_public_packet", "capture_public_id", "capture_public_source", "capture_public_duplicate", "capture_public_capacity", "capture_public_change_identity", "capture_public_operation_identity", "capture_public_slots",
		"reserve_validate", "reserve_normalize", "reserve_packet", "reserve_decode", "reserve_parse", "reserve_id", "reserve_source", "reserve_identity", "reserve_basis", "reserve_capacity",
		"capture_validate", "capture_connection", "capture_origin", "capture_store_binding", "capture_run_human", "capture_submission", "capture_seal", "capture_wait", "send_public_group", "send_original_bound", "send_original_writer", "send_original_cell", "send_original_admission", "send_original_runtime":
	default:
		stage = "unclassified"
	}
	switch category {
	case "deadline", "canceled", "capacity", "capacity_bytes", "capacity_records", "rejected", "read_timeout", "peer_closed", "transport_error":
	default:
		category = "rejected"
	}
	logger.Info("mooring websocket ingress failed", "connection_ref", connectionRef, "admission_ref", admissionRef, "runtime_ref", runtimeRef, "stage", stage, "category", category)
}
func (c *collaborationHTTPConnection) logIngressFailure(stage, category string) {
	if c == nil || c.document == nil || c.document.manager == nil || c.document.manager.pages == nil {
		return
	}
	runtimeRef := ""
	if c.document.cell != nil && c.document.cell.runtime != nil {
		runtimeRef = c.document.cell.runtime.RuntimeRef()
	}
	collaborationLogIngressFailure(c.document.manager.pages.logger, c.ref, collaborationAdmissionRef(c.admission), runtimeRef, stage, category)
}
