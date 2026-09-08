package manifest

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/crewship-ai/crewship/internal/serviceconfig"
)

func TestAutoManagedRefusesPrivateSnapshot(t *testing.T) {
	if _, err := expandAutoCredentialsInCrewSpec(&CrewSpec{}, serviceconfig.Redacted); err == nil {
		t.Fatal("must not regenerate credentials from a private snapshot")
	}
}

func TestExportCrewRefusesPrivateSnapshot(t *testing.T) {
	stub := newCovStub()
	marker := serviceconfig.Redacted
	body, err := json.Marshal([]CrewResponse{{ID: "private", Slug: "private", ServicesJSON: &marker}})
	if err != nil {
		t.Fatal(err)
	}
	stub.on("GET", "/api/v1/crews", 200, string(body))
	if result, err := ExportCrew(context.Background(), NewClient(stub), "private", DefaultExportOptions()); err == nil || result != "" {
		t.Fatal("must not export a partial manifest from private configuration")
	}
}
