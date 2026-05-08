package pipeline

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// TierMapping is the workspace-level config that maps each named
// complexity tier to an adapter+model pair, with optional fallback.
// Stored per-workspace as JSON in workspaces.execution_tiers_json.
//
// The default mapping is seeded by the v78 migration:
//
//	trivial  → claude / haiku-4-5
//	fast     → claude / haiku-4-5  (fallback: sonnet-4-6)
//	moderate → claude / sonnet-4-6
//	smart    → claude / opus-4-7
//
// Workspaces can override individual tiers (e.g. trivial → ollama /
// llama3.2:8b for full local execution) via the settings UI in
// Phase 2. For MVP, the default values are good enough.
type TierMapping map[string]TierTarget

// TierTarget binds one tier (or one fallback step) to a concrete
// adapter+model pair. Adapters mirror the values in
// internal/orchestrator/adapter_*.go: claude, codex, cursor, droid,
// gemini, opencode, plus "ollama" via internal/llm.
type TierTarget struct {
	Primary  AdapterModel   `json:"primary"`
	Fallback []AdapterModel `json:"fallback,omitempty"`
}

// AdapterModel is the resolved (adapter, model) pair the executor
// hands to orchestrator.RunAgent or to the LLM middleware directly.
type AdapterModel struct {
	Adapter string `json:"adapter"`
	Model   string `json:"model"`
}

// Resolver translates a step's Complexity (or explicit ModelOverride)
// into a concrete AdapterModel pair, using the workspace's tier
// mapping with fallback to a hard-coded sane default.
type Resolver struct {
	db *sql.DB
}

// NewResolver returns a Resolver bound to the supplied DB handle.
func NewResolver(db *sql.DB) *Resolver {
	return &Resolver{db: db}
}

// Resolve returns the primary AdapterModel for the given step in the
// given workspace. ModelOverride wins; absent that, the step's
// complexity is looked up in the workspace tier mapping; absent that,
// the package default for "moderate" is used.
//
// FallbackChain returns the additional fallback targets to attempt on
// validation failure (when on_fail = escalate_tier). Empty slice means
// "no further escalation possible".
func (r *Resolver) Resolve(ctx context.Context, workspaceID string, step Step) (primary AdapterModel, fallback []AdapterModel, err error) {
	// 1. Explicit override wins. We still need an adapter — the
	//    convention is that ModelOverride may be just a model name
	//    (e.g. "claude-haiku-4-5-20251001") OR an "adapter:model"
	//    pair. The colon form is unambiguous for new code.
	if step.ModelOverride != "" {
		am := splitAdapterModel(step.ModelOverride, "claude")
		return am, nil, nil
	}

	tier := step.Complexity
	if tier == "" {
		tier = ComplexityModerate
	}

	mapping, err := r.loadMapping(ctx, workspaceID)
	if err != nil {
		// Hard fall through to default — failing to resolve a tier
		// should not crash the pipeline run; we want the most
		// graceful degradation possible.
		return defaultTier(tier), nil, nil
	}
	target, ok := mapping[string(tier)]
	if !ok {
		return defaultTier(tier), nil, nil
	}
	return target.Primary, target.Fallback, nil
}

// loadMapping reads workspaces.execution_tiers_json for one workspace
// and decodes it into a TierMapping. Returns an empty map (nil) and
// no error on a missing/empty value — the caller falls back to the
// package default.
func (r *Resolver) loadMapping(ctx context.Context, workspaceID string) (TierMapping, error) {
	if workspaceID == "" {
		return nil, errors.New("pipeline: tier resolver: workspace_id required")
	}
	var raw sql.NullString
	err := r.db.QueryRowContext(ctx,
		`SELECT execution_tiers_json FROM workspaces WHERE id = ?`, workspaceID,
	).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("pipeline: tier resolver: workspace %q not found", workspaceID)
	}
	if err != nil {
		return nil, fmt.Errorf("pipeline: tier resolver: query: %w", err)
	}
	if !raw.Valid || raw.String == "" || raw.String == "{}" {
		return nil, nil
	}
	var m TierMapping
	if err := json.Unmarshal([]byte(raw.String), &m); err != nil {
		return nil, fmt.Errorf("pipeline: tier resolver: decode: %w", err)
	}
	return m, nil
}

// splitAdapterModel parses a "model" or "adapter:model" string into
// an AdapterModel. The default adapter is used when the colon form
// is absent — typically "claude" since that's the most common case
// in author tier emissions.
func splitAdapterModel(s, defaultAdapter string) AdapterModel {
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			return AdapterModel{Adapter: s[:i], Model: s[i+1:]}
		}
	}
	return AdapterModel{Adapter: defaultAdapter, Model: s}
}

// defaultTier returns the package-level default AdapterModel for a
// tier name. Used only when the workspace mapping is missing or
// malformed — production workspaces always have the v78 seed.
//
// Models hard-coded here mirror the v78 migration default exactly so
// behaviour stays stable when a workspace's tier JSON is reset.
func defaultTier(tier Complexity) AdapterModel {
	switch tier {
	case ComplexityTrivial, ComplexityFast:
		return AdapterModel{Adapter: "claude", Model: "claude-haiku-4-5-20251001"}
	case ComplexitySmart:
		return AdapterModel{Adapter: "claude", Model: "claude-opus-4-7"}
	default: // moderate or unknown
		return AdapterModel{Adapter: "claude", Model: "claude-sonnet-4-6"}
	}
}
