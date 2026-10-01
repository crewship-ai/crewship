package restricteddispatch

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
)

func TestRunProofPortableCapabilityBoundary(t *testing.T) {
	ids := []string{"allowed-origin"}
	proof := NewRunProof("synthetic-host-only-handle", ids)
	ids[0] = "foreign-origin"
	returned := proof.ContextIDs()
	returned[0] = "foreign-origin"
	if !reflect.DeepEqual(proof.ContextIDs(), []string{"allowed-origin"}) {
		t.Fatal("caller mutation widened completed sources")
	}
	if _, err := json.Marshal(proof); !errors.Is(err, access.ErrDenied) {
		t.Fatalf("capability JSON publication: %v", err)
	}
	for _, invalid := range []RunProof{{}, NewRunProof("synthetic-host-only-handle", nil), NewRunProof("", []string{"allowed-origin"})} {
		if _, err := invalid.Seal(); !errors.Is(err, access.ErrDenied) {
			t.Fatalf("incomplete capability persisted: %v", err)
		}
	}
}
