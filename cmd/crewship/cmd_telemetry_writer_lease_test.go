//go:build !clionly

package main

import (
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
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

// Read-only local commands (sessions list, list-users --local, memory show and
// log --local) are what an operator runs on a live host during an incident.
// They must work beside the server's writer lease, and must not write.
func TestReadOnlyLocalCommandsWorkBesideALiveWriter(t *testing.T) {
	t.Setenv("CREWSHIP_DATA_DIR", t.TempDir())
	t.Setenv("CREWSHIP_SERVER", "")
	t.Setenv("CREWSHIP_PROFILE", "")
	t.Setenv("DATABASE_URL", "")
	previous := cliCfg
	cliCfg = &cli.CLIConfig{}
	t.Cleanup(func() { cliCfg = previous })
	dir, err := database.DefaultDataDir()
	if err != nil {
		t.Fatal(err)
	}
	owner, err := database.Open(dir.DatabaseURL(), database.WithManagedWAL())
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if _, err = owner.Exec(`CREATE TABLE probe(v INTEGER)`); err != nil {
		t.Fatal(err)
	}

	reader, err := openReadOnlyLocalDB(adminSessionsListCmd, "crewship admin sessions list", "")
	if err != nil {
		t.Fatalf("read-only command refused beside a live writer: %v", err)
	}
	defer reader.Close()
	var n int
	if err = reader.QueryRow(`SELECT COUNT(*) FROM probe`).Scan(&n); err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, err = reader.Exec(`INSERT INTO probe(v) VALUES (1)`); err == nil {
		t.Fatal("a read-only handle wrote beside the live writer")
	}

	writer, err := openGatedLocalDB(adminSessionsListCmd, "crewship admin reset-password", "")
	if writer != nil {
		writer.Close()
	}
	if !errors.Is(err, writerlease.ErrHeld) {
		t.Fatalf("a mutating command opened beside a live writer: %v", err)
	}
}
