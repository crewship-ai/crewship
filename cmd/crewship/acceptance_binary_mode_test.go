package main

import (
	"debug/buildinfo"
	"os"
	"testing"
)

// The lane must prove the executed CLI is instrumented, rather than relying
// on -race on the outer test process. The same artifact drives auth pairing
// and conversation acceptance.
func TestAcceptanceBinaryBuildMode(t *testing.T) {
	info, err := buildinfo.ReadFile(buildCrewshipBinary(t))
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("TEST_CREWSHIP_CLI_RACE") != "1" {
		return
	}
	for _, setting := range info.Settings {
		if setting.Key == "-race" && setting.Value == "true" {
			return
		}
	}
	t.Fatal("dedicated subprocess race lane executed a CLI without -race")
}
