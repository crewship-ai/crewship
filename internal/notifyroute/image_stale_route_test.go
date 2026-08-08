package notifyroute

// #1845 — the last link: a detected stale crew image has to reach a human.
//
// Detection, journalling and de-duplication already existed for the SIDECAR
// half and were already routed (journal.EntrySidecarStale → agents.error). The
// container-image half was journalled and dropped. These tests pin where it
// goes and, just as importantly, where it does NOT.

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/notify"
)

// TestImageStaleRoutesToSystemHealth.
//
// system.health rather than a fifteenth category, and that is a decision:
//
//   - The taxonomy is closed on purpose. Its last change (migration v169) had
//     to rewrite every stored user_notification_prefs cell and every channel
//     allowlist to keep opted-in users opted in, and the CHECK constraint on
//     user_notification_prefs.category is generated from notify.AllCategories
//     at v169 time — so a new category needs its own widening migration or it
//     is unstorable on every instance that has already migrated.
//   - system.health was the ONE category in the whole matrix with no journal
//     producer: "the instance itself reported a problem" was a switchable row
//     that the journal bridge could never deliver against. "Your crews are
//     running images three releases old" is precisely that sentence.
//
// system.health rather than agents.error, which is where the sidecar signal
// goes: a stale sidecar is one agent's container misbehaving now; a stale
// image is an instance-wide hygiene fact about a fleet nobody has recycled.
// Someone who mutes agent errors to stop run-failure noise must not thereby
// mute "your whole fleet is behind".
func TestImageStaleRoutesToSystemHealth(t *testing.T) {
	if got := CategoryForJournalType(journal.EntryImageStale); got != notify.CategorySystemHealth {
		t.Errorf("CategoryForJournalType(image.stale) = %q, want %q", got, notify.CategorySystemHealth)
	}
}

// TestImageStaleAndSidecarStaleAreRoutedApart is the other half of the design
// decision, and the one a later refactor is most likely to undo: the two
// conditions look alike enough that collapsing them onto one category is the
// obvious "simplification". It would mean a single mute silences both the
// urgent one and the hygiene one.
func TestImageStaleAndSidecarStaleAreRoutedApart(t *testing.T) {
	image := CategoryForJournalType(journal.EntryImageStale)
	sidecar := CategoryForJournalType(journal.EntrySidecarStale)
	if image == sidecar {
		t.Errorf("image.stale and sidecar.stale both route to %q — one mute would silence "+
			"an actively-degraded sidecar along with a merely-out-of-date image", image)
	}
	if sidecar != notify.CategoryAgentsError {
		t.Errorf("sidecar.stale = %q, want %q (unchanged by #1845)", sidecar, notify.CategoryAgentsError)
	}
}

// TestImageStaleSeverityIsWarnNotError pins the priority the router compares
// against a channel's min_priority floor. severityPriority maps warn→medium
// and error→high; an image that is merely behind must not push past a floor
// set to keep the urgent things through.
func TestImageStaleSeverityIsWarnNotError(t *testing.T) {
	if got := severityPriority(journal.SeverityWarn); got != "medium" {
		t.Fatalf("severityPriority(warn) = %q, want medium", got)
	}
	if got := severityPriority(journal.SeverityError); got != "high" {
		t.Fatalf("severityPriority(error) = %q, want high", got)
	}
}
