package main

import (
	"context"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// samplerDSLResolver bridges the quartermaster.OnlineSampler's
// DSLResolver interface to pipeline.Store. The sampler needs to read
// each routine's eval.online policy at decision time; the store
// owns the persisted DSL JSON; this adapter parses on demand.
//
// Lives in cmd/crewship (not pipeline or quartermaster) so neither
// of those packages takes a circular dependency on the other.
type samplerDSLResolver struct {
	store *pipeline.Store
}

// GetDSLByPipelineID satisfies quartermaster.DSLResolver. Returns
// the parsed *pipeline.DSL for the given pipeline id, or nil when
// the row is missing / soft-deleted (sampler treats nil as "skip"
// — deterministic, not retryable).
func (r *samplerDSLResolver) GetDSLByPipelineID(ctx context.Context, pipelineID string) (*pipeline.DSL, error) {
	p, err := r.store.GetByID(ctx, pipelineID)
	if err != nil {
		// Bubble the error: a transient DB issue must NOT be
		// silently treated as "no eval config" — the sampler's
		// stuck-on-error path holds the watermark for retry.
		return nil, err
	}
	if p == nil || p.DefinitionJSON == "" {
		return nil, nil
	}
	dsl, err := pipeline.Parse([]byte(p.DefinitionJSON))
	if err != nil {
		// Malformed DSL on a stored pipeline is a save-time
		// failure that shouldn't have landed. Surface the error
		// so the sampler logs it; the watermark holds and the
		// operator gets a chance to fix the row before grading
		// drifts further.
		return nil, err
	}
	return dsl, nil
}
