package api

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/crewship-ai/crewship/internal/dispatch"
	"github.com/crewship-ai/crewship/internal/work"
)

// ErrNoWebhookRoute means the agent-webhook route was never registered, so
// there is nothing to execute for and no dispatcher to build.
var ErrNoWebhookRoute = errors.New("api: the agent-webhook route is not registered")

// StartWebhookDispatcher builds and runs the one dispatcher that executes
// accepted agent-webhook work, and returns a stop function.
//
// It is the whole wiring, in one place, because every part of it is a rule that
// has to hold together:
//
//   - the Authorizer is NOT optional. A dispatcher without one runs whatever it
//     claims, which means work authorised minutes or hours earlier runs on
//     permissions nobody rechecked. If this cannot be built, the server should
//     not start a dispatcher at all.
//   - the limits are SERIAL, for every adapter. The parallel profile is not
//     verified — T06 and T07 have not been run against a real Claude runtime —
//     and a dispatcher that quietly allowed two concurrent runs would be
//     enabling that profile by omission.
//   - the sources are declared. This dispatcher can execute webhook work and
//     nothing else, and claiming another producer's work would not merely fail
//     it, it would CONSUME it: the attempt is burned and the producer that
//     could have handled it never sees it again.
//   - recovery and shutdown live in the same lifecycle as the loop, so a stop
//     is a drain rather than an abandonment.
func (r *Router) StartWebhookDispatcher(ctx context.Context, logger *slog.Logger) (stop func(), err error) {
	if r.webhookHandler == nil {
		return nil, ErrNoWebhookRoute
	}
	if logger == nil {
		logger = r.logger
	}

	runtime := NewWebhookRuntime(r.webhookHandler)
	authz := NewWebhookAuthorizer(r.db)

	limits := work.SerialAgentLimits()
	d := dispatch.New(work.NewStore(r.db), runtime, authz, dispatch.Config{
		Owner:   "webhook-dispatcher",
		Limits:  limits,
		Sources: []work.Source{work.SourceWebhook},
		// Modest, because the hint carries the common case and the poll is
		// only the guarantee behind it.
		PollInterval:        2 * time.Second,
		ConfirmPollInterval: time.Second,
	}, logger)

	// The hint. Deliberately assigned after the dispatcher exists and
	// deliberately never error-checked by the handler: acceptance must not be
	// able to fail because a nudge did.
	r.webhookHandler.dispatchHint = d.Hint

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := d.Run(runCtx); err != nil {
			logger.Error("webhook dispatcher stopped with an error", "error", err)
		}
	}()

	logger.Info("webhook dispatcher started",
		"limits", "serial (parallel profile not enabled)",
		"sources", "webhook",
		"agent_total", limits.AgentTotal)

	var stopped bool
	return func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		<-done
	}, nil
}
