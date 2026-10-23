package mooring

import (
	"context"
	"time"
)

// Exchange serializes one host-initiated request/reply on a private runtime
// channel. Its caller must own the dispatcher: do not mix direct RPC reads or
// asynchronous runtime traffic with this mode. Schema, correlation, authority
// and candidate/receipt validation remain mandatory at the typed boundary.
//
// Cancellation or a transport error closes the channel: a later response cannot
// be mistaken for the next request. This is a nonterminal transport failure,
// never an operation rejection or evidence that a host commit did not happen.
func (r *Runtime) Exchange(ctx context.Context, request []byte) ([]byte, error) {
	r.exchangeOnce.Do(func() {
		r.exchangeSlot = make(chan struct{}, 1)
		r.exchangeSlot <- struct{}{}
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-r.exchangeSlot:
	}
	defer func() { r.exchangeSlot <- struct{}{} }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(5 * time.Second)
	if limit, ok := ctx.Deadline(); ok && limit.Before(deadline) {
		deadline = limit
	}
	if err := r.RPC.conn.SetDeadline(deadline); err != nil {
		_ = r.RPC.Close()
		return nil, err
	}
	cancelled := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = r.RPC.Close()
		close(cancelled)
	})
	finish := func() {
		if !stop() {
			<-cancelled
		}
	}
	if err := r.RPC.Write(request); err != nil {
		finish()
		_ = r.RPC.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	response, err := r.RPC.Read()
	finish()
	if ctx.Err() != nil {
		_ = r.RPC.Close()
		return nil, ctx.Err()
	}
	if err != nil {
		_ = r.RPC.Close()
		return nil, err
	}
	if err = r.RPC.conn.SetDeadline(time.Time{}); err != nil {
		_ = r.RPC.Close()
		return nil, err
	}
	return response, nil
}
