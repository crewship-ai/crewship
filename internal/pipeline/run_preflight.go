package pipeline

import (
	"context"
	"errors"
)

// RunPreflight answers "may this saved routine be dispatched right now" for
// the reasons that live OUTSIDE the definition: does the author crew have the
// integrations it declares, the datastores and tools it declares, and the
// credentials it declares.
//
// It exists because the same operation had two doors and only one was
// guarded. An agent asking for a routine goes over HTTP to InternalRun, which
// runs those three gates before it executes anything
// (internal/api/pipelines_exec.go). A `call_pipeline` step ran the target
// in-process, straight to runDSL — so a routine that could not be run
// directly could still be run by being called, and the {{ secrets.* }}
// resolver failed deep inside a runner with an opaque auth error instead of
// refusing up front with an actionable one.
//
// Seam, not an import: `internal/pipeline` MUST NOT import `internal/api`
// (api already imports pipeline). The three resolvers the gates need —
// crew integrations, crew resources, the vault probes — live on the api side,
// so the interface is declared here, satisfied there, and wired in
// cmd/crewship/cmd_start.go, exactly like the eight seams in
// executor_factory.go.
type RunPreflight interface {
	// Check returns nil when the routine may run. A refusal wraps
	// ErrRunPreflightBlocked and carries an operator-readable reason.
	//
	// Implementations FAIL OPEN on infrastructure trouble (no DB, resolver
	// error): a bug in resolution must never wedge every run of every
	// routine. That is the contract the HTTP gates already document and it
	// is deliberately preserved here — the two doors must not disagree about
	// what an unanswerable question means.
	Check(ctx context.Context, req PreflightRequest) error
}

// PreflightRequest is everything the gates need to judge one dispatch.
// AuthorCrewID (not the invoker) is the subject: a routine runs in its
// author's crew, so its author's connected integrations and installed tools
// are what determine whether it can work.
type PreflightRequest struct {
	WorkspaceID  string
	PipelineID   string
	PipelineSlug string
	AuthorCrewID string
	DSL          *DSL
}

// ErrRunPreflightBlocked is the sentinel every preflight refusal wraps, so a
// caller can tell "this routine may not run here" apart from "the routine ran
// and failed".
var ErrRunPreflightBlocked = errors.New("routine preflight blocked")
