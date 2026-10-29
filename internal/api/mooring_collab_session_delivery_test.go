package api

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/mooring"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

func TestCollaborationSessionDeliveryRequiresOriginalJoin(t *testing.T) {
	var absent collaborationHumanJoined
	if _, err := absent.renewDelivery(context.Background(), nil, mooring.OwnerLease{}, protocol.DeliveryRenew{}); err == nil {
		t.Fatal("manufactured join renewed delivery")
	}
}
