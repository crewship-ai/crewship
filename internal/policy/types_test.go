package policy

import (
	"os"
	"regexp"
	"sort"
	"strings"
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

		// #1768 principal row: crew / persistent agent. Stricter than
		// ephemeral_spawn at trusted (InboxApprove, not journal) because
		// neither expires or is quota-bounded, and noisier than it at full
		// (AutoLogInbox, not AutoJournal) because a new permanent principal
		// is worth an operator's glance.
		//
		// These two are BLOCKING at guided for oversight, not for security:
		// the escape is closed by strict's refusal plus autonomy inheritance
		// (TestAutonomyInvariant_ChildCrewNeverOutranksCreator in
		// internal/api). Changing these cells is a product decision about
		// operator visibility; it must not be justified as a security fix.
		{AutonomyStrict, ActionCrewCreate, DecisionRejected},
		{AutonomyGuided, ActionCrewCreate, DecisionInboxApprove},
		{AutonomyTrusted, ActionCrewCreate, DecisionInboxApprove},
		{AutonomyFull, ActionCrewCreate, DecisionAutoLogInbox},

		{AutonomyStrict, ActionAgentCreate, DecisionRejected},
		{AutonomyGuided, ActionAgentCreate, DecisionInboxApprove},
		{AutonomyTrusted, ActionAgentCreate, DecisionInboxApprove},
		{AutonomyFull, ActionAgentCreate, DecisionAutoLogInbox},

		// Cron schedule: creates no principal, so it left the row above. It
		// still outlives its session, which is why strict refuses; below
		// that a non-blocking notice gives the same visibility a blocking
		// hold would, and the row stays operator-editable via PATCH
		// .../pipeline-schedules/{id}.
		{AutonomyStrict, ActionRoutineScheduleCreate, DecisionRejected},
		{AutonomyGuided, ActionRoutineScheduleCreate, DecisionAutoLogInbox},
		{AutonomyTrusted, ActionRoutineScheduleCreate, DecisionAutoLogJournal},
		{AutonomyFull, ActionRoutineScheduleCreate, DecisionAutoLogJournal},

		// Mission creation: delegation with a plan attached, so strict
		// approves rather than rejects — it creates no principal and grants
		// nothing durable. Guided is a notice, not a hold: planning is
		// ordinary work and nothing on this path widens what the acting
		// agent could already do.
		{AutonomyStrict, ActionMissionCreate, DecisionInboxApprove},
		{AutonomyGuided, ActionMissionCreate, DecisionAutoLogInbox},
		{AutonomyTrusted, ActionMissionCreate, DecisionAutoLogJournal},
		{AutonomyFull, ActionMissionCreate, DecisionAutoJournal},

		// Issue writes (update / comment / link): modify a thing that already
		// exists, create nothing — the skill_assign shape, and the same row.
		// Guided is journal-only rather than a hold because the write is
		// already recorded on the issue itself (#1791), because one inbox item
		// per issue write is how an inbox stops being read, and because a hold
		// on the unattended path is a refusal that moves the write to the
		// sidecar's ungated door rather than stopping it.
		{AutonomyStrict, ActionIssueWrite, DecisionInboxApprove},
		{AutonomyGuided, ActionIssueWrite, DecisionAutoLogJournal},
		{AutonomyTrusted, ActionIssueWrite, DecisionAutoLogJournal},
		{AutonomyFull, ActionIssueWrite, DecisionAutoJournal},

		// Assignment creation: mission_create with the plan removed, so the
		// same row. Guided gets a non-blocking NOTICE where issue_write gets
		// journal-only — this one spends a container start and a model turn.
		{AutonomyStrict, ActionAssignmentCreate, DecisionInboxApprove},
		{AutonomyGuided, ActionAssignmentCreate, DecisionAutoLogInbox},
		{AutonomyTrusted, ActionAssignmentCreate, DecisionAutoLogJournal},
		{AutonomyFull, ActionAssignmentCreate, DecisionAutoJournal},

		// Escalation: a FLAT row on purpose. The cost is a human's attention,
		// not the system's, so trust is not the axis; and both inbox-shaped
		// decisions are self-defeating (an approval to authorise an interrupt
		// IS the interrupt; a notice about an escalation duplicates it).
		// Volume — the real risk — is bounded on the door, not here.
		{AutonomyStrict, ActionEscalationCreate, DecisionAutoLogJournal},
		{AutonomyGuided, ActionEscalationCreate, DecisionAutoLogJournal},
		{AutonomyTrusted, ActionEscalationCreate, DecisionAutoLogJournal},
		{AutonomyFull, ActionEscalationCreate, DecisionAutoJournal},

		// Page write (#1945): one panel's payload. issue_write's numbers,
		// reached by its own argument rather than by inheritance — the door is
		// already default-deny per panel and a HUMAN opened it (the declared
		// producer, or a produce grant), the frequency is the highest in this
		// matrix (a 30 s SLA, a ring sized for a push every 5 s), and the page
		// is already the artefact the operator is looking at, so an inbox item
		// would duplicate it. Strict still holds: a number other humans read
		// and trust is the write an operator at that level wants to see first.
		{AutonomyStrict, ActionPageWrite, DecisionInboxApprove},
		{AutonomyGuided, ActionPageWrite, DecisionAutoLogJournal},
		{AutonomyTrusted, ActionPageWrite, DecisionAutoLogJournal},
		{AutonomyFull, ActionPageWrite, DecisionAutoJournal},
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

var (
	actionConstRe = regexp.MustCompile(`^\s*(Action[A-Za-z0-9_]+)\s+Action\s*=`)
	actionValueRe = regexp.MustCompile(`^\s*Action[A-Za-z0-9_]+\s+Action\s*=\s*"([^"]+)"`)
	decideCaseRe  = regexp.MustCompile(`\bAction[A-Za-z0-9_]+`)
)

// autonomyOrder is the trust dial from least to most permissive. It is the
// axis TestPolicy_DecideAction_MonotonicInAutonomy walks.
var autonomyOrder = []AutonomyLevel{AutonomyStrict, AutonomyGuided, AutonomyTrusted, AutonomyFull}

// decisionRestriction ranks decisions by how much they restrain the agent, so
// two cells in the same row can be compared. Higher = more restrictive.
//
// The three "proceed" decisions are deliberately NOT all equal. AutoLogInbox
// proceeds but demands an operator's attention; the journal arms proceed
// quietly. Ranking them apart is what makes a row that gets *noisier* as trust
// rises show up as the mistake it is, rather than passing because "both
// proceed". AutoLogJournal and AutoJournal share a rank because they share a
// wire path — the distinction between them is provenance, not restraint.
var decisionRestriction = map[Decision]int{
	DecisionRejected:       5,
	DecisionBlockInbox:     4,
	DecisionBlockJournal:   4,
	DecisionInboxApprove:   3,
	DecisionAutoLogInbox:   2,
	DecisionAutoLogJournal: 1,
	DecisionAutoJournal:    1,
}

// TestPolicy_DecideAction_MonotonicInAutonomy pins the one property that holds
// across the WHOLE matrix rather than in any single cell: raising a crew's
// autonomy level must never make an action harder.
//
// The matrix is a table of independently-decided cells, which is what makes it
// readable and also what makes it possible to write an incoherent row — a
// tuning pass that relaxes `guided` while leaving `trusted` blocking produces
// a dial where turning trust UP turns capability DOWN. Nothing else in the
// package notices; every individual cell still looks defensible in isolation.
//
// Actions are read out of types.go rather than listed here, so a new Action
// inherits the check without anyone remembering to add it.
func TestPolicy_DecideAction_MonotonicInAutonomy(t *testing.T) {
	src, err := os.ReadFile("types.go")
	if err != nil {
		t.Fatalf("read types.go: %v", err)
	}
	var actions []Action
	for _, l := range strings.Split(string(src), "\n") {
		if m := actionValueRe.FindStringSubmatch(l); m != nil {
			actions = append(actions, Action(m[1]))
		}
	}
	if len(actions) < 7 {
		t.Fatalf("recovered only %d Action values from types.go — the parser is broken", len(actions))
	}

	for _, a := range actions {
		t.Run(string(a), func(t *testing.T) {
			for i := 1; i < len(autonomyOrder); i++ {
				lower, higher := autonomyOrder[i-1], autonomyOrder[i]
				dl := Policy{AutonomyLevel: lower, BehaviorMode: BehaviorWarn}.DecideAction(a)
				dh := Policy{AutonomyLevel: higher, BehaviorMode: BehaviorWarn}.DecideAction(a)
				rl, okl := decisionRestriction[dl]
				rh, okh := decisionRestriction[dh]
				if !okl || !okh {
					t.Fatalf("%s: unranked decision (%s→%s, %s→%s) — add it to decisionRestriction",
						a, lower, dl, higher, dh)
				}
				if rh > rl {
					t.Errorf("%s is STRICTER at %s (%s) than at %s (%s): raising autonomy must never "+
						"reduce what an agent may do", a, higher, dh, lower, dl)
				}
			}
		})
	}
}

// TestPolicy_EveryActionIsMapped enforces what the Action docstring has so far
// only asked for in prose: "Adding a new Action requires extending the
// decision matrix in Policy.DecideAction *and* the test matrix in
// types_test.go."
//
// A comment cannot enforce that, and the failure is invisible when it happens.
// DecideAction's defensive default returns DecisionInboxApprove for any
// unmapped pair, which is the right safety behaviour but also completely
// indistinguishable — from the outside — from a row someone deliberately
// mapped to InboxApprove. So an Action added and never mapped does not crash,
// does not fail a test, and does not look wrong in the operator UI for any
// action whose intended answer was "ask a human"; it simply stops tracking
// what the matrix says once someone later intends it to relax. The defensive
// default buys safety, not visibility, and this test is the visibility half.
//
// Derived from source rather than from a hand-kept list, for the same reason
// the sidecar route table is (memory_routes_coverage_test.go): a list that has
// to be remembered is a list that drifts.
func TestPolicy_EveryActionIsMapped(t *testing.T) {
	src, err := os.ReadFile("types.go")
	if err != nil {
		t.Fatalf("read types.go: %v", err)
	}
	lines := strings.Split(string(src), "\n")

	// Every `ActionFoo Action = "..."` constant declared in the package.
	var declared []string
	for _, l := range lines {
		if m := actionConstRe.FindStringSubmatch(l); m != nil {
			declared = append(declared, m[1])
		}
	}
	if len(declared) < 7 {
		t.Fatalf("recovered only %d Action constants from types.go — the parser is broken", len(declared))
	}

	// Every Action named by a `case` arm inside DecideAction's switch.
	start, end := -1, -1
	for i, l := range lines {
		if strings.Contains(l, "func (p Policy) DecideAction(") {
			start = i
		}
		if start >= 0 && strings.Contains(l, "Defensive default") {
			end = i
			break
		}
	}
	if start < 0 || end < 0 {
		t.Fatal("could not locate DecideAction's switch in types.go — update this test")
	}
	mapped := map[string]bool{}
	for i := start; i < end; i++ {
		if !strings.HasPrefix(strings.TrimSpace(lines[i]), "case ") {
			continue
		}
		for _, name := range decideCaseRe.FindAllString(lines[i], -1) {
			mapped[name] = true
		}
	}

	// And every Action exercised by the matrix test above, read from this
	// file so the two lists cannot be satisfied by editing only one.
	tsrc, err := os.ReadFile("types_test.go")
	if err != nil {
		t.Fatalf("read types_test.go: %v", err)
	}
	tested := map[string]bool{}
	for _, name := range decideCaseRe.FindAllString(string(tsrc), -1) {
		tested[name] = true
	}

	var unmapped, untested []string
	for _, a := range declared {
		if !mapped[a] {
			unmapped = append(unmapped, a)
		}
		if !tested[a] {
			untested = append(untested, a)
		}
	}
	if len(unmapped) > 0 {
		sort.Strings(unmapped)
		t.Errorf("Action constants with no case arm in DecideAction:\n  %s\n"+
			"They silently take the defensive default (DecisionInboxApprove). Decide each "+
			"(autonomy_level × action) cell explicitly instead.", strings.Join(unmapped, "\n  "))
	}
	if len(untested) > 0 {
		sort.Strings(untested)
		t.Errorf("Action constants absent from TestPolicy_DecideAction_Matrix:\n  %s\n"+
			"Every cell of the matrix is the contract; an untested row can be changed without "+
			"anything going red.", strings.Join(untested, "\n  "))
	}
}

// decideActionArms slices DecideAction's outer switch into (action name → the
// lines of its case arm). Outer case arms are indented one tab inside the
// function; the inner per-level switches are indented two, which is what makes
// the split reliable without parsing Go.
func decideActionArms(t *testing.T) map[string][]string {
	t.Helper()
	src, err := os.ReadFile("types.go")
	if err != nil {
		t.Fatalf("read types.go: %v", err)
	}
	lines := strings.Split(string(src), "\n")
	start, end := -1, -1
	for i, l := range lines {
		if strings.Contains(l, "func (p Policy) DecideAction(") {
			start = i
		}
		if start >= 0 && strings.Contains(l, "Defensive default") {
			end = i
			break
		}
	}
	if start < 0 || end < 0 {
		t.Fatal("could not locate DecideAction's switch in types.go — update this test")
	}

	// Collect (names on the case line, body lines) in source order first, then
	// fan out to the map — several actions can share one arm (crew_create and
	// agent_create do), and each must be checked against that shared body.
	type arm struct {
		names []string
		body  []string
	}
	var collected []*arm
	var current *arm
	for i := start; i < end; i++ {
		l := lines[i]
		if strings.HasPrefix(l, "\tcase ") {
			current = &arm{names: decideCaseRe.FindAllString(l, -1)}
			collected = append(collected, current)
			continue
		}
		if current != nil {
			current.body = append(current.body, l)
		}
	}

	arms := map[string][]string{}
	for _, a := range collected {
		for _, n := range a.names {
			arms[n] = a.body
		}
	}
	return arms
}

// TestPolicy_EveryActionCoversEveryAutonomyLevel is the "no implicit default"
// gate, and it is a different question from TestPolicy_EveryActionIsMapped.
//
// That test asks whether an Action has a case arm at all. This one asks whether
// the arm DECIDES all four levels. An arm that switches on three of them falls
// through to the defensive DecisionInboxApprove for the fourth, which is
// invisible from the outside — the value is a legitimate answer for most rows,
// so nothing looks wrong until someone later relaxes the row and one level
// silently does not move. The failure mode this catches is precisely the one
// the defensive default's docstring admits it buys safety but not visibility
// for.
//
// An arm with no autonomy level in it at all is a deliberate flat row
// (persona_direct_write returns Rejected unconditionally) and passes.
func TestPolicy_EveryActionCoversEveryAutonomyLevel(t *testing.T) {
	arms := decideActionArms(t)
	if len(arms) < 10 {
		t.Fatalf("recovered only %d case arms from DecideAction — the parser is broken", len(arms))
	}
	want := map[string]bool{
		"AutonomyStrict": true, "AutonomyGuided": true,
		"AutonomyTrusted": true, "AutonomyFull": true,
	}
	levelRe := regexp.MustCompile(`\bAutonomy(Strict|Guided|Trusted|Full)\b`)

	for action, body := range arms {
		t.Run(action, func(t *testing.T) {
			seen := map[string]bool{}
			for _, l := range body {
				// Only the case labels of the inner switch decide levels; a
				// level named in a comment is prose, not a decision.
				code := l
				if idx := strings.Index(code, "//"); idx >= 0 {
					code = code[:idx]
				}
				for _, m := range levelRe.FindAllString(code, -1) {
					seen[m] = true
				}
			}
			if len(seen) == 0 {
				return // flat row, decided unconditionally
			}
			var missing []string
			for lvl := range want {
				if !seen[lvl] {
					missing = append(missing, lvl)
				}
			}
			if len(missing) > 0 {
				sort.Strings(missing)
				t.Errorf("%s decides only %d of 4 autonomy levels — missing %s.\n"+
					"The unlisted level falls through to the defensive DecisionInboxApprove, "+
					"which is indistinguishable from a row somebody chose. Decide it explicitly.",
					action, len(seen), strings.Join(missing, ", "))
			}
		})
	}
}

// TestPolicy_KnownActionsMatchesSource keeps IsKnownAction's map from falling
// behind the constants. The map is hand-written (a closed set callers can ask
// about); the constants are the contract. Deriving the check from source is the
// same trick TestPolicy_EveryActionIsMapped uses, for the same reason: a list
// that has to be remembered is a list that drifts.
func TestPolicy_KnownActionsMatchesSource(t *testing.T) {
	src, err := os.ReadFile("types.go")
	if err != nil {
		t.Fatalf("read types.go: %v", err)
	}
	var declared []Action
	for _, l := range strings.Split(string(src), "\n") {
		if m := actionValueRe.FindStringSubmatch(l); m != nil {
			declared = append(declared, Action(m[1]))
		}
	}
	if len(declared) < 10 {
		t.Fatalf("recovered only %d Action values from types.go — the parser is broken", len(declared))
	}
	for _, a := range declared {
		if !IsKnownAction(a) {
			t.Errorf("Action %q is declared but missing from knownActions — "+
				"a caller holding it as a string cannot tell it from a typo", a)
		}
	}
	if len(knownActions) != len(declared) {
		t.Errorf("knownActions has %d entries, types.go declares %d Action constants",
			len(knownActions), len(declared))
	}
}

// TestPolicy_StrictCrew_RefusesTheUnattendedWrites is the operator-facing half
// of the #1791 follow-on rows, stated as behaviour rather than as a table cell.
//
// On the routine path a held decision is a REFUSAL — nobody is attached to a
// 03:00 run to approve anything (internal/api/crewship_actions.go). So
// "InboxApprove at strict" means, in the only place these Actions are enforced
// today: a strict crew's routine does not write to issues and does not dispatch
// work. This test says that in those words, so relaxing either cell has to be a
// deliberate edit to a test that describes the promise, not a quiet change to a
// row in a matrix.
//
// escalation_create is deliberately NOT in this list and is asserted the other
// way: strict may still raise one. Refusing it would forbid asking for help at
// the level whose whole purpose is to ask before acting, while the unpoliced
// `notify` step reaches the same human anyway.
func TestPolicy_StrictCrew_RefusesTheUnattendedWrites(t *testing.T) {
	strict := Policy{AutonomyLevel: AutonomyStrict, BehaviorMode: BehaviorWarn}

	// page_write joins the list for the same reason and with the same cost,
	// stated in the PRD's terms: a strict crew's routines do not write panels,
	// and a panel nobody writes reads as `stale` rather than as a number nobody
	// checked (docs/prd/pages.md §4). The public PUT stays open to a human.
	for _, a := range []Action{ActionIssueWrite, ActionAssignmentCreate, ActionPageWrite} {
		got := strict.DecideAction(a)
		if got == DecisionAutoJournal || got == DecisionAutoLogJournal || got == DecisionAutoLogInbox {
			t.Errorf("strict × %s = %s — a strict crew must not do this unattended", a, got)
		}
		if got != DecisionInboxApprove {
			t.Errorf("strict × %s = %s, want %s (held for an operator, which the "+
				"unattended path reports as a refusal)", a, got, DecisionInboxApprove)
		}
	}

	if got := strict.DecideAction(ActionEscalationCreate); got != DecisionAutoLogJournal {
		t.Errorf("strict × %s = %s, want %s — asking a human for help is the one "+
			"thing 'ask before you act' cannot coherently forbid",
			ActionEscalationCreate, got, DecisionAutoLogJournal)
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

// TestPolicy_DecideBehaviorDeny_FullBlock_FailsClosed locks the
// defensive contract: the (full × block) combination is forbidden by
// Validate, but if validation is bypassed (manual SQL fix-up, schema
// drift) DecideBehaviorDeny must return the strictest block decision
// rather than silently relaxing to journal-only. "Silently let an
// agent through that the operator thought was blocked" is the failure
// mode we will not ship.
func TestPolicy_DecideBehaviorDeny_FullBlock_FailsClosed(t *testing.T) {
	p := Policy{AutonomyLevel: AutonomyFull, BehaviorMode: BehaviorBlock}
	if got := p.DecideBehaviorDeny(); got != DecisionBlockInbox {
		t.Errorf("full × block (bypassed validation): got %s, want %s (fail closed)", got, DecisionBlockInbox)
	}
}
