package policy

import (
	"testing"
)

// TestPolicy_DecideAction_Matrix locks the per-(autonomy × action)
// decision matrix from PRD §6 F2. Every combination is explicit
// because the table is the authoritative contract — a future change
// must update both the matrix and this test in lockstep, so silent
// drift (e.g. trusted starts auto-approving skill creation without
// anyone noticing) becomes a test failure instead of a subtle
// behavior change.
func TestPolicy_DecideAction_Matrix(t *testing.T) {
	cases := []struct {
		autonomy AutonomyLevel
		action   Action
		want     Decision
	}{
		// Memory write: progressively relaxes
		{AutonomyStrict, ActionMemoryWrite, DecisionInboxApprove},
		{AutonomyGuided, ActionMemoryWrite, DecisionInboxApprove},
		{AutonomyTrusted, ActionMemoryWrite, DecisionAutoLogInbox},
		{AutonomyFull, ActionMemoryWrite, DecisionAutoJournal},

		// Skill creation: stays gated until full
		{AutonomyStrict, ActionSkillCreate, DecisionInboxApprove},
		{AutonomyGuided, ActionSkillCreate, DecisionInboxApprove},
		{AutonomyTrusted, ActionSkillCreate, DecisionInboxApprove},
		{AutonomyFull, ActionSkillCreate, DecisionAutoLogInbox},

		// Skill assign (existing skill → existing agent): only strict gates
		{AutonomyStrict, ActionSkillAssign, DecisionInboxApprove},
		{AutonomyGuided, ActionSkillAssign, DecisionAutoLogJournal},
		{AutonomyTrusted, ActionSkillAssign, DecisionAutoLogJournal},
		{AutonomyFull, ActionSkillAssign, DecisionAutoLogJournal},

		// Persona suggest via inbox proposal (Phase 1 path)
		{AutonomyStrict, ActionPersonaSuggest, DecisionInboxApprove},
		{AutonomyGuided, ActionPersonaSuggest, DecisionInboxApprove},
		{AutonomyTrusted, ActionPersonaSuggest, DecisionInboxApprove},
		{AutonomyFull, ActionPersonaSuggest, DecisionAutoJournal},

		// Persona direct write by agent: rejected across all modes in Phase 1
		// (operator-only edit per PRD §6 F6)
		{AutonomyStrict, ActionPersonaDirectWrite, DecisionRejected},
		{AutonomyGuided, ActionPersonaDirectWrite, DecisionRejected},
		{AutonomyTrusted, ActionPersonaDirectWrite, DecisionRejected},
		{AutonomyFull, ActionPersonaDirectWrite, DecisionRejected},

		// Negative learning capture
		{AutonomyStrict, ActionNegativeLearning, DecisionInboxApprove},
		{AutonomyGuided, ActionNegativeLearning, DecisionAutoLogJournal},
		{AutonomyTrusted, ActionNegativeLearning, DecisionAutoJournal},
		{AutonomyFull, ActionNegativeLearning, DecisionAutoJournal},

		// Ephemeral agent spawn: strict rejects (too risky), guided gates
		{AutonomyStrict, ActionEphemeralSpawn, DecisionRejected},
		{AutonomyGuided, ActionEphemeralSpawn, DecisionInboxApprove},
		{AutonomyTrusted, ActionEphemeralSpawn, DecisionAutoLogJournal},
		{AutonomyFull, ActionEphemeralSpawn, DecisionAutoJournal},
	}

	for _, tc := range cases {
		t.Run(string(tc.autonomy)+"/"+string(tc.action), func(t *testing.T) {
			p := Policy{AutonomyLevel: tc.autonomy, BehaviorMode: BehaviorWarn}
			got := p.DecideAction(tc.action)
			if got != tc.want {
				t.Errorf("%s × %s: got %s, want %s", tc.autonomy, tc.action, got, tc.want)
			}
		})
	}
}

// TestPolicy_DecideBehavior_WarnMode in warn mode every level treats
// the DENY decision as a non-blocking inbox notification — the
// agent's action proceeds. This is the default behavior mode.
func TestPolicy_DecideBehavior_WarnMode(t *testing.T) {
	for _, lvl := range []AutonomyLevel{AutonomyStrict, AutonomyGuided, AutonomyTrusted, AutonomyFull} {
		t.Run(string(lvl), func(t *testing.T) {
			p := Policy{AutonomyLevel: lvl, BehaviorMode: BehaviorWarn}
			got := p.DecideBehaviorDeny()
			// In warn mode DENY downgrades to a non-blocking inbox
			// notification (or journal-only at higher trust).
			if lvl == AutonomyFull {
				if got != DecisionAutoJournal {
					t.Errorf("warn × full: got %s, want %s", got, DecisionAutoJournal)
				}
			} else {
				if got != DecisionAutoLogInbox {
					t.Errorf("warn × %s: got %s, want %s (non-blocking inbox)", lvl, got, DecisionAutoLogInbox)
				}
			}
		})
	}
}

// TestPolicy_DecideBehavior_BlockMode in block mode DENY actually
// stops the agent — except at full autonomy (forbidden combination,
// see TestPolicy_Validate below).
func TestPolicy_DecideBehavior_BlockMode(t *testing.T) {
	cases := []struct {
		level AutonomyLevel
		want  Decision
	}{
		{AutonomyStrict, DecisionBlockInbox},
		{AutonomyGuided, DecisionBlockInbox},
		{AutonomyTrusted, DecisionBlockJournal},
	}
	for _, tc := range cases {
		t.Run(string(tc.level), func(t *testing.T) {
			p := Policy{AutonomyLevel: tc.level, BehaviorMode: BehaviorBlock}
			if got := p.DecideBehaviorDeny(); got != tc.want {
				t.Errorf("block × %s: got %s, want %s", tc.level, got, tc.want)
			}
		})
	}
}

// TestPolicy_Validate locks the forbidden combination block + full.
// block is opt-in restriction; full is opt-in autonomy — combining
// them creates a contradiction (the operator both trusts the agent
// fully AND wants its anti-patterns blocked).
func TestPolicy_Validate(t *testing.T) {
	bad := Policy{AutonomyLevel: AutonomyFull, BehaviorMode: BehaviorBlock}
	if err := bad.Validate(); err == nil {
		t.Error("expected validation error for autonomy=full + behavior_mode=block")
	}
	good := []Policy{
		{AutonomyLevel: AutonomyStrict, BehaviorMode: BehaviorWarn},
		{AutonomyLevel: AutonomyStrict, BehaviorMode: BehaviorBlock},
		{AutonomyLevel: AutonomyGuided, BehaviorMode: BehaviorBlock},
		{AutonomyLevel: AutonomyTrusted, BehaviorMode: BehaviorBlock},
		{AutonomyLevel: AutonomyFull, BehaviorMode: BehaviorWarn},
	}
	for _, p := range good {
		if err := p.Validate(); err != nil {
			t.Errorf("%s × %s: expected valid, got %v", p.AutonomyLevel, p.BehaviorMode, err)
		}
	}
}

// TestPolicy_Validate_RejectsBogusEnums guards the boundary so a
// JSON deserializer feeding an unknown string can't sneak past.
func TestPolicy_Validate_RejectsBogusEnums(t *testing.T) {
	if err := (Policy{AutonomyLevel: "yolo", BehaviorMode: BehaviorWarn}).Validate(); err == nil {
		t.Error("expected error for bogus autonomy_level")
	}
	if err := (Policy{AutonomyLevel: AutonomyGuided, BehaviorMode: "lax"}).Validate(); err == nil {
		t.Error("expected error for bogus behavior_mode")
	}
}
