package api

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync/atomic"

	"github.com/crewship-ai/crewship/internal/mooring"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

var errCollaborationSessionPump = errors.New("Mooring session processing stopped")

// These are trusted, inactive host callbacks, not participant authority or
// author seals. Submit must return a definitive committed receipt, never a
// speculative candidate. Status must enforce its own current reader authority.
// Output must check current recipient delivery authority before forwarding any
// bytes or reporting completion; the pump itself grants no content access.
// Every callback must respect cancellation and retain no borrowed request data.
// Returned values must be owned and immutable after the callback returns.
// No routes or application handlers install these callbacks today.
type collaborationSessionPumpHandlers struct {
	Submit   func(context.Context, protocol.SessionSubmitBatch) (protocol.SessionCommitResult, error)
	Status   func(context.Context, protocol.SessionStatusRequest) (protocol.SessionStatusResult, error)
	Acquire  func(context.Context, protocol.OwnerAcquire) (protocol.OwnerGrant, error)
	Renew    func(context.Context, protocol.OwnerRenew) (protocol.OwnerGrant, error)
	Ticket   func(context.Context, protocol.TicketValidationRequest) (protocol.TicketValidationResult, error)
	Delivery func(context.Context, protocol.DeliveryRenew) (protocol.DeliveryGrant, error)
	Output   func(context.Context, protocol.SessionParticipantOutput) error
}

type collaborationSessionPump struct {
	runtime  *mooring.SessionRuntime
	handlers collaborationSessionPumpHandlers
	started  atomic.Bool
}

func newCollaborationSessionPump(runtime *mooring.SessionRuntime, handlers collaborationSessionPumpHandlers) (*collaborationSessionPump, error) {
	if runtime == nil || runtime.RuntimeRef() == "" || runtime.Done() == nil || handlers.Submit == nil || handlers.Status == nil {
		return nil, errCollaborationSessionPump
	}
	select {
	case <-runtime.Done():
		return nil, errCollaborationSessionPump
	default:
	}
	return &collaborationSessionPump{runtime: runtime, handlers: handlers}, nil
}

// run exclusively owns NextControl for this process. It queues only opaque
// tokens: their original frames remain charged to SessionRuntime's shared
// 128-control/4MiB limit until ReplyControl or explicit notification completion.
// Submit and status use one FIFO worker; owner/delivery controls do not wait for
// a held submit. Any uncertainty kills the process before another queued submit
// can run. No original operation is converted into a terminal rejection here.
func (p *collaborationSessionPump) run(parent context.Context) (err error) {
	if p == nil || !p.started.CompareAndSwap(false, true) {
		return errCollaborationSessionPump
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = p.runtime.Close() })
	defer stop()
	go func() {
		select {
		case <-p.runtime.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	type pending struct {
		token mooring.SessionControl
		kind  string
	}
	queue := make(chan pending, 128)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		defer cancel()
		defer func() { _ = recover() }() // opaque failure; never publish panic data
		for {
			select {
			case <-ctx.Done():
				return
			case next := <-queue:
				if ctx.Err() != nil {
					return
				}
				if p.mutation(ctx, next.token, next.kind) != nil {
					return
				}
			}
		}
	}()
	defer func() {
		cancel()
		_ = p.runtime.Close()
		<-workerDone
	}()
	defer func() {
		if recover() != nil {
			err = errCollaborationSessionPump
		}
	}()
	for {
		token, e := p.runtime.NextControl(ctx)
		if e != nil {
			return errCollaborationSessionPump
		}
		raw, e := token.Frame()
		if e != nil {
			return errCollaborationSessionPump
		}
		var header struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &header) != nil {
			return errCollaborationSessionPump
		}
		if header.Type == "session_submit_batch" || header.Type == "session_status_request" {
			select {
			case <-ctx.Done():
				return errCollaborationSessionPump
			case queue <- pending{token, header.Type}:
			default:
				return errCollaborationSessionPump
			}
			continue
		}
		if e = p.control(ctx, token, header.Type, raw); e != nil {
			return errCollaborationSessionPump
		}
	}
}

// Keep the original scalar binding outside the callback. ReplyControl owns
// transport correlation, but does not compare every reply field to its request.
// These comparisons do not establish receipt semantics or participant rights.
func pumpRequest[Q any](ctx context.Context, raw []byte) (Q, error) {
	var request Q
	if ctx.Err() != nil || json.Unmarshal(raw, &request) != nil {
		return request, errCollaborationSessionPump
	}
	return request, nil
}

// Counter comparisons must remain exact above 2^53 and reject overflow or a
// noncanonical spelling. Owner knowledge may advance; delivery rights may not.
func pumpCounterAtLeast(got, original protocol.Counter) bool {
	a, errA := strconv.ParseUint(string(got), 10, 64)
	b, errB := strconv.ParseUint(string(original), 10, 64)
	return errA == nil && errB == nil &&
		strconv.FormatUint(a, 10) == string(got) &&
		strconv.FormatUint(b, 10) == string(original) && a >= b
}

func (p *collaborationSessionPump) reply(ctx context.Context, token mooring.SessionControl, response any, err error) error {
	if err != nil || ctx.Err() != nil {
		return errCollaborationSessionPump
	}
	raw, err := json.Marshal(response)
	if err != nil {
		return errCollaborationSessionPump
	}
	return p.runtime.ReplyControl(ctx, token, raw)
}

func (p *collaborationSessionPump) mutation(ctx context.Context, token mooring.SessionControl, kind string) error {
	raw, err := token.Frame()
	if err != nil {
		return errCollaborationSessionPump
	}
	switch kind {
	case "session_submit_batch":
		request, err := pumpRequest[protocol.SessionSubmitBatch](ctx, raw)
		if err != nil || p.handlers.Submit == nil {
			return errCollaborationSessionPump
		}
		result, err := p.handlers.Submit(ctx, request)
		if err == nil {
			if result.RequestID != request.RequestID ||
				string(result.ControlProfile) != string(request.ControlProfile) ||
				result.Scope != request.Scope || result.SessionID != request.SessionID ||
				result.RuntimeRef != request.RuntimeRef || result.Fence != request.Fence ||
				result.Versions != request.Versions ||
				result.Receipt.RequestID != request.RequestID ||
				result.Receipt.Scope != request.Scope ||
				result.Receipt.Generation != request.Fence.Generation {
				return errCollaborationSessionPump
			}
			for _, change := range result.Receipt.Changes {
				for _, operation := range change.Operations {
					var outcome struct {
						Status string `json:"status"`
						Reason struct {
							Code      string `json:"code"`
							RootCause struct {
								Code string `json:"code"`
							} `json:"root_cause"`
						} `json:"reason"`
					}
					if json.Unmarshal(operation, &outcome) != nil || (outcome.Status != "saved" && outcome.Status != "rejected") || outcome.Reason.Code == "unknown_outcome" || outcome.Reason.RootCause.Code == "unknown_outcome" {
						return errCollaborationSessionPump
					}
				}
			}
		}
		return p.reply(ctx, token, result, err)
	case "session_status_request":
		request, err := pumpRequest[protocol.SessionStatusRequest](ctx, raw)
		if err != nil || p.handlers.Status == nil || request.Message.Scope != request.Scope {
			return errCollaborationSessionPump
		}
		result, err := p.handlers.Status(ctx, request)
		if err == nil {
			var operation struct {
				OpID protocol.Identifier `json:"op_id"`
			}
			if result.RequestID != request.RequestID ||
				string(result.ControlProfile) != string(request.ControlProfile) ||
				result.Scope != request.Scope || result.SessionID != request.SessionID ||
				result.RuntimeRef != request.RuntimeRef || result.Fence != request.Fence ||
				result.ConnectionRef != request.ConnectionRef ||
				result.ParticipantRef != request.ParticipantRef ||
				result.GateID != request.GateID ||
				result.PermissionVersion != request.PermissionVersion ||
				result.Message.RequestID != request.Message.RequestID ||
				result.Message.Scope != request.Message.Scope ||
				json.Unmarshal(result.Message.Result, &operation) != nil ||
				operation.OpID != request.Message.OpID {
				return errCollaborationSessionPump
			}
		}
		return p.reply(ctx, token, result, err)
	default:
		return errCollaborationSessionPump
	}
}

func (p *collaborationSessionPump) control(ctx context.Context, token mooring.SessionControl, kind string, raw []byte) error {
	switch kind {
	case "owner_acquire":
		request, err := pumpRequest[protocol.OwnerAcquire](ctx, raw)
		if err != nil || p.handlers.Acquire == nil {
			return errCollaborationSessionPump
		}
		result, err := p.handlers.Acquire(ctx, request)
		if err == nil && (result.RequestID != request.RequestID ||
			result.Scope != request.Scope || result.RuntimeRef != request.RuntimeRef ||
			result.RenewalNonce != request.RenewalNonce) {
			return errCollaborationSessionPump
		}
		return p.reply(ctx, token, result, err)
	case "owner_renew":
		request, err := pumpRequest[protocol.OwnerRenew](ctx, raw)
		if err != nil || p.handlers.Renew == nil {
			return errCollaborationSessionPump
		}
		result, err := p.handlers.Renew(ctx, request)
		if err == nil && (result.RequestID != request.RequestID ||
			result.Scope != request.Scope || result.SessionID != request.SessionID ||
			result.RuntimeRef != protocol.Identifier(p.runtime.RuntimeRef()) ||
			result.Fence != request.Fence || result.RenewalNonce != request.RenewalNonce ||
			!pumpCounterAtLeast(result.PermissionVersion, request.PermissionVersion)) {
			return errCollaborationSessionPump
		}
		return p.reply(ctx, token, result, err)
	case "ticket_validate":
		request, err := pumpRequest[protocol.TicketValidationRequest](ctx, raw)
		if err != nil || p.handlers.Ticket == nil {
			return errCollaborationSessionPump
		}
		result, err := p.handlers.Ticket(ctx, request)
		if err == nil && (result.RequestID != request.RequestID ||
			result.Scope != request.Scope || result.SessionID != request.SessionID ||
			result.Generation != request.Fence.Generation ||
			result.ValidationNonce != request.ValidationNonce) {
			return errCollaborationSessionPump
		}
		return p.reply(ctx, token, result, err)
	case "delivery_renew":
		request, err := pumpRequest[protocol.DeliveryRenew](ctx, raw)
		if err != nil || p.handlers.Delivery == nil {
			return errCollaborationSessionPump
		}
		result, err := p.handlers.Delivery(ctx, request)
		if err == nil && (result.RequestID != request.RequestID ||
			result.Scope != request.Scope || result.SessionID != request.SessionID ||
			result.ParticipantRef != request.ParticipantRef ||
			result.Generation != request.Fence.Generation ||
			result.PermissionVersion != request.PermissionVersion ||
			result.RenewalNonce != request.RenewalNonce || result.GateID != request.GateID) {
			return errCollaborationSessionPump
		}
		return p.reply(ctx, token, result, err)
	case "session_participant_output":
		var output protocol.SessionParticipantOutput
		if p.handlers.Output == nil || ctx.Err() != nil || json.Unmarshal(raw, &output) != nil {
			return errCollaborationSessionPump
		}
		if p.handlers.Output(ctx, output) != nil || ctx.Err() != nil {
			return errCollaborationSessionPump
		}
		return p.runtime.CompleteControl(token)
	default:
		return errCollaborationSessionPump
	}
}
