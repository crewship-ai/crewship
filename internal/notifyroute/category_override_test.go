package notifyroute

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/notify"
)

// The router derived a notification's category from the inbox KIND alone.
// That works for producers whose kind says what happened (a waitpoint is an
// approval), and fails for the one producer whose kind cannot: a routine's
// notify step writes kind "message" whatever it is reporting, so every
// routine notice — a failure, a digest, a deploy result — arrived as
// chat.replies.
//
// A producer may now state the category outright. The kind mapping stays the
// default, so nothing that did not opt in changes.

func TestCategoryForItem_PrefersWhatTheProducerDeclared(t *testing.T) {
	got := categoryForItem(inbox.Item{
		Kind:     inbox.KindMessage,
		Category: notify.CategoryRoutinesFailed,
	})
	if got != notify.CategoryRoutinesFailed {
		t.Errorf("category = %q, want the declared one — the kind mapping would have said %q",
			got, notify.CategoryForKind(inbox.KindMessage))
	}
}

func TestCategoryForItem_FallsBackToTheKindMapping(t *testing.T) {
	// Every producer that has not opted in must be untouched.
	for _, kind := range []string{
		inbox.KindMessage, inbox.KindWaitpoint, inbox.KindEscalation,
		inbox.KindFailedRun, inbox.KindScheduleMissed,
	} {
		want := notify.CategoryForKind(kind)
		if got := categoryForItem(inbox.Item{Kind: kind}); got != want {
			t.Errorf("kind %q: category = %q, want %q", kind, got, want)
		}
	}
}

func TestCategoryForItem_IgnoresACategoryThatIsNotReal(t *testing.T) {
	// Authoring validates this, but the field is a plain string on a leaf
	// package that cannot import the category list, and other producers
	// will set it in future. A bogus value must degrade to the kind mapping
	// rather than route into a category nothing matches — which would
	// deliver to nobody while every log line said success.
	got := categoryForItem(inbox.Item{Kind: inbox.KindMessage, Category: "routines.exploded"})
	if got != notify.CategoryForKind(inbox.KindMessage) {
		t.Errorf("category = %q, want the kind fallback for an unknown value", got)
	}
}

func TestCategoryForItem_IgnoresTheMuteSentinel(t *testing.T) {
	// "*" means "mute everything" in a preference row. Routing INTO it
	// would match every mute rule and silently drop the notice.
	if got := categoryForItem(inbox.Item{Kind: inbox.KindMessage, Category: notify.CategoryMuteAll}); got == notify.CategoryMuteAll {
		t.Error(`"*" must never become a delivery category`)
	}
}
