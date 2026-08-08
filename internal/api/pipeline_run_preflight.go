package api

import (
	"context"
	"fmt"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// routinePreflight satisfies pipeline.RunPreflight: it applies the three
// dispatch gates — integrations, resources, credentials — to a routine the
// executor is about to run IN PROCESS from a call_pipeline step.
//
// The bug it closes: `run_routine` (the agent-facing door) goes over HTTP to
// InternalRun, which runs all three before executing anything. `call_pipeline`
// (the routine-author's door) went straight to runDSL. Same operation, two
// doors, one guarded — so a routine that could not be run directly could be
// run by being called, and a missing credential surfaced as an opaque auth
// error deep in a runner rather than as an actionable refusal.
//
// It reuses the gates' extracted decision cores (findMissing*) rather than
// re-deriving anything. That is the whole point: one gate with two renderings
// — a 422 Problem Details body on the HTTP path, a wrapped error on this one —
// not two gates that will drift.
type routinePreflight struct {
	h *PipelineHandler
}

// RunPreflight returns the handler's dispatch-gate seam for wiring into
// pipeline.ExecutorDeps. Exported so the boot-time executors built in
// cmd/crewship/cmd_start.go (resume scan, cron scheduler, pending-run
// dispatcher) enforce the same gates as the HTTP-driven ones — a gate wired
// on one path and not the others is the drift class NewWiredExecutor exists
// to prevent.
func (h *PipelineHandler) RunPreflight() pipeline.RunPreflight {
	if h == nil {
		return nil
	}
	return &routinePreflight{h: h}
}

// Check runs the gates in the same order InternalRun does. The first refusal
// wins; each returns the same sentence its HTTP counterpart would have put in
// the 422 `detail` field, wrapped in pipeline.ErrRunPreflightBlocked.
//
// The subject crew is the ROUTINE'S AUTHOR crew, not the caller's — a routine
// executes in its author's crew, so its author's connected integrations and
// installed tools decide whether it can work. That matches InternalRun, which
// passes p.AuthorCrewID to all three gates.
func (p *routinePreflight) Check(ctx context.Context, req pipeline.PreflightRequest) error {
	h := p.h
	if h == nil || req.DSL == nil {
		return nil
	}

	if missing := h.findMissingIntegrations(ctx, req.WorkspaceID, req.AuthorCrewID,
		req.DSL.NormalizedIntegrationsRequired()); len(missing) > 0 {
		return p.blocked(ctx, req, missingIntegrationsDetail(missing, p.crewName(ctx, req)))
	}

	ds, tools := declaredResources(req.DSL)
	if missing := h.findMissingResources(ctx, req.WorkspaceID, req.AuthorCrewID, ds, tools); len(missing) > 0 {
		return p.blocked(ctx, req, missingResourcesDetail(missing, p.crewName(ctx, req)))
	}

	if missing := h.findMissingCredentials(ctx, req.WorkspaceID, req.AuthorCrewID, req.DSL); len(missing) > 0 {
		return p.blocked(ctx, req, missingCredentialsDetail(missing, p.crewName(ctx, req)))
	}
	return nil
}

// blocked wraps a gate's human sentence in the sentinel, naming the routine
// that was refused — the caller is a step in some OTHER routine, so "which
// one" is not obvious from the error alone.
func (p *routinePreflight) blocked(_ context.Context, req pipeline.PreflightRequest, detail string) error {
	return fmt.Errorf("%w: routine %q: %s", pipeline.ErrRunPreflightBlocked, req.PipelineSlug, detail)
}

// crewName resolves the author crew's display name for the message, falling
// back to the id — same fallback chain the HTTP gates use. Resolved lazily so
// the allowed path (the overwhelming majority) does no extra query.
func (p *routinePreflight) crewName(ctx context.Context, req pipeline.PreflightRequest) string {
	if p.h.db != nil && req.AuthorCrewID != "" {
		if name := lookupCrewName(ctx, p.h.db, req.WorkspaceID, req.AuthorCrewID); name != "" {
			return name
		}
	}
	return req.AuthorCrewID
}
