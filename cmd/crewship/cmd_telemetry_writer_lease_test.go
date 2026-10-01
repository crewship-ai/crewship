//go:build !clionly

package main

import (
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/writerlease"
)

func TestTelemetryMutationRefusesAWriterOwnedDatabase(t *testing.T) {
	t.Setenv("CREWSHIP_DATA_DIR", t.TempDir())
	dir, err := database.DefaultDataDir()
	if err != nil {
		t.Fatal(err)
	}
	owner, err := database.Open(dir.DatabaseURL(), database.WithManagedWAL())
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	other, err := openLocalDB(t.Context())
	if other != nil {
		other.Close()
	}
	if !errors.Is(err, writerlease.ErrHeld) {
		t.Fatalf("offline telemetry accepted a live writer: %v", err)
	}
}
