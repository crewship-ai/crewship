package main

// #1845 — CLI parity for the crew image-freshness endpoints (project rule #3).
//
// The notification an operator receives names `crewship crew refresh-image`
// as the remediation, and the sweep's journal payload carries that same
// string. If the command did not exist, the notification would be telling
// people to run something that is not there — which is a worse outcome than
// the silence this issue set out to fix.

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// crewImageStatusResponse mirrors GET /api/v1/crews/{crewId}/image-status.
//
// Reason is carried all the way to the surface deliberately. `behind: false`
// on its own cannot distinguish "compared and current" from "the registry
// could not be reached", and printing a bare "up to date" for the second is
// the exact false assurance the provider's classifier refuses to give.
type crewImageStatusResponse struct {
	CrewID         string `json:"crew_id"`
	Image          string `json:"image"`
	ContainerID    string `json:"container_id"`
	Running        bool   `json:"running"`
	RunningDigest  string `json:"running_digest"`
	ResolvedDigest string `json:"resolved_digest"`
	Behind         bool   `json:"behind"`
	Reason         string `json:"reason"`
}

type crewImageRefreshResponse struct {
	CrewID           string `json:"crew_id"`
	Image            string `json:"image"`
	PreviousDigest   string `json:"previous_digest"`
	NewDigest        string `json:"new_digest"`
	ContainerRemoved bool   `json:"container_removed"`
}

var crewImageStatusCmd = &cobra.Command{
	Use:   "image-status <slug-or-id>",
	Short: "Report whether a crew's container is behind its image tag",
	Long: `Compare the image a crew's RUNNING container was created from against
what its tag resolves to on the registry right now.

A crew container is created once and reused until something recreates it, so a
long-lived crew keeps running whatever the registry held on the day it started.
This is the read behind that: it never pulls and never restarts anything.

"behind: false" is not always "current". When the registry is unreachable, or
the crew runs a locally built image with no registry digest, there is nothing
to compare — the reason field says which, and it is printed rather than
collapsed into a green tick.

Examples:
  crewship crew image-status demo-crew
  crewship crew image-status demo-crew --format json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		crewID, err := resolveCrewID(client, args[0])
		if err != nil {
			return err
		}

		var result crewImageStatusResponse
		if err := getJSON(client, "/api/v1/crews/"+crewID+"/image-status", &result); err != nil {
			return err
		}

		f := newFormatter()
		switch f.Format {
		case "json":
			return f.JSON(result)
		case "yaml":
			return f.YAML(result)
		}

		verdict := "current"
		switch {
		case result.Behind:
			verdict = "BEHIND"
		case result.Reason != "":
			verdict = "unknown (" + sanitizeTerminal(result.Reason) + ")"
		}
		pairs := [][]string{
			{"Crew", sanitizeTerminal(args[0])},
			{"Image", sanitizeTerminal(result.Image)},
			{"Status", verdict},
		}
		if result.RunningDigest != "" {
			pairs = append(pairs, []string{"Running digest", sanitizeTerminal(result.RunningDigest)})
		}
		if result.ResolvedDigest != "" {
			pairs = append(pairs, []string{"Tag resolves to", sanitizeTerminal(result.ResolvedDigest)})
		}
		if result.ContainerID != "" {
			state := "stopped"
			if result.Running {
				state = "running"
			}
			pairs = append(pairs, []string{"Container", sanitizeTerminal(shortContainerID(result.ContainerID)) + " (" + state + ")"})
		}
		f.Detail(pairs)
		if result.Behind {
			fmt.Fprintf(os.Stdout, "\nRun 'crewship crew refresh-image %s' to pull the current image and recycle the container.\n",
				sanitizeTerminal(args[0]))
		}
		return nil
	},
}

var crewRefreshImageCmd = &cobra.Command{
	Use:   "refresh-image <slug-or-id>",
	Short: "Pull the crew's current image and drop its container so agents pick it up",
	Long: `Pull the image the crew is configured to run, then force-remove its
runtime container. The next agent exec recreates the container from the freshly
pulled image.

This is what to run when 'crew image-status' says BEHIND, and what the
"crew image is behind" notification points at.

Different from 'crew restart-agents', which drops the container WITHOUT
pulling: that picks up a devcontainer image you have already rebuilt locally,
this picks up a base image that has moved on in the registry.

Agents currently executing in the container are interrupted. Idempotent: a crew
that is already current pulls nothing new, and one with no container has
nothing to drop.

Examples:
  crewship crew refresh-image demo-crew
  crewship crew refresh-image demo-crew --format json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		crewID, err := resolveCrewID(client, args[0])
		if err != nil {
			return err
		}

		var result crewImageRefreshResponse
		if err := postJSON(client, "/api/v1/crews/"+crewID+"/refresh-image", nil, &result); err != nil {
			return err
		}

		f := newFormatter()
		switch f.Format {
		case "json":
			return f.JSON(result)
		case "yaml":
			return f.YAML(result)
		}

		pairs := [][]string{
			{"Crew", sanitizeTerminal(args[0])},
			{"Image", sanitizeTerminal(result.Image)},
		}
		if result.PreviousDigest != "" {
			pairs = append(pairs, []string{"Was running", sanitizeTerminal(result.PreviousDigest)})
		}
		if result.NewDigest != "" {
			pairs = append(pairs, []string{"Now on", sanitizeTerminal(result.NewDigest)})
		}
		f.Detail(pairs)

		if result.ContainerRemoved {
			cli.PrintSuccess(fmt.Sprintf(
				"Crew %q: container dropped — agents recreate it from the refreshed image on the next exec.",
				sanitizeTerminal(args[0])))
		} else {
			cli.PrintSuccess(fmt.Sprintf(
				"Crew %q: image refreshed. No container was running, so the next start uses it directly.",
				sanitizeTerminal(args[0])))
		}
		return nil
	},
}

// shortContainerID trims a full 64-hex container id to the 12 characters
// docker itself displays. Kept local rather than reaching into the provider
// package: the CLI must not link the container runtime.
func shortContainerID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func init() {
	crewCmd.AddCommand(crewImageStatusCmd)
	crewCmd.AddCommand(crewRefreshImageCmd)
}
