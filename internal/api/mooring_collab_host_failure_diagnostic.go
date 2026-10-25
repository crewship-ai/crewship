package api

import (
	"context"
	"sync/atomic"
)

// TEMPORARY PRIVATE DIAGNOSTIC. Not a runtime option, authority, or release feature.
// The actual fixture installs this pointer before requests; no global observer.
type collaborationHostStage uint32

const (
	hostNoFailure collaborationHostStage = iota
	hostGateTransitionFailure
	hostSendWriterCommit
	hostCatchupContext
	hostCatchupCursor
	hostCatchupEncode
	hostCatchupGate
	hostCatchupObserve
	hostCatchupPhysical
	hostCatchupSend
	hostJoinedCatchup
	hostOutputBinding
	hostOutputCurrentDispatch
	hostOutputCurrentGate
	hostOutputDenied
	hostOutputKind
	hostOutputRetiredDispatch
	hostOutputRetiredProof
	hostOutputSchema
	hostOutputUnknownTuple
	hostPumpComplete
	hostPumpFrame
	hostPumpPanic
	hostPumpJoined
	hostPumpManagement
	hostPumpMutation
	hostPumpOutput
	hostPumpRead
	hostPendingShape
	hostPendingMembership
	hostReceiptShape
	hostReceiptMatch
	hostReceiptBinding
	hostRuntimeStopped
	hostSavedChain
	hostSavedCorrelation
	hostSavedCursor
	hostSavedSnapshot
	hostSendCell
	hostSendCurrentRights
	hostSendDeadline
	hostSendOriginalOperation
	hostSendOwnerFence
	hostSendOwnerLive
	hostSendOwnerLookup
	hostSendPhysical
	hostSendPrecondition
	hostSendSQLGateLive
	hostSendSQLGateLookup
	hostSendWriter
	hostUpdateOuter
	hostUpdateShape
	hostUpdateTuple
)

// One atomic word retains only the first observed enum plus cancellation/cleanup
// flags. It never retains an error, payload, identity, callback, or raw string.
// No timer, lock, wait, IO, or successful-operation event is added.
type collaborationHostFailure struct {
	first   atomic.Uint32
	cleanup atomic.Bool
}

const hostCancelledBit uint32 = 1 << 30
const hostCleanupBit uint32 = 1 << 31

func (o *collaborationHostFailure) record(ctx context.Context, stage collaborationHostStage) {
	if o == nil || stage <= hostNoFailure || stage > hostUpdateTuple {
		return
	}
	value := uint32(stage)
	if ctx != nil && ctx.Err() != nil {
		value |= hostCancelledBit
	}
	if o.cleanup.Load() {
		value |= hostCleanupBit
	}
	o.first.CompareAndSwap(0, value)
}
func (d *collaborationHTTPDocument) observeHostFailure(ctx context.Context, stage collaborationHostStage, err error) {
	if err != nil && d != nil && d.manager != nil {
		d.manager.hostFailure.record(ctx, stage)
	}
}
func (p *collaborationSessionPump) observeHostFailure(ctx context.Context, stage collaborationHostStage, err error) error {
	if err != nil {
		p.hostFailure.record(ctx, stage)
	}
	return err
}

// Static, finite labels only. Unknown enum values are never formatted as data.
func collaborationHostStageName(value uint32) string {
	switch collaborationHostStage(value &^ (hostCancelledBit | hostCleanupBit)) {
	case hostNoFailure:
		return "none"
	case hostGateTransitionFailure:
		return "gate_transition_failure"
	case hostSendWriterCommit:
		return "send_writer_commit"
	case hostCatchupContext:
		return "catchup_context"
	case hostCatchupCursor:
		return "catchup_cursor"
	case hostCatchupEncode:
		return "catchup_encode"
	case hostCatchupGate:
		return "catchup_gate"
	case hostCatchupObserve:
		return "catchup_observe"
	case hostCatchupPhysical:
		return "catchup_physical"
	case hostCatchupSend:
		return "catchup_send"
	case hostJoinedCatchup:
		return "joined_catchup"
	case hostOutputBinding:
		return "output_binding"
	case hostOutputCurrentDispatch:
		return "output_current_dispatch"
	case hostOutputCurrentGate:
		return "output_current_gate"
	case hostOutputDenied:
		return "output_denied"
	case hostOutputKind:
		return "output_kind"
	case hostOutputRetiredDispatch:
		return "output_retired_dispatch"
	case hostOutputRetiredProof:
		return "output_retired_proof"
	case hostOutputSchema:
		return "output_schema"
	case hostOutputUnknownTuple:
		return "output_unknown_tuple"
	case hostPumpComplete:
		return "pump_complete"
	case hostPumpFrame:
		return "pump_frame"
	case hostPumpPanic:
		return "pump_panic"
	case hostPumpJoined:
		return "pump_joined"
	case hostPumpManagement:
		return "pump_management"
	case hostPumpMutation:
		return "pump_mutation"
	case hostPumpOutput:
		return "pump_output"
	case hostPumpRead:
		return "pump_read"
	case hostPendingShape:
		return "pending_shape"
	case hostPendingMembership:
		return "pending_membership"
	case hostReceiptShape:
		return "receipt_shape"
	case hostReceiptMatch:
		return "receipt_match"
	case hostReceiptBinding:
		return "receipt_binding"
	case hostRuntimeStopped:
		return "runtime_stopped"
	case hostSavedChain:
		return "saved_chain"
	case hostSavedCorrelation:
		return "saved_correlation"
	case hostSavedCursor:
		return "saved_cursor"
	case hostSavedSnapshot:
		return "saved_snapshot"
	case hostSendCell:
		return "send_cell"
	case hostSendCurrentRights:
		return "send_current_rights"
	case hostSendDeadline:
		return "send_deadline"
	case hostSendOriginalOperation:
		return "send_original_operation"
	case hostSendOwnerFence:
		return "send_owner_fence"
	case hostSendOwnerLive:
		return "send_owner_live"
	case hostSendOwnerLookup:
		return "send_owner_lookup"
	case hostSendPhysical:
		return "send_physical"
	case hostSendPrecondition:
		return "send_precondition"
	case hostSendSQLGateLive:
		return "send_s_q_l_gate_live"
	case hostSendSQLGateLookup:
		return "send_s_q_l_gate_lookup"
	case hostSendWriter:
		return "send_writer"
	case hostUpdateOuter:
		return "update_outer"
	case hostUpdateShape:
		return "update_shape"
	case hostUpdateTuple:
		return "update_tuple"
	default:
		return "invalid"
	}
}
