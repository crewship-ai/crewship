package api

import (
	"github.com/crewship-ai/crewship/internal/mooring"
	"testing"
)

func TestCollaborationOriginGenesisRejectsUnverifiedStartup(t *testing.T) {
	for _, cell := range []*collaborationSessionStore{nil, {}} {
		if err := cell.initializeNode(nil, mooring.OwnerLease{}, []byte("untrusted"), nil); err == nil {
			t.Fatal("unverified startup acquired node lineage")
		}
	}
}
