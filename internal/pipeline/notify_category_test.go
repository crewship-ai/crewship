package pipeline

import (
	"context"
	"strings"
	"testing"
)

// A routine's notify step could say anything and never say what it WAS.
//
// Every notice it emitted was written as inbox kind "message", which the
// router maps to the chat.replies category — so "the nightly deploy failed"
// and "here is your weekly digest" arrived under the same label, and the
// 14-category preference matrix people tune their notifications with was
// invisible to the one author who knows what the event actually is.
//
// The step can now declare its category. It is a routing label, not a
// privilege: it decides which row of a recipient's matrix the notice is
// matched against, and every row is still opt-in per user.

func notifyCategoryDSL(category string) *DSL {
	return &DSL{
		DSLVersion: "1.0",
		Name:       "deploy-watch",
		Steps: []Step{{
			ID:   "tell",
			Type: StepNotify,
			Notify: &NotifyStep{
				To:       "workspace",
				Title:    "Deploy failed",
				Category: category,
			},
		}},
	}
}

func TestValidate_NotifyCategory_AcceptsARealCategory(t *testing.T) {
	if err := Validate(notifyCategoryDSL("routines.failed"), nil, nil); err != nil {
		t.Fatalf("routines.failed must be accepted: %v", err)
	}
}

func TestValidate_NotifyCategory_EmptyKeepsTheOldDefault(t *testing.T) {
	// Every routine written before this field existed must keep working
	// unchanged, so an absent category is not an error.
	if err := Validate(notifyCategoryDSL(""), nil, nil); err != nil {
		t.Fatalf("an omitted category must stay valid: %v", err)
	}
}

func TestValidate_NotifyCategory_RejectsOneThatDoesNotExist(t *testing.T) {
	// Caught at author time, because the failure mode otherwise is silent:
	// a category nothing matches routes to nobody, and the routine looks
	// like it ran fine.
	err := Validate(notifyCategoryDSL("routines.exploded"), nil, nil)
	if err == nil {
		t.Fatal("an unknown category must be rejected at author time")
	}
	if !strings.Contains(err.Error(), "routines.exploded") {
		t.Errorf("the error must name the bad category, got: %v", err)
	}
}

func TestValidate_NotifyCategory_RejectsTheMuteSentinel(t *testing.T) {
	// "*" is the mute-everything marker in a preference row, not a real
	// category. Emitting into it would be meaningless, and ValidCategory
	// already excludes it — this pins that the step agrees.
	if err := Validate(notifyCategoryDSL("*"), nil, nil); err == nil {
		t.Fatal(`"*" is a preference sentinel, not a category a step may emit`)
	}
}

func TestNotifyStep_CategoryReachesTheInboxItem(t *testing.T) {
	// The declaration is only worth anything if it survives to the item the
	// router reads. Nothing persists it — the resolved category is stored on
	// the delivery row — so this in-flight hand-off is the whole mechanism.
	fake := &fakeInboxNotifier{}
	e := notifyExecutor(fake)

	step := Step{
		ID:   "tell",
		Type: StepNotify,
		Notify: &NotifyStep{
			To:       "workspace",
			Title:    "Deploy failed",
			Category: "routines.failed",
		},
	}
	if _, _, _, err := e.runNotifyStep(context.Background(), step,
		RenderContext{}, notifyRunInput("ws_1", ""), "run_1"); err != nil {
		t.Fatalf("runNotifyStep: %v", err)
	}
	if len(fake.items) != 1 {
		t.Fatalf("want 1 inbox item, got %d", len(fake.items))
	}
	if got := fake.items[0].Category; got != "routines.failed" {
		t.Errorf("item.Category = %q, want the step's declared category", got)
	}
}

func TestNotifyStep_NoCategoryLeavesTheItemUnlabelled(t *testing.T) {
	// An unset category must stay unset rather than being defaulted here —
	// the fallback (kind → category) belongs to the router, which is the
	// one place that mapping lives.
	fake := &fakeInboxNotifier{}
	e := notifyExecutor(fake)

	step := Step{
		ID:     "tell",
		Type:   StepNotify,
		Notify: &NotifyStep{To: "workspace", Title: "FYI"},
	}
	if _, _, _, err := e.runNotifyStep(context.Background(), step,
		RenderContext{}, notifyRunInput("ws_1", ""), "run_1"); err != nil {
		t.Fatalf("runNotifyStep: %v", err)
	}
	if got := fake.items[0].Category; got != "" {
		t.Errorf("item.Category = %q, want empty so the router decides", got)
	}
}
