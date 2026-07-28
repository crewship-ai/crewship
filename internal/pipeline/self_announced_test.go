package pipeline

import "testing"

// Every completed run produced TWO notifications in the same category, to the
// same channel, for the same person:
//
//	Fetch and report              ← the routine's own notify step
//	example.com resolves to …
//
//	Pipeline demo-fetch-and-report completed   ← the journal's run.completed
//
// The journal one exists so that a routine which says nothing still reports
// that it finished. When the routine already announced its own completion,
// it is the same news twice, and the second telling carries less.
//
// This is the rule the bridge already applies to pipeline.run.failed, whose
// comment says mapping both producers "would deliver the same failure twice".
// Completion had the same problem and no equivalent guard, because whether it
// applies depends on the routine rather than on the entry type.
//
// The test is narrow on purpose. Suppression is only correct where the two
// notifications genuinely overlap.

func announcingDSL(category, to, ifExpr string) *DSL {
	return &DSL{
		DSLVersion: "1.0",
		Name:       "p",
		Steps: []Step{{
			ID:     "tell",
			Type:   StepNotify,
			If:     ifExpr,
			Notify: &NotifyStep{To: to, Title: "done", Category: category},
		}},
	}
}

// landed is what a notify step's output looks like when the notice was
// actually written.
func landed(stepID string) map[string]string {
	return map[string]string{stepID: "notified:run_1:" + stepID}
}

func TestAnnouncesOwnCompletion_UnconditionalWorkspaceNotice(t *testing.T) {
	if !announcesOwnCompletion(announcingDSL("routines.completed", "workspace", ""), landed("tell")) {
		t.Error("a routine that unconditionally tells the workspace it finished announces its own completion")
	}
	// Empty `to` means workspace — the documented default.
	if !announcesOwnCompletion(announcingDSL("routines.completed", "", ""), landed("tell")) {
		t.Error("an omitted target defaults to workspace and must count")
	}
}

// The claim was computed from the DSL TEXT alone and never reconciled with
// whether the notice was actually delivered. A notify step drops its notice
// outright once the per-recipient cap is reached, and its inbox write is
// best-effort behind a timeout — in both cases the step records what
// happened in its own output. So a routine that emits several notices before
// its completion notice could hit the cap, lose its own message, AND have the
// generic journal notification suppressed behind it: the run shows COMPLETED
// and no channel receives anything.
//
// The file's own comment named that outcome as the thing to avoid. Nothing
// prevented it.
func TestAnnouncesOwnCompletion_ADroppedNoticeIsNotAnAnnouncement(t *testing.T) {
	dsl := announcingDSL("routines.completed", "workspace", "")
	for _, outcome := range []string{
		"notified:capped",  // per-recipient soft cap reached
		"notified:error",   // the inbox write failed or timed out
		"notified:skipped", // no recipient resolved
		"notified:preview", // dry run
		"",                 // the step never ran
	} {
		outputs := map[string]string{"tell": outcome}
		if announcesOwnCompletion(dsl, outputs) {
			t.Errorf("output %q means the notice did not reach anyone; the journal notification must still go out", outcome)
		}
	}
}

func TestAnnouncesOwnCompletion_DegradedStillCounts(t *testing.T) {
	// `notified:degraded` means the target could not be resolved and the
	// notice fell back to a WORKSPACE notice — which is exactly the audience
	// the journal notification would have reached. It landed.
	outputs := map[string]string{"tell": "notified:degraded:run_1:tell"}
	if !announcesOwnCompletion(announcingDSL("routines.completed", "workspace", ""), outputs) {
		t.Error("a degraded notice still reached the workspace and must count")
	}
}

func TestAnnouncesOwnCompletion_OneLandedNoticeIsEnough(t *testing.T) {
	// A routine may have several qualifying steps; one that landed is an
	// announcement even if another was capped.
	dsl := announcingDSL("routines.completed", "workspace", "")
	dsl.Steps = append(dsl.Steps, Step{
		ID: "tell2", Type: StepNotify,
		Notify: &NotifyStep{To: "workspace", Title: "also", Category: "routines.completed"},
	})
	outputs := map[string]string{"tell": "notified:capped", "tell2": "notified:run_1:tell2"}
	if !announcesOwnCompletion(dsl, outputs) {
		t.Error("one notice that landed is an announcement")
	}
}

func TestAnnouncesOwnCompletion_ConditionalDoesNotCount(t *testing.T) {
	// The step may not run. Suppressing on a maybe would mean a completed
	// run reports nothing at all whenever the condition is false.
	if announcesOwnCompletion(announcingDSL("routines.completed", "workspace", "{{ inputs.x }} == \"y\""), landed("tell")) {
		t.Error("a conditional notice cannot be relied on to have fired")
	}
}

func TestAnnouncesOwnCompletion_NarrowerAudienceDoesNotCount(t *testing.T) {
	// A notice to one role reaches fewer people than the journal's
	// workspace-wide one. Suppressing would silence everybody else.
	for _, to := range []string{"role:OWNER", "trigger", "user:u1", "crew:ops"} {
		if announcesOwnCompletion(announcingDSL("routines.completed", to, ""), landed("tell")) {
			t.Errorf("to=%q reaches a narrower audience and must not suppress the workspace notice", to)
		}
	}
}

func TestAnnouncesOwnCompletion_DifferentCategoryDoesNotCount(t *testing.T) {
	// A routine that reports its result under some other category has not
	// said "I finished" in the row this would suppress.
	for _, c := range []string{"", "chat.replies", "routines.failed", "security"} {
		if announcesOwnCompletion(announcingDSL(c, "workspace", ""), landed("tell")) {
			t.Errorf("category %q must not suppress routines.completed", c)
		}
	}
}

func TestAnnouncesOwnCompletion_NoNotifyStepAtAll(t *testing.T) {
	// The case the journal notification exists for.
	dsl := &DSL{DSLVersion: "1.0", Name: "p", Steps: []Step{
		{ID: "fetch", Type: StepHTTP, HTTP: &HTTPStep{Method: "GET", URL: "https://x.test"}},
	}}
	if announcesOwnCompletion(dsl, landed("fetch")) {
		t.Error("a routine with no notify step must keep getting the journal notice")
	}
	if announcesOwnCompletion(nil, nil) {
		t.Error("no DSL means no claim either way — must not suppress")
	}
}
