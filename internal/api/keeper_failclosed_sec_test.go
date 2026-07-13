package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper/gatekeeper"
	"github.com/crewship-ai/crewship/internal/policy"
)

// TestSecKeeperPhase2_Behavior_FailsClosed_OnPolicyResolveError is the #1047
// regression. The behavior endpoint feeds the resolved policy INTO the
// decision (BehaviorMode block vs warn is what makes a DENY actually
// ShouldBlock). resolvePolicySafe fell back to guided/warn on a resolve
// error, so a crew configured behavior_mode=block that hit a transient
// resolve failure was silently evaluated as warn and a tool call that should
// have been ShouldBlock=true was downgraded to non-blocking — a fail-open
// enforcement bypass.
//
// The fix resolves strictly and defers (503) on error instead of downgrading.
//
// To isolate the resolve failure from audit persistence, the policy resolver
// is backed by a SEPARATE, closed DB (Resolve → DB error), while the handler's
// own DB stays open. Pre-fix: 200 with should_block=false. Post-fix: 503.
func TestSecKeeperPhase2_Behavior_FailsClosed_OnPolicyResolveError(t *testing.T) {
	db, _ := kp2DB(t)
	// The crew IS block mode — a correct resolve would block. The point is
	// that a resolve *error* must not silently downgrade that to warn.
	if _, err := db.Exec(`UPDATE crews SET behavior_mode='block' WHERE id='cr1'`); err != nil {
		t.Fatal(err)
	}

	// Resolver backed by a closed DB → Resolve returns a (non-ErrNoRows) error.
	polDB, _ := kp2DB(t)
	polDB.Close()
	pr := policy.NewResolver(polDB)

	p := &kp2Provider{content: `{"decision":"DENY","reason":"destructive","risk":9}`}
	gk := gatekeeper.New(p, "claude-haiku-4-5", kp2Logger())
	ev := gatekeeper.NewBehaviorEvaluator(gk, kp2Logger())
	h := NewKeeperPhase2Handler(db, "tok", pr, nil, ev, nil, nil, kp2Logger())

	body := behaviorBody{
		WorkspaceID: "ws1", CrewID: "cr1",
		AgentID: "a1", AgentName: "Worker", CrewName: "Ops",
		ToolName: "shell_exec", ToolArgsSnippet: `{"cmd":"rm -rf /"}`,
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/internal/keeper/behavior", mustJSON(t, body))
	r = r.WithContext(context.WithValue(r.Context(), ctxWorkspaceID, body.WorkspaceID))
	h.HandleBehavior(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s; want 503 (policy resolve error must defer, not downgrade block→warn)",
			w.Code, w.Body.String())
	}
}

// TestSecKeeperPhase2_Escalate_FailsClosed_OnInboxInsertError is the #1048
// regression. The ESCALATE/DENY inbox row is the ONLY operator-visible
// surface for an F4 decision; the four handlers used `_ = inbox.Insert(...)`
// and returned 200 even when the insert failed — governance that fails
// silently, no human alerted that a tool/skill/behaviour was escalated.
//
// Repro: drop inbox_items so the insert fails while keeper_requests (the audit
// row, written first) still lands. Pre-fix: 200. Post-fix: 500.
func TestSecKeeperPhase2_Escalate_FailsClosed_OnInboxInsertError(t *testing.T) {
	db, _ := kp2DB(t)
	if _, err := db.Exec(`UPDATE crews SET behavior_mode='block' WHERE id='cr1'`); err != nil {
		t.Fatal(err)
	}
	pr := policy.NewResolver(db)

	p := &kp2Provider{content: `{"decision":"DENY","reason":"destructive","risk":9}`}
	gk := gatekeeper.New(p, "claude-haiku-4-5", kp2Logger())
	ev := gatekeeper.NewBehaviorEvaluator(gk, kp2Logger())
	h := NewKeeperPhase2Handler(db, "tok", pr, nil, ev, nil, nil, kp2Logger())

	// Make the operator-inbox insert fail; the audit INSERT (keeper_requests)
	// still succeeds so we prove the handler refuses to 200 when only the
	// operator surface fails.
	if _, err := db.Exec(`DROP TABLE inbox_items`); err != nil {
		t.Fatalf("drop inbox_items: %v", err)
	}

	body := behaviorBody{
		WorkspaceID: "ws1", CrewID: "cr1",
		AgentID: "a1", AgentName: "Worker", CrewName: "Ops",
		ToolName: "shell_exec", ToolArgsSnippet: `{"cmd":"rm -rf /"}`,
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/internal/keeper/behavior", mustJSON(t, body))
	r = r.WithContext(context.WithValue(r.Context(), ctxWorkspaceID, body.WorkspaceID))
	h.HandleBehavior(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s; want 500 (a failed ESCALATE/DENY inbox insert must not return 200)",
			w.Code, w.Body.String())
	}
}
