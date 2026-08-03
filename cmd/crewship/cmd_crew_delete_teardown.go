package main

// What `crewship crew delete` is about to destroy, said before it is destroyed.
//
// The command has always confirmed. The prompt it confirmed with — `Delete crew
// "x"?` — was written when deleting a crew soft-deleted a row and left every
// container and volume it owned running (#1709). It now force-removes the
// crew's sidecar containers and DELETES their named volumes, a Postgres data
// directory among them.
//
// A prompt whose wording predates what it authorises is worse than no prompt:
// the operator has answered this exact question fifty times and knows what it
// costs. So the question changes when the answer does — and only then. A crew
// with no sidecars keeps the one-liner, because a warning that fires when there
// is nothing to lose is how warnings stop being read.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
)

// defaultContainerPrefix mirrors the docker provider's own fallback
// (internal/provider/docker/docker.go:namePrefix). An instance can override it
// with CREWSHIP_CONTAINER_PREFIX — which is a server-side setting the CLI has no
// way to read — so the rendered names carry a note rather than a false
// certainty. The suffix, which is the part that identifies the crew and the
// volume, is exact either way.
const defaultContainerPrefix = "crewship"

type crewDeleteService struct {
	Name    string `json:"name"`
	Image   string `json:"image"`
	Volumes []struct {
		Name string `json:"name"`
	} `json:"volumes"`
}

// crewSidecarDeleteWarning returns the extra paragraphs the delete confirmation
// carries for this crew, or "" when the crew declares no sidecars and the
// prompt should stay exactly as it was.
//
// A crew that cannot be read is NOT reported as a crew with nothing to lose:
// the failure this whole change is about is silence in front of a destructive
// unknown. It says it could not check, and lets the operator decide.
func crewSidecarDeleteWarning(client *cli.Client, crewID string) string {
	resp, err := client.Get("/api/v1/crews/" + crewID)
	if err == nil {
		err = cli.CheckError(resp)
	}
	if err != nil {
		return "Could not check this crew for sidecar services (" + err.Error() + ").\n" +
			"If it declares any, deleting the crew also deletes their data volumes."
	}

	var crew struct {
		Slug         string `json:"slug"`
		ServicesJSON string `json:"services_json"`
	}
	if err := cli.ReadJSON(resp, &crew); err != nil {
		return "Could not check this crew for sidecar services (" + err.Error() + ").\n" +
			"If it declares any, deleting the crew also deletes their data volumes."
	}
	if strings.TrimSpace(crew.ServicesJSON) == "" {
		return ""
	}

	var services []crewDeleteService
	if err := json.Unmarshal([]byte(crew.ServicesJSON), &services); err != nil {
		return "Could not read this crew's sidecar service list (" + err.Error() + ").\n" +
			"Deleting the crew removes its sidecar containers and their data volumes."
	}
	if len(services) == 0 {
		return ""
	}

	return renderCrewSidecarDeleteWarning(crew.Slug, services)
}

// renderCrewSidecarDeleteWarning is the text itself, kept pure so it can be
// read (and tested) without a server.
func renderCrewSidecarDeleteWarning(crewSlug string, services []crewDeleteService) string {
	var b strings.Builder
	b.WriteString("This also destroys the crew's sidecar services — their containers are\n")
	b.WriteString("force-removed and their named volumes are deleted with them:\n\n")

	anyVolume := false
	for _, s := range services {
		name := strings.TrimSpace(s.Name)
		if name == "" {
			continue
		}
		image := strings.TrimSpace(s.Image)
		if image == "" {
			image = "(no image)"
		}
		fmt.Fprintf(&b, "  %-12s %s\n", name, image)
		for _, v := range s.Volumes {
			vol := strings.TrimSpace(v.Name)
			if vol == "" {
				continue
			}
			anyVolume = true
			fmt.Fprintf(&b, "               volume %s-svc-%s-vol-%s\n",
				defaultContainerPrefix, crewSlug, vol)
		}
	}

	b.WriteString("\n")
	if anyVolume {
		b.WriteString("Whatever those volumes hold — a database, a queue, an index — is gone for\n")
		b.WriteString("good; it cannot be recovered afterwards. Back it up first if it matters:\n")
		b.WriteString("  crewship backup create\n")
		b.WriteString("(volume names carry this instance's container prefix, \"" +
			defaultContainerPrefix + "\" unless it was changed.)")
	} else {
		b.WriteString("None of them declares a volume, so no stored data is lost — but the\n")
		b.WriteString("containers are removed and anything written inside them is gone for good.")
	}
	return b.String()
}
