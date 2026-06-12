package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// browserOpen is intentionally not driven here — it would launch a real
// browser via `open`/`xdg-open`. RunE coverage uses --print-only.

func TestOpenRunE_PrintOnly(t *testing.T) {
	covSetupCli10(t, "")
	cliCfg = &cli.CLIConfig{Server: "http://dev1.local:8080"}
	setFlagCovCli10(t, openCmd, "print-only", "true")

	out, err := captureStdoutCovCli10(t, func() error {
		return openCmd.RunE(openCmd, []string{"mission", "MIS-42"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if strings.TrimSpace(out) != "http://dev1.local:8080/missions/MIS-42/timeline" {
		t.Errorf("print-only output = %q", out)
	}
}

func TestOpenRunE_UnknownResourceErrors(t *testing.T) {
	covSetupCli10(t, "")
	cliCfg = &cli.CLIConfig{Server: "http://dev1.local:8080"}
	setFlagCovCli10(t, openCmd, "print-only", "true")

	err := openCmd.RunE(openCmd, []string{"bogus-resource"})
	if err == nil || !strings.Contains(err.Error(), `unknown resource "bogus-resource"`) {
		t.Errorf("expected unknown-resource error, got %v", err)
	}
}

func TestBuildOpenURLCov_RemainingBranches(t *testing.T) {
	base := "http://localhost:8080"
	cases := []struct {
		name string
		args []string
		want string
		err  string
	}{
		{"crews list", []string{"crews"}, base + "/crews", ""},
		{"crew missing arg", []string{"crew"}, "", "requires exactly 1 argument"},
		{"mission missing arg", []string{"mission"}, "", "requires exactly 1 argument"},
		{"agent two args", []string{"agent", "a", "b"}, "", "requires exactly 1 argument(s), got 2"},
		{"issues too many args", []string{"issues", "a", "b"}, "", "accepts 0-1 argument"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := buildOpenURL(base, c.args)
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Errorf("want error containing %q, got %v (url %q)", c.err, err, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if got != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
}
