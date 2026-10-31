package mooring

import (
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

func TestNativeProposalIdempotencyIsBoundToAuthenticatedRun(t *testing.T) {
	s, _, _, scope, acting, _ := nativeProposalFixture(t)
	human := *acting
	lease, _, err := s.Initialize(t.Context(), scope, human, []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	*acting = nativeProposalAgent
	one, err := s.Propose(t.Context(), scope, 1, "request-1", "Review", []byte(`{"v":"one"}`))
	if err != nil {
		t.Fatal(err)
	}
	acting.SessionID = "native-agent:run2"
	two, err := s.Propose(t.Context(), scope, 1, "request-1", "Review", []byte(`{"v":"one"}`))
	if err != nil || two.Replayed || two.ID == one.ID {
		t.Fatalf("two authenticated runs shared a proposal: first=%+v second=%+v err=%v", one, two, err)
	}
	if two.AuthorActorID != one.AuthorActorID || two.AuthorActorID != nativeProposalAgent.ActorID {
		t.Fatalf("run scoping changed the display author: first=%+v second=%+v", one, two)
	}
	// A run's exact retry still works after a human advances the document.
	*acting = human
	if _, err := s.Save(t.Context(), lease, 1, []byte(`{"v":"human"}`)); err != nil {
		t.Fatal(err)
	}
	*acting = EditLeasePrincipal{ActorID: nativeProposalAgent.ActorID, SessionID: "native-agent:run2"}
	retry, err := s.Propose(t.Context(), scope, 1, "request-1", "Review", []byte(`{"v":"one"}`))
	if err != nil || !retry.Replayed || retry.ID != two.ID {
		t.Fatalf("own retry failed: %+v %v", retry, err)
	}
	if _, err := s.Propose(t.Context(), scope, 1, "request-1", "Review", []byte(`{"v":"changed"}`)); !errors.Is(err, artifacts.ErrVersionConflict) {
		t.Fatalf("changed retry accepted: %v", err)
	}
}
