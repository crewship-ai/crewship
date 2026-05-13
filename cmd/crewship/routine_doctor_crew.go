package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

func checkAuthorCrew(client doctorHTTPGetter, crewID string) doctorCheck {
	if crewID == "" {
		return doctorCheck{
			Name:    "author_crew",
			Level:   doctorFail,
			Message: "author_crew_id is empty",
			Hint:    "the routine has no owner crew — re-save with --author-crew",
		}
	}
	resp, err := client.Get(fmt.Sprintf("/api/v1/crews/%s/provision", url.PathEscape(crewID)))
	if resp != nil {
		defer resp.Body.Close()
	}
	if err != nil || resp == nil {
		return doctorCheck{
			Name:    "author_crew",
			Level:   doctorWarn,
			Message: "could not query crew provisioning status",
			Hint:    "the run will probably still work; this just means doctor couldn't verify the crew is ready",
		}
	}
	if resp.StatusCode == http.StatusNotFound {
		return doctorCheck{
			Name:    "author_crew",
			Level:   doctorFail,
			Message: "author crew not found in workspace",
			Hint:    "crew was deleted — re-author this routine under a still-existing crew",
		}
	}
	if resp.StatusCode != http.StatusOK {
		return doctorCheck{
			Name:    "author_crew",
			Level:   doctorWarn,
			Message: fmt.Sprintf("crew status returned HTTP %d", resp.StatusCode),
		}
	}
	var status struct {
		Status             string `json:"status"`
		DevcontainerConfig string `json:"devcontainer_config"`
		CachedImage        string `json:"cached_image"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return doctorCheck{Name: "author_crew", Level: doctorWarn, Message: "could not decode crew status response"}
	}
	if status.DevcontainerConfig == "" {
		return doctorCheck{
			Name:    "author_crew",
			Level:   doctorWarn,
			Message: "crew has no devcontainer config — Claude Code CLI may not be available",
			Hint:    "set a devcontainer config on the crew so the OrchestratorRunner can spawn agents",
		}
	}
	switch status.Status {
	case "completed":
		return doctorCheck{
			Name:    "author_crew",
			Level:   doctorOK,
			Message: fmt.Sprintf("provisioned (image cached: %s)", truncCrewID(status.CachedImage)),
		}
	case "in_progress":
		return doctorCheck{
			Name:    "author_crew",
			Level:   doctorWarn,
			Message: "provisioning in progress — first run will block until image is built",
			Hint:    "wait for `crewship crew provision status " + crewID + "` to show completed",
		}
	case "failed":
		return doctorCheck{
			Name:    "author_crew",
			Level:   doctorFail,
			Message: "crew provisioning failed",
			Hint:    "re-trigger via `crewship crew provision start " + crewID + "` and inspect logs",
		}
	default:
		return doctorCheck{
			Name:    "author_crew",
			Level:   doctorWarn,
			Message: "provisioning status: " + status.Status,
		}
	}
}
