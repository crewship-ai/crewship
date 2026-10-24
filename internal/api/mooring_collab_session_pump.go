package api

import (
	"context"
	"encoding/json"
	"errors"
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

func pumpCall[Q, R any](ctx context.Context, raw []byte, call func(context.Context, Q) (R, error)) (R, error) {
	var request Q
	var empty R
	if call == nil || ctx.Err() != nil || json.Unmarshal(raw, &request) != nil {
		return empty, errCollaborationSessionPump
	}
	return call(ctx, request)
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
		result, err := pumpCall(ctx, raw, p.handlers.Submit)
		if err == nil {
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
		result, err := pumpCall(ctx, raw, p.handlers.Status)
		return p.reply(ctx, token, result, err)
	default:
		return errCollaborationSessionPump
	}
}

func (p *collaborationSessionPump) control(ctx context.Context, token mooring.SessionControl, kind string, raw []byte) error {
	switch kind {
	case "owner_acquire":
		result, err := pumpCall(ctx, raw, p.handlers.Acquire)
		return p.reply(ctx, token, result, err)
	case "owner_renew":
		result, err := pumpCall(ctx, raw, p.handlers.Renew)
		return p.reply(ctx, token, result, err)
	case "ticket_validate":
		result, err := pumpCall(ctx, raw, p.handlers.Ticket)
		return p.reply(ctx, token, result, err)
	case "delivery_renew":
		result, err := pumpCall(ctx, raw, p.handlers.Delivery)
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
