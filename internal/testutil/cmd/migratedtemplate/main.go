// migratedtemplate is the source-built test fixture helper. Its caller owns the
// private output directory and charges compilation and migration to its test.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/testutil/migratedtemplate"
)

type receipt struct {
	Fingerprint string `json:"fingerprint"`
	GoVersion   string `json:"go_version"`
	RaceEnabled bool   `json:"race_enabled"`
}

func run(args []string) error {
	flags := flag.NewFlagSet("migratedtemplate", flag.ContinueOnError)
	output := flags.String("output", "", "fresh private output file")
	expected := flags.String("fingerprint", "", "parent's compiled migration fingerprint")
	goVersion := flags.String("go-version", "", "parent's Go toolchain version")
	check := flags.Bool("check", false, "verify the source-built helper without opening a database")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *expected == "" || *goVersion == "" {
		return fmt.Errorf("exact fingerprint and Go version are required")
	}
	if migratedtemplate.RaceInstrumented {
		return fmt.Errorf("fixture helper must be built with -race=false")
	}
	actual, err := database.MigrationFingerprint()
	if err != nil {
		return err
	}
	if actual != *expected || runtime.Version() != *goVersion {
		return fmt.Errorf("normal fixture helper does not match the parent's migration inputs and Go toolchain")
	}
	if *check {
		if *output != "" {
			return fmt.Errorf("verification cannot name an output")
		}
	} else {
		if *output == "" {
			return fmt.Errorf("fresh private output file is required")
		}
		if err := migratedtemplate.Build(context.Background(), *output); err != nil {
			return err
		}
	}
	return json.NewEncoder(os.Stdout).Encode(receipt{Fingerprint: actual, GoVersion: runtime.Version()})
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
