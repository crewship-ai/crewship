package policy

// ApprovalMode is the harbormaster HITL gate mode a run is dispatched with.
// Kept as plain strings (not the harbormaster.Mode enum) so this package
// stays free of a harbormaster import; the orchestrator/harbormaster layer
// maps the string back to its enum.
const (
	ApprovalModeNone  = "none"  // gate short-circuits — rules never consulted
	ApprovalModeAsync = "async" // rules consulted; matches log/enqueue, run continues
	ApprovalModeSync  = "sync"  // rules consulted; matches block until a human decides
)

// ApprovalModeForLevel maps a crew's autonomy_level to the harbormaster gate
// mode the request-builder stamps onto every dispatched run (#810). Before
// this, ApprovalMode was never set → always ModeNone → the gate was dead on
// every path.
//
// The mapping mirrors the autonomy semantics documented on the AutonomyLevel
// constants:
//
//   - strict / guided → sync  ("every action needs Approve" / "writes need OK")
//   - trusted         → async ("most actions auto; writes log to inbox")
//   - full            → none  ("autonomous; journal-only")
//
// Note: the baked-in default harbormaster rule set does not match the
// "agent_run" tool, so raising a default (guided) crew to sync does not block
// today's runs — no rule fires, the gate returns Approved. What changes is
// that the gate is now LIVE: once an operator-authored rule matches agent_run
// (or a custom rule set is wired), the gate fires instead of being bypassed
// before the rules are ever read. Unknown levels fail safe to sync.
func ApprovalModeForLevel(level AutonomyLevel) string {
	switch level {
	case AutonomyFull:
		return ApprovalModeNone
	case AutonomyTrusted:
		return ApprovalModeAsync
	case AutonomyStrict, AutonomyGuided:
		return ApprovalModeSync
	default:
		return ApprovalModeSync
	}
}
