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

// These are trusted host callbacks, not participant authority or
// author seals. Submit must return a definitive committed receipt, never a
// speculative candidate. Status must enforce its own current reader authority.
// Output must check current recipient delivery authority before forwarding any
// bytes or reporting completion; the pump itself grants no content access.
// Every callback must respect cancellation and retain no borrowed request data.
// Returned values must be owned and immutable after the callback returns.
// The default-off HTTP session bootstrap installs these callbacks. Each
// concrete callback owns its original-source and final store/delivery fences.
type collaborationSessionPumpHandlers struct {
	Submit        func(context.Context, protocol.SessionSubmitBatch) (protocol.SessionCommitResult, error)
	Status        func(context.Context, protocol.SessionStatusRequest) (protocol.SessionStatusResult, error)
	Acquire       func(context.Context, protocol.OwnerAcquire) (protocol.OwnerGrant, error)
	Renew         func(context.Context, protocol.OwnerRenew) (protocol.OwnerGrant, error)
	Ticket        func(context.Context, protocol.TicketValidationRequest) (protocol.TicketValidationResult, error)
	TicketControl func(context.Context, mooring.SessionControl, protocol.TicketValidationRequest) error
	Delivery      func(context.Context, protocol.DeliveryRenew) (protocol.DeliveryGrant, error)
	// Optional host synchronization, not authority. It spans callback AND the
	// actual correlated reply write, including every error/panic exit.
	DeliveryGuard func(context.Context, protocol.DeliveryRenew) (func(), error)
	Output        func(context.Context, protocol.SessionParticipantOutput) error
	// Optional exact-token host composition. Delivery owns either its physical
	// correlated reply or typed retirement completion; DATA remains queued opaque.
	DeliveryControl func(context.Context, mooring.SessionControl, protocol.DeliveryRenew) error
	OutputControl   func(context.Context, mooring.SessionControl, protocol.SessionParticipantOutput) error
	// Joined is captured once on the serial management reader. A nonnil owned
	// continuation moves the SAME charged token to the DATA FIFO; only that
	// worker completes it after the continuation. It grants no recipient rights.
	JoinedControl func(context.Context, mooring.SessionControl, protocol.SessionParticipantOutput) (func(context.Context) error, error)
}

type collaborationSessionPump struct {
	runtime  *mooring.SessionRuntime
	handlers collaborationSessionPumpHandlers
	started  atomic.Bool
	// Observation only: validated mutation tokens accepted by this one reader.
	// This counter grants no admission, result, delivery or completion authority.
	queuedMutations atomic.Uint64
}

func newCollaborationSessionPump(runtime *mooring.SessionRuntime, handlers collaborationSessionPumpHandlers) (*collaborationSessionPump, error) {
	if runtime == nil || runtime.RuntimeRef() == "" || runtime.Done() == nil || handlers.Submit == nil || handlers.Status == nil || (handlers.JoinedControl != nil && handlers.OutputControl == nil) {
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
	finishRuntimeWatch := beginBackgroundWork()
	go func() {
		defer finishRuntimeWatch()
		select {
		case <-p.runtime.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	type pending struct {
		token       mooring.SessionControl
		kind        string
		afterJoined func(context.Context) error
	}
	queue := make(chan pending, 128)
	workerDone := make(chan struct{})
	finishWorker := beginBackgroundWork()
	go func() {
		defer finishWorker()
		defer close(workerDone)
		defer cancel()
		defer func() {
			if recover() != nil {
				p.runtime.ObserveFailure("pump-mutation-panic", "", nil)
			}
		}() // never publish panic data
		for {
			select {
			case <-ctx.Done():
				return
			case next := <-queue:
				if ctx.Err() != nil {
					return
				}
				if e := p.mutation(ctx, next.token, next.kind); e != nil {
					p.runtime.ObserveFailure("pump-mutation", next.kind, e)
					return
				}
			}
		}
	}()
	var outputQueue chan pending
	outputDone := make(chan struct{})
	if p.handlers.OutputControl == nil {
		close(outputDone)
	} else {
		outputQueue = make(chan pending, 128)
		finishOutput := beginBackgroundWork()
		go func() {
			defer finishOutput()
			defer close(outputDone)
			defer cancel()
			defer func() {
				if recover() != nil {
					p.runtime.ObserveFailure("pump-output-panic", "", nil)
				}
			}()
			for {
				select {
				case <-ctx.Done():
					return
				case next := <-outputQueue:
					if ctx.Err() != nil {
						return
					}
					raw, e := next.token.Frame()
					if e != nil {
						p.runtime.ObserveFailure("pump-frame", next.kind, e)
						return
					}
					if next.afterJoined != nil {
						if e := next.afterJoined(ctx); e != nil {
							p.runtime.ObserveFailure("pump-joined-continuation", next.kind, e)
							return
						}
						if ctx.Err() != nil {
							p.runtime.ObserveFailure("pump-callback-context", next.kind, ctx.Err())
							return
						}
						if e := p.runtime.CompleteControl(next.token); e != nil {
							p.runtime.ObserveFailure("pump-complete-control", next.kind, e)
							return
						}
					} else if e := p.control(ctx, next.token, next.kind, raw); e != nil {
						p.runtime.ObserveFailure("pump-output", next.kind, e)
						return
					}
				}
			}
		}()
	}
	defer func() {
		cancel()
		_ = p.runtime.Close()
		<-workerDone
		<-outputDone
	}()
	defer func() {
		if recover() != nil {
			p.runtime.ObserveFailure("pump-management-panic", "", nil)
			err = errCollaborationSessionPump
		}
		if err != nil {
			p.runtime.ObserveFailure("pump-management", "", err)
		}
	}()
	for {
		token, e := p.runtime.NextControl(ctx)
		if e != nil {
			p.runtime.ObserveFailure("pump-next-control", "", e)
			return errCollaborationSessionPump
		}
		raw, e := token.Frame()
		if e != nil {
			p.runtime.ObserveFailure("pump-frame", "", e)
			return errCollaborationSessionPump
		}
		var header struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &header) != nil {
			p.runtime.ObserveFailure("pump-decode", "", errCollaborationSessionPump)
			return errCollaborationSessionPump
		}
		if header.Type == "session_submit_batch" || header.Type == "session_status_request" {
			select {
			case <-ctx.Done():
				return errCollaborationSessionPump
			case queue <- pending{token: token, kind: header.Type}:
				p.queuedMutations.Add(1)
			default:
				p.runtime.ObserveFailure("pump-queue-capacity", header.Type, errCollaborationSessionPump)
				return errCollaborationSessionPump
			}
			continue
		}
		if outputQueue != nil && header.Type == "session_participant_output" {
			var envelope struct {
				Message struct {
					Type string `json:"type"`
				} `json:"message"`
			}
			if json.Unmarshal(raw, &envelope) != nil {
				return errCollaborationSessionPump
			}
			if envelope.Message.Type == "session_joined" && p.handlers.JoinedControl != nil {
				output, e := pumpRequest[protocol.SessionParticipantOutput](ctx, raw)
				if e != nil {
					return errCollaborationSessionPump
				}
				after, e := p.handlers.JoinedControl(ctx, token, output)
				if e != nil || ctx.Err() != nil {
					if e == nil {
						e = ctx.Err()
					}
					p.runtime.ObserveFailure("pump-joined-callback", header.Type, e)
					return errCollaborationSessionPump
				}
				if after == nil {
					if e := p.runtime.CompleteControl(token); e != nil {
						p.runtime.ObserveFailure("pump-complete-control", header.Type, e)
						return errCollaborationSessionPump
					}
				} else {
					select {
					case outputQueue <- pending{token: token, kind: header.Type, afterJoined: after}:
					default:
						p.runtime.ObserveFailure("pump-queue-capacity", header.Type, errCollaborationSessionPump)
						return errCollaborationSessionPump
					}
				}
				continue
			}
			if envelope.Message.Type != "session_joined" {
				select {
				case outputQueue <- pending{token: token, kind: header.Type}:
				default:
					return errCollaborationSessionPump
				}
				continue
			}
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

func (p *collaborationSessionPump) reply(ctx context.Context, token mooring.SessionControl, response any, err error, kind string) error {
	if err != nil {
		p.runtime.ObserveFailure("pump-callback", kind, err)
		return errCollaborationSessionPump
	}
	if ctx.Err() != nil {
		p.runtime.ObserveFailure("pump-callback-context", kind, ctx.Err())
		return errCollaborationSessionPump
	}
	raw, err := json.Marshal(response)
	if err != nil {
		p.runtime.ObserveFailure("pump-response-marshal", kind, err)
		return errCollaborationSessionPump
	}
	err = p.runtime.ReplyControl(ctx, token, raw)
	if err != nil {
		p.runtime.ObserveFailure("pump-reply-control", kind, err)
	}
	return err
}

func (p *collaborationSessionPump) mutation(ctx context.Context, token mooring.SessionControl, kind string) (failure error) {
	defer func() {
		if failure != nil {
			p.runtime.ObserveFailure("pump-mutation", kind, failure)
		}
	}()
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
		return p.reply(ctx, token, result, err, kind)
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
		return p.reply(ctx, token, result, err, kind)
	default:
		return errCollaborationSessionPump
	}
}

func (p *collaborationSessionPump) control(ctx context.Context, token mooring.SessionControl, kind string, raw []byte) (failure error) {
	defer func() {
		if failure != nil {
			p.runtime.ObserveFailure("pump-management", kind, failure)
		}
	}()
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
		return p.reply(ctx, token, result, err, kind)
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
		return p.reply(ctx, token, result, err, kind)
	case "ticket_validate":
		request, err := pumpRequest[protocol.TicketValidationRequest](ctx, raw)
		if err != nil || (p.handlers.Ticket == nil && p.handlers.TicketControl == nil) {
			return errCollaborationSessionPump
		}
		if p.handlers.TicketControl != nil {
			e := p.handlers.TicketControl(ctx, token, request)
			if e != nil {
				p.runtime.ObserveFailure("pump-callback", kind, e)
			}
			return e
		}
		result, err := p.handlers.Ticket(ctx, request)
		if err == nil && (result.RequestID != request.RequestID ||
			result.Scope != request.Scope || result.SessionID != request.SessionID ||
			result.Generation != request.Fence.Generation ||
			result.ValidationNonce != request.ValidationNonce) {
			return errCollaborationSessionPump
		}
		return p.reply(ctx, token, result, err, kind)
	case "delivery_renew":
		request, err := pumpRequest[protocol.DeliveryRenew](ctx, raw)
		if err != nil || (p.handlers.Delivery == nil && p.handlers.DeliveryControl == nil) {
			return errCollaborationSessionPump
		}
		if p.handlers.DeliveryControl != nil {
			e := p.handlers.DeliveryControl(ctx, token, request)
			if e != nil {
				p.runtime.ObserveFailure("pump-callback", kind, e)
			}
			return e
		}
		if p.handlers.DeliveryGuard != nil {
			release, e := p.handlers.DeliveryGuard(ctx, request)
			if release != nil {
				defer release()
			}
			if e != nil || release == nil {
				p.runtime.ObserveFailure("pump-delivery-guard", kind, e)
				return errCollaborationSessionPump
			}
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
		return p.reply(ctx, token, result, err, kind)
	case "session_participant_output":
		var output protocol.SessionParticipantOutput
		if (p.handlers.Output == nil && p.handlers.OutputControl == nil) || ctx.Err() != nil || json.Unmarshal(raw, &output) != nil {
			return errCollaborationSessionPump
		}
		if p.handlers.OutputControl != nil {
			if e := p.handlers.OutputControl(ctx, token, output); e != nil {
				p.runtime.ObserveFailure("pump-callback", kind, e)
				return errCollaborationSessionPump
			}
			if ctx.Err() != nil {
				p.runtime.ObserveFailure("pump-callback-context", kind, ctx.Err())
				return errCollaborationSessionPump
			}
			e := p.runtime.CompleteControl(token)
			if e != nil {
				p.runtime.ObserveFailure("pump-complete-control", kind, e)
			}
			return e
		}
		if e := p.handlers.Output(ctx, output); e != nil {
			p.runtime.ObserveFailure("pump-callback", kind, e)
			return errCollaborationSessionPump
		}
		if ctx.Err() != nil {
			p.runtime.ObserveFailure("pump-callback-context", kind, ctx.Err())
			return errCollaborationSessionPump
		}
		e := p.runtime.CompleteControl(token)
		if e != nil {
			p.runtime.ObserveFailure("pump-complete-control", kind, e)
		}
		return e
	default:
		return errCollaborationSessionPump
	}
}

// The same positive correlation applies to the exact-token host composition.
func collaborationPumpDeliveryReply(ctx context.Context, runtime *mooring.SessionRuntime, token mooring.SessionControl, request protocol.DeliveryRenew, result protocol.DeliveryGrant) error {
	if ctx.Err() != nil || result.RequestID != request.RequestID || result.Scope != request.Scope || result.SessionID != request.SessionID || result.ParticipantRef != request.ParticipantRef || result.Generation != request.Fence.Generation || result.PermissionVersion != request.PermissionVersion || result.RenewalNonce != request.RenewalNonce || result.GateID != request.GateID {
		runtime.ObserveFailure("pump-response-correlation", "delivery_renew", ctx.Err())
		return errCollaborationSessionPump
	}
	raw, err := json.Marshal(result)
	if err != nil {
		runtime.ObserveFailure("pump-response-marshal", "delivery_renew", err)
		return errCollaborationSessionPump
	}
	err = runtime.ReplyControl(ctx, token, raw)
	if err != nil {
		runtime.ObserveFailure("pump-reply-control", "delivery_renew", err)
	}
	return err
}

func collaborationPumpTicketReply(ctx context.Context, runtime *mooring.SessionRuntime, token mooring.SessionControl, request protocol.TicketValidationRequest, result protocol.TicketValidationResult) error {
	if ctx.Err() != nil || result.RequestID != request.RequestID || result.Scope != request.Scope || result.SessionID != request.SessionID || result.Generation != request.Fence.Generation || result.ValidationNonce != request.ValidationNonce {
		runtime.ObserveFailure("pump-response-correlation", "ticket_validate", ctx.Err())
		return errCollaborationSessionPump
	}
	raw, err := json.Marshal(result)
	if err != nil {
		runtime.ObserveFailure("pump-response-marshal", "ticket_validate", err)
		return errCollaborationSessionPump
	}
	err = runtime.ReplyControl(ctx, token, raw)
	if err != nil {
		runtime.ObserveFailure("pump-reply-control", "ticket_validate", err)
	}
	return err
}
