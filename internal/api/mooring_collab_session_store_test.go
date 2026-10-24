package api

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/mooring"
)

// These default-build denials do not replace the separately tagged actual
// process/SQL proof. An inactive or copied empty startup cannot mint authority.
func TestCollaborationSessionInactiveFactoriesRejectUnverifiedState(t *testing.T) {
	if _, err := newCollaborationSessionStore(false, nil, nil, nil, nil, mooring.SessionKey{}, mooring.RuntimeVersions{}); err == nil {
		t.Fatal("disabled session store accepted")
	}
	cell := &collaborationSessionStore{}
	if err := cell.initialize(nil, mooring.OwnerLease{}, []byte("untrusted input")); err == nil {
		t.Fatal("missing authenticated request initialized content")
	}
	if _, err := newCollaborationSessionHumanRegistry(t.Context(), cell, &mooring.SessionActivation{}, mooring.OwnerLease{}, &collaborationAdmissionTestClock{}, 1); err == nil {
		t.Fatal("empty copied activation minted a registry")
	}
	if _, err := newCollaborationSessionHumanRegistry(t.Context(), nil, nil, mooring.OwnerLease{}, nil, 1); err == nil {
		t.Fatal("missing startup minted a registry")
	}
}
