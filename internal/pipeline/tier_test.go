package pipeline

import (
	"context"
	"database/sql"
	"testing"
)

const tierSchemaSQL = `
CREATE TABLE workspaces (
    id TEXT PRIMARY KEY,
    execution_tiers_json TEXT NOT NULL DEFAULT '{}'
);
INSERT INTO workspaces (id, execution_tiers_json) VALUES
    ('ws_default', '{}'),
    ('ws_custom', '{"trivial":{"primary":{"adapter":"ollama","model":"llama3.2:8b"}},"smart":{"primary":{"adapter":"openai","model":"gpt-5"}}}');
`

func openTierTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), tierSchemaSQL); err != nil {
		_ = db.Close()
		t.Fatalf("schema: %v", err)
	}
	return db
}

func TestResolver_ExplicitOverride(t *testing.T) {
	db := openTierTestDB(t)
	defer db.Close()
	r := NewResolver(db)

	step := Step{ModelOverride: "claude:claude-opus-4-7"}
	primary, fb, err := r.Resolve(context.Background(), "ws_default", step)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if primary.Adapter != "claude" || primary.Model != "claude-opus-4-7" {
		t.Errorf("primary: got %+v", primary)
	}
	if len(fb) != 0 {
		t.Errorf("explicit override should have no fallback chain")
	}
}

func TestResolver_OverrideWithoutAdapterPrefix(t *testing.T) {
	db := openTierTestDB(t)
	defer db.Close()
	r := NewResolver(db)

	step := Step{ModelOverride: "claude-haiku-4-5-20251001"}
	primary, _, _ := r.Resolve(context.Background(), "ws_default", step)
	if primary.Adapter != "claude" {
		t.Errorf("default adapter should be claude, got %q", primary.Adapter)
	}
	if primary.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("model: got %q", primary.Model)
	}
}

func TestResolver_FallsBackToPackageDefaults_OnEmptyMapping(t *testing.T) {
	db := openTierTestDB(t)
	defer db.Close()
	r := NewResolver(db)

	step := Step{Complexity: ComplexityModerate}
	primary, _, _ := r.Resolve(context.Background(), "ws_default", step)
	// Workspace has '{}' → no mapping → defaultTier(moderate).
	if primary.Adapter != "claude" || primary.Model != "claude-sonnet-4-6" {
		t.Errorf("expected default sonnet, got %+v", primary)
	}
}

func TestResolver_UsesWorkspaceCustomMapping(t *testing.T) {
	db := openTierTestDB(t)
	defer db.Close()
	r := NewResolver(db)

	// ws_custom maps trivial → ollama/llama3.2:8b
	step := Step{Complexity: ComplexityTrivial}
	primary, _, _ := r.Resolve(context.Background(), "ws_custom", step)
	if primary.Adapter != "ollama" || primary.Model != "llama3.2:8b" {
		t.Errorf("expected ollama/llama3.2:8b, got %+v", primary)
	}

	// ws_custom maps smart → openai/gpt-5
	step = Step{Complexity: ComplexitySmart}
	primary, _, _ = r.Resolve(context.Background(), "ws_custom", step)
	if primary.Adapter != "openai" || primary.Model != "gpt-5" {
		t.Errorf("expected openai/gpt-5, got %+v", primary)
	}
}

func TestResolver_NoComplexityDefaultsToModerate(t *testing.T) {
	db := openTierTestDB(t)
	defer db.Close()
	r := NewResolver(db)

	primary, _, _ := r.Resolve(context.Background(), "ws_default", Step{})
	// Step has no complexity, no override → default moderate → sonnet.
	if primary.Model != "claude-sonnet-4-6" {
		t.Errorf("expected moderate→sonnet, got %+v", primary)
	}
}
