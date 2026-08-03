package main

// The delete confirmation has to ask the question the command now answers.
//
// `crewship crew delete` has always confirmed, but that prompt was written when
// deleting a crew destroyed a row. It now force-removes the crew's sidecar
// containers AND deletes their named volumes (#1709) — a Postgres data
// directory among them. Someone who has typed `y` to "Delete crew?" fifty times
// will type it the fifty-first without reading, and the docs warn the person
// who reads the docs, not the person doing it.
//
// So these tests assert the TEXT IN FRONT OF THE OPERATOR, not that
// confirmAction was called: a call-site assertion passes with the old wording,
// which is the entire defect. And the quiet case is pinned just as hard — a
// crew with no sidecars must keep the old one-line prompt, because a warning
// that fires when there is nothing to lose is how warnings stop being read.

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"github.com/spf13/cobra"
)

// deletePromptText runs `crew delete <ref>`, answers "n", and returns
// everything the operator saw.
func deletePromptText(t *testing.T, ref string) string {
	t.Helper()
	c := covFreshCmd(crewDeleteCmd, func(c *cobra.Command) {
		c.Flags().BoolP("yes", "y", false, "")
	})
	out, err := covCaptureStdoutCli4(t, func() error {
		return covWithStdinCli4(t, "n\n", func() error { return c.RunE(c, []string{ref}) })
	})
	if err == nil || err.Error() != "aborted" {
		t.Fatalf("want the prompt to abort on \"n\", got %v", err)
	}
	return out
}

func stubCrewWithServices(t *testing.T, servicesJSON string) *clitest.StubServer {
	t.Helper()
	stub := covSetupCli4(t)
	crew := map[string]any{
		"id":   covCrewIDCli4,
		"slug": "data-crew",
		"name": "Data Crew",
	}
	if servicesJSON != "" {
		crew["services_json"] = servicesJSON
	}
	stub.OnGet("/api/v1/crews/"+covCrewIDCli4, clitest.JSONResponse(200, crew))
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []any{crew}))
	return stub
}

func TestCrewDeletePromptNamesTheSidecarVolumesItDestroys(t *testing.T) {
	stub := stubCrewWithServices(t, `[
		{"name":"redis","image":"redis:7-alpine"},
		{"name":"postgres","image":"postgres:16","volumes":[{"name":"pg-data","mount":"/var/lib/postgresql/data"}]}
	]`)

	out := deletePromptText(t, covCrewIDCli4)
	t.Logf("the operator sees:\n%s", out)

	for _, want := range []string{
		"postgres",                  // the service, by the name the operator declared
		"svc-data-crew-vol-pg-data", // the volume, by the name docker knows it as
		"redis",                     // every sidecar, not just the one with a volume
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the confirmation never mentions %q — an operator answering it cannot know a "+
				"database volume is about to be deleted (#1709).\nprompt was:\n%s", want, out)
		}
	}
	if !strings.Contains(strings.ToLower(out), "cannot be recovered") &&
		!strings.Contains(strings.ToLower(out), "gone for good") {
		t.Errorf("the confirmation never says the data is unrecoverable:\n%s", out)
	}
	if got := len(stub.CallsFor("DELETE", "/api/v1/crews/"+covCrewIDCli4)); got != 0 {
		t.Errorf("answering \"n\" still deleted the crew (%d DELETE calls)", got)
	}
}

// A crew with no sidecars keeps the prompt it always had.
func TestCrewDeletePromptStaysQuietWithoutSidecars(t *testing.T) {
	stubCrewWithServices(t, "")

	out := deletePromptText(t, covCrewIDCli4)

	if !strings.Contains(out, "Delete crew") {
		t.Fatalf("the crew-delete confirmation disappeared entirely:\n%s", out)
	}
	for _, unwanted := range []string{"volume", "sidecar", "destroy"} {
		if strings.Contains(strings.ToLower(out), unwanted) {
			t.Errorf("a crew with no sidecars was warned about %q — a warning that fires when there "+
				"is nothing to lose is how warnings stop being read.\nprompt was:\n%s", unwanted, out)
		}
	}
}

// An unreadable crew is not the same as a crew with no sidecars, and must not
// be reported as one: the whole point of this PR is that silence about a
// destructive unknown is the defect.
func TestCrewDeletePromptSaysSoWhenItCannotCheckForSidecars(t *testing.T) {
	stub := covSetupCli4(t)
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []any{
		map[string]any{"id": covCrewIDCli4, "slug": "data-crew"},
	}))
	// The crew read fails: the prompt knows the crew exists (it resolved the
	// slug) but cannot see whether it has sidecars.
	stub.OnGet("/api/v1/crews/"+covCrewIDCli4, clitest.ErrorResponse(500, "boom"))

	out := deletePromptText(t, "data-crew")

	if !strings.Contains(strings.ToLower(out), "could not check") {
		t.Errorf("an unreadable crew was silently treated as one with nothing to lose:\n%s", out)
	}
}
