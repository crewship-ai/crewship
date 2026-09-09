package kinds

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/serviceconfig"
)

func TestCrewPrivateSnapshotCannotReplaceServices(t *testing.T) {
	marker := serviceconfig.Redacted
	remote := &CrewRemote{ServicesJSON: &marker}
	for _, services := range [][]Service{{}, {{Name: "cache", Image: "redis:7"}}} {
		doc := &CrewDocument{Spec: CrewSpec{Services: services}}
		if _, err := doc.updatePatch(remote); err == nil {
			t.Fatal("must not diff private services")
		}
	}
	// Metadata-only edits remain available.
	if _, err := (&CrewDocument{}).updatePatch(remote); err != nil {
		t.Fatal(err)
	}
}
