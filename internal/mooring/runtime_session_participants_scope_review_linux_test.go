//go:build linux

package mooring

import (
	"encoding/json"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
	"testing"
)

func TestReviewParticipantTransportNestedScopeCorrelation(t *testing.T) {
	f := sessionOriginsFixture(t, "participant-notifications", false)
	_, input := participantTransportRequests(t, f)
	var message protocol.StatusRequest
	if err := json.Unmarshal(input.Message, &message); err != nil {
		t.Fatal(err)
	}
	message.Scope.ArtifactID = "foreign-document"
	input.Message, _ = json.Marshal(message)
	if err := f.runtime.SendParticipantInput(t.Context(), f.activation, input); err == nil {
		t.Fatal("foreign inner message scope crossed actual inherited participant transport")
	}
}
