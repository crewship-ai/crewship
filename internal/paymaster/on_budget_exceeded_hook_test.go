package paymaster

// on_budget_exceeded_hook_test.go proves Enforce actually dispatches a
// user-registered on_budget_exceeded hook when a hard-mode budget breach
// blocks a call, closing one of the ten gaps found alongside pre_tool_call
// (#2132): on_budget_exceeded was declared in hooks.AllEvents, accepted by
// the CLI/API, and never reached by any hooks.Dispatch call anywhere in
// the tree.
//
// hooks_config isn't part of this package's hand-rolled schemaSQL (budget
// enforcement predates the hooks package), so the table is added inline
// here rather than pulled from the migrate package — same "stay decoupled,
// stay fast" reasoning schemaSQL's own comment gives for cost_ledger /
// budget_limits.

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/hooks"
)

const hooksConfigSchemaSQL = `
CREATE TABLE hooks_config (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    crew_id TEXT,
    event TEXT NOT NULL,
    matcher TEXT NOT NULL DEFAULT '{}',
    handler_kind TEXT NOT NULL CHECK(handler_kind IN ('shell','http','subagent')),
    handler_config TEXT NOT NULL DEFAULT '{}',
    blocking INTEGER NOT NULL DEFAULT 0,
    enabled INTEGER NOT NULL DEFAULT 1,
    created_by TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_hooks_event ON hooks_config(event, enabled);
CREATE INDEX idx_hooks_ws ON hooks_config(workspace_id, enabled);
`

func openTestDBWithHooks(t *testing.T) *sql.DB {
	t.Helper()
	db := openTestDB(t)
	if _, err := db.ExecContext(context.Background(), hooksConfigSchemaSQL); err != nil {
		t.Fatalf("hooks_config schema: %v", err)
	}
	return db
}

// TestEnforceHardModeBlocks_DispatchesOnBudgetExceededHook registers an
// observation webhook, breaches a hard-mode budget the same way
// TestEnforceHardModeBlocks does, and asserts the webhook was actually hit.
func TestEnforceHardModeBlocks_DispatchesOnBudgetExceededHook(t *testing.T) {
	t.Setenv("CREWSHIP_HOOKS_ALLOW_PRIVATE", "true") // httptest binds 127.0.0.1; opt out of the SSRF guard like the hooks package's own tests do

	db := openTestDBWithHooks(t)
	em := &fakeEmitter{}
	ctx := context.Background()

	hit := make(chan struct{}, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit <- struct{}{}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()

	if _, err := hooks.Register(ctx, db, hooks.Hook{
		WorkspaceID: "ws1",
		Event:       hooks.EventOnBudgetExceeded,
		HandlerKind: hooks.HandlerKindHTTP,
		HandlerConfig: map[string]any{
			"url": ts.URL,
		},
		Enabled: true,
	}, false); err != nil {
		t.Fatalf("register hook: %v", err)
	}

	mustExec(t, db, `INSERT INTO budget_limits (id, workspace_id, scope_kind, scope_id, window, limit_usd, mode)
	                 VALUES ('b1', 'ws1', 'workspace', 'ws1', 'day', 1.0, 'hard')`)
	now := time.Now().UTC().Format(tsLayout)
	mustExec(t, db, `INSERT INTO cost_ledger (id, workspace_id, ts, provider, model, cost_usd)
	                 VALUES ('c1', 'ws1', ?, 'anthropic', 'claude-opus-4-7', 1.50)`, now)

	if err := Enforce(ctx, db, em, Scope{WorkspaceID: "ws1"}); err == nil {
		t.Fatal("expected BudgetExceededError")
	}

	select {
	case <-hit:
	case <-time.After(time.Second):
		t.Fatal("on_budget_exceeded hook was never dispatched — Enforce blocked the budget but did not fire the hook")
	}
}
