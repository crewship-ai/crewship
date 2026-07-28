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
func announcesOwnCompletion(dsl *DSL, stepOutputs map[string]string) bool {
	if dsl == nil {
		return false
	}
	for i := range dsl.Steps {
		st := dsl.Steps[i]
		if !stepAnnouncesCompletion(st) {
			continue
		}
		// And it has to have actually LANDED. The first version stopped at
		// the DSL text, which describes intent, not outcome: a notify step
		// drops its notice once the per-recipient cap is reached, and its
		// inbox write is best-effort behind a timeout. A routine that emitted
		// progress notices first could therefore lose its own completion
		// message AND have the generic journal notification suppressed behind
		// it — the run reports COMPLETED and no channel receives anything.
		//
		// The step records what happened in its own output, so the outcome is
		// available at the moment the claim is made.
		if noticeLanded(stepOutputs[st.ID]) {
			return true
		}
	}
	return false
}

// noticeLanded reports whether a notify step's recorded output means the
// notice reached someone.
//
// The markers are the step's own vocabulary (runner_notify.go): "capped",
// "error", "skipped" and "preview" all mean nobody was written to.
// "degraded" DID reach someone — the target could not be resolved and the
// notice fell back to a workspace notice, which is precisely the audience the
// journal notification would have reached.
func noticeLanded(output string) bool {
	if !strings.HasPrefix(output, "notified:") {
		return false // the step did not run, or is not a notify step
	}
	switch strings.TrimPrefix(output, "notified:") {
	case "capped", "error", "skipped", "preview":
		return false
	}
	return true
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
