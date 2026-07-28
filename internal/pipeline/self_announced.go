package pipeline

import (
	"strings"

	"github.com/crewship-ai/crewship/internal/notify"
)

// announcesOwnCompletion reports whether this routine reliably tells the whole
// workspace that it finished, using the same category the journal's
// run-completed notification routes to.
//
// When it does, that journal notification is the same news twice — the
// routine's own message says what happened AND carries its result, the generic
// one says only that something ended. The bridge already applies this rule to
// pipeline.run.failed, whose comment notes that mapping both producers "would
// deliver the same failure twice"; completion had the same problem and no
// guard, because whether it applies depends on the routine rather than on the
// entry type. So the routine answers the question and the answer rides along
// on the journal entry.
//
// Deliberately conservative: suppression is only correct where the two
// notifications genuinely overlap, and a false positive means a completed run
// reports nothing at all.
//
//   - Conditional steps do not count. An `if` may be false, and "it might have
//     notified" is not a reason to stay quiet.
//   - Only workspace-wide targets count. A notice to one role or one user
//     reaches fewer people than the journal's does, so suppressing it would
//     silence everyone else.
//   - Only the matching category counts. Reporting a result under some other
//     category is not saying "I finished" in the row being suppressed.
func announcesOwnCompletion(dsl *DSL) bool {
	if dsl == nil {
		return false
	}
	for i := range dsl.Steps {
		if stepAnnouncesCompletion(dsl.Steps[i]) {
			return true
		}
	}
	return false
}

func stepAnnouncesCompletion(st Step) bool {
	if st.Type != StepNotify || st.Notify == nil {
		return false
	}
	if strings.TrimSpace(st.If) != "" {
		return false // may not run
	}
	if st.Notify.Category != notify.CategoryRoutinesCompleted {
		return false
	}
	// Empty means workspace — the documented default.
	to := strings.TrimSpace(st.Notify.To)
	return to == "" || to == "workspace"
}
