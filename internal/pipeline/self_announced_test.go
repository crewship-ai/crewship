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

func TestAnnouncesOwnCompletion_UnconditionalWorkspaceNotice(t *testing.T) {
	if !announcesOwnCompletion(announcingDSL("routines.completed", "workspace", "")) {
		t.Error("a routine that unconditionally tells the workspace it finished announces its own completion")
	}
	// Empty `to` means workspace — the documented default.
	if !announcesOwnCompletion(announcingDSL("routines.completed", "", "")) {
		t.Error("an omitted target defaults to workspace and must count")
	}
}

func TestAnnouncesOwnCompletion_ConditionalDoesNotCount(t *testing.T) {
	// The step may not run. Suppressing on a maybe would mean a completed
	// run reports nothing at all whenever the condition is false.
	if announcesOwnCompletion(announcingDSL("routines.completed", "workspace", "{{ inputs.x }} == \"y\"")) {
		t.Error("a conditional notice cannot be relied on to have fired")
	}
}

func TestAnnouncesOwnCompletion_NarrowerAudienceDoesNotCount(t *testing.T) {
	// A notice to one role reaches fewer people than the journal's
	// workspace-wide one. Suppressing would silence everybody else.
	for _, to := range []string{"role:OWNER", "trigger", "user:u1", "crew:ops"} {
		if announcesOwnCompletion(announcingDSL("routines.completed", to, "")) {
			t.Errorf("to=%q reaches a narrower audience and must not suppress the workspace notice", to)
		}
	}
}

func TestAnnouncesOwnCompletion_DifferentCategoryDoesNotCount(t *testing.T) {
	// A routine that reports its result under some other category has not
	// said "I finished" in the row this would suppress.
	for _, c := range []string{"", "chat.replies", "routines.failed", "security"} {
		if announcesOwnCompletion(announcingDSL(c, "workspace", "")) {
			t.Errorf("category %q must not suppress routines.completed", c)
		}
	}
}

func TestAnnouncesOwnCompletion_NoNotifyStepAtAll(t *testing.T) {
	// The case the journal notification exists for.
	dsl := &DSL{DSLVersion: "1.0", Name: "p", Steps: []Step{
		{ID: "fetch", Type: StepHTTP, HTTP: &HTTPStep{Method: "GET", URL: "https://x.test"}},
	}}
	if announcesOwnCompletion(dsl) {
		t.Error("a routine with no notify step must keep getting the journal notice")
	}
	if announcesOwnCompletion(nil) {
		t.Error("no DSL means no claim either way — must not suppress")
	}
}
