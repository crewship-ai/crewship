package gatekeeper

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper"
)

// classifyBehaviorDecision scanned the whole raw LLM body for the substring
// `"WARN"` before honouring the actual decision. A genuine block-worthy DENY /
// ESCALATE whose reason merely *quotes* the option set (e.g. `... allowed set
// ["ALLOW","WARN","DENY"]`) was silently downgraded to WARN — which is always
// non-blocking, so in block mode the destructive tool sequence is never
// interrupted. The decision must be read from the parsed JSON `decision` field,
// mirroring isUnknownDecisionInRaw.

func TestSecClassifyBehavior_DenyQuotingWarnStaysDeny(t *testing.T) {
	// Small aux models routinely wrap their JSON in prose. Here the prose
	// mentions the option token "WARN" while the actual decision is DENY.
	// parseResponse extracts only the {...} object (decision=DENY); the raw
	// substring scan must NOT downgrade this to WARN.
	raw := `Considering options "ALLOW", "WARN", "DENY". {"decision":"DENY","reason":"tight loop"}`
	got := classifyBehaviorDecision(string(keeper.DecisionDeny), "tight loop", raw)
	if got != BehaviorDeny {
		t.Fatalf("DENY whose surrounding prose mentions \"WARN\" was downgraded: got %v, want BehaviorDeny", got)
	}
}

func TestSecClassifyBehavior_EscalateQuotingWarnStaysEscalate(t *testing.T) {
	raw := `Torn between "WARN" and block. {"decision":"ESCALATE","reason":"ambiguous"}`
	got := classifyBehaviorDecision(string(keeper.DecisionEscalate), "ambiguous", raw)
	if got != BehaviorEscalate {
		t.Fatalf("ESCALATE whose reason quotes \"WARN\" was downgraded: got %v, want BehaviorEscalate", got)
	}
}

func TestSecClassifyBehavior_GenuineWarnStillWarns(t *testing.T) {
	// Regression: a real WARN must still classify as WARN after the fix.
	raw := `{"decision":"WARN","reason":"borderline"}`
	got := classifyBehaviorDecision(string(keeper.DecisionDeny), "borderline", raw)
	if got != BehaviorWarn {
		t.Fatalf("genuine WARN not recognised: got %v, want BehaviorWarn", got)
	}
}
