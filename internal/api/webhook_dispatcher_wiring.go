package api

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/dispatch"
	"github.com/crewship-ai/crewship/internal/scheduler"
	"github.com/crewship-ai/crewship/internal/work"
)

// agentWorkRuntime routes only declared source/domain pairs. Stop, Alive and
// projection share one launch registry across sources.
type agentWorkRuntime struct {
	*WebhookRuntime
	scheduled *ScheduledRuntime
}

func (rt *agentWorkRuntime) Run(ctx context.Context, a dispatch.Assignment, started func()) error {
	if a.Item != nil && a.Item.Source == work.SourceSchedule && a.Item.DomainKind == work.DomainAgentRun {
		return rt.scheduled.Run(ctx, a, started)
	}
	return rt.WebhookRuntime.Run(ctx, a, started)
}

type agentWorkAuthorizer struct {
	webhook   dispatch.Authorizer
	scheduled dispatch.Authorizer
}

func (a agentWorkAuthorizer) Authorize(ctx context.Context, assignment dispatch.Assignment) (dispatch.Decision, error) {
	if assignment.Item != nil && assignment.Item.Source == work.SourceSchedule && assignment.Item.DomainKind == work.DomainAgentRun {
		return a.scheduled.Authorize(ctx, assignment)
	}
	return a.webhook.Authorize(ctx, assignment)
}

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
//   - the executable KINDS are declared, and as (source, domain) pairs rather
//     than as a source. `webhook` is not a work type: an agent webhook accepts
//     {webhook, agent_run} and a routine webhook accepts {webhook,
//     pipeline_run}, into the same table, and they are run by different code.
//     A filter on the source alone looks specific and takes both — and taking
//     another executor's work does not merely fail it, it CONSUMES it, because
//     the claim has already bumped the generation and burned an attempt by the
//     time anything can object. Pipeline work carries no agent id, so this
//     dispatcher's authorizer would then have refused it as work naming no
//     agent, and a routine trigger would have died as `failed` with a reason
//     about agents.
//   - recovery and shutdown live in the same lifecycle as the loop, so a stop
//     is a drain rather than an abandonment.
func (r *Router) StartWebhookDispatcher(ctx context.Context, logger *slog.Logger) (stop func(), err error) {
	stop, _, err = r.startAgentWorkDispatcher(ctx, logger, nil, 0, 0, false)
	return stop, err
}

// StartAgentWorkDispatcher starts one dispatcher for webhook and scheduled
// agent work. The returned hint is optional; polling and recovery are the
// guarantee if a producer crashes after commit.
func (r *Router) StartAgentWorkDispatcher(ctx context.Context, logger *slog.Logger,
	conv *conversation.Store, memoryMB int, cpus float64) (stop func(), hint func(string), err error) {
	return r.startAgentWorkDispatcher(ctx, logger, conv, memoryMB, cpus, true)
}

func (r *Router) startAgentWorkDispatcher(ctx context.Context, logger *slog.Logger,
	conv *conversation.Store, memoryMB int, cpus float64, scheduled bool) (stop func(), hint func(string), err error) {
	if r.webhookHandler == nil {
		return nil, nil, ErrNoWebhookRoute
	}
	if logger == nil {
		logger = r.logger
	}

	runtime := NewWebhookRuntime(r.webhookHandler)
	if r.webhookAuthorizer == nil {
		r.webhookAuthorizer = NewWebhookAuthorizer(r.db)
	}
	var authz dispatch.Authorizer = r.webhookAuthorizer
	var executable dispatch.Runtime = runtime
	kinds := []work.Kind{{Source: work.SourceWebhook, DomainKind: work.DomainAgentRun}}
	if scheduled {
		executable = &agentWorkRuntime{WebhookRuntime: runtime,
			scheduled: NewScheduledRuntime(runtime, conv, memoryMB, cpus)}
		authz = agentWorkAuthorizer{webhook: authz, scheduled: scheduler.NewScheduledAuthorizer(r.db)}
		kinds = append(kinds, work.Kind{Source: work.SourceSchedule, DomainKind: work.DomainAgentRun})
	}

	limits := work.SerialAgentLimits()
	d := dispatch.New(work.NewStore(r.db), executable, authz, dispatch.Config{
		Owner:  "webhook-dispatcher",
		Limits: limits,
		Kinds:  kinds,
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
			logger.Error("agent work dispatcher stopped with an error", "error", err)
		}
	}()

	logger.Info("agent work dispatcher started",
		"limits", "serial (parallel profile not enabled)",
		"kinds", kinds,
		"agent_total", limits.AgentTotal)

	var stopped bool
	return func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		<-done
	}, d.Hint, nil
}
