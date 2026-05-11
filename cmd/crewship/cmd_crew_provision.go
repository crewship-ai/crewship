package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// provisionStatusResponse mirrors what GET /api/v1/crews/{id}/provision returns.
// All progress fields are optional — only present while a job is in flight or
// has just completed.
type provisionStatusResponse struct {
	Status               string   `json:"status"`
	Error                string   `json:"error,omitempty"`
	CachedImage          *string  `json:"cached_image"`
	ConfigHash           *string  `json:"config_hash"`
	DevcontainerConfig   *string  `json:"devcontainer_config"`
	Step                 int      `json:"step,omitempty"`
	Total                int      `json:"total,omitempty"`
	Message              string   `json:"message,omitempty"`
	Steps                []string `json:"steps,omitempty"`
	LogTail              []string `json:"log_tail,omitempty"`
	AgentsPendingRestart int      `json:"agents_pending_restart,omitempty"`
}

var crewProvisionCmd = &cobra.Command{
	Use:   "provision <slug-or-id>",
	Short: "Trigger devcontainer provisioning for a crew (live progress)",
	Long: `Trigger a devcontainer build for a crew and stream live progress
until the build completes or fails. Equivalent to clicking Build now in the
toolbar popover — same checklist, same outcome.

Pass --no-watch to fire-and-forget; use 'crewship crew provision status
<slug> --watch' to attach later.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		crewID, err := resolveCrewID(client, args[0])
		if err != nil {
			return err
		}

		resp, err := client.Post("/api/v1/crews/"+crewID+"/provision", nil)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		noWatch, _ := cmd.Flags().GetBool("no-watch")
		if noWatch {
			cli.PrintSuccess(fmt.Sprintf("Provisioning started for crew %q.", args[0]))
			return nil
		}

		fmt.Fprintf(os.Stdout, "%sBuilding container image for %q…%s\n", cli.Bold, args[0], cli.Reset)
		return watchProvision(client, crewID, args[0])
	},
}

var crewProvisionStatusCmd = &cobra.Command{
	Use:   "status <slug-or-id>",
	Short: "Check (or watch) provisioning status for a crew",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		crewID, err := resolveCrewID(client, args[0])
		if err != nil {
			return err
		}

		watch, _ := cmd.Flags().GetBool("watch")
		if watch {
			return watchProvision(client, crewID, args[0])
		}

		// Single-shot status snapshot.
		resp, err := client.Get("/api/v1/crews/" + crewID + "/provision")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var result provisionStatusResponse
		if err := cli.ReadJSON(resp, &result); err != nil {
			return err
		}

		f := newFormatter()
		cachedImage := "-"
		if result.CachedImage != nil {
			cachedImage = *result.CachedImage
		}
		configHash := "-"
		if result.ConfigHash != nil {
			configHash = *result.ConfigHash
		}
		hasConfig := "no"
		if result.DevcontainerConfig != nil {
			hasConfig = "yes"
		}
		pairs := [][]string{
			{"Status", result.Status},
			{"Has Config", hasConfig},
			{"Cached Image", cachedImage},
			{"Config Hash", configHash},
		}
		if result.Total > 0 {
			pairs = append(pairs, []string{"Step", fmt.Sprintf("%d/%d %s", result.Step, result.Total, result.Message)})
		}
		if result.AgentsPendingRestart > 0 {
			pairs = append(pairs, []string{"Agents pending restart", fmt.Sprintf("%d", result.AgentsPendingRestart)})
		}
		return f.AutoDetail(result, pairs)
	},
}

// crewRestartAgentsCmd wires the call the provision-success message has
// been suggesting for months. Drops the crew's runtime container so the
// next agent exec recreates it from the latest cached image — agents pick
// up new system prompts, new MCP config, new env, without a full rebuild.
//
// Idempotent: returns {restarted: 0} when no container was running.
var crewRestartAgentsCmd = &cobra.Command{
	Use:   "restart-agents <slug-or-id>",
	Short: "Restart all agents in a crew (drops the runtime container)",
	Long: `Force-remove the crew's runtime container. The next agent exec
recreates it from the current cached image, so agents pick up new system
prompts, MCP config and env vars without a full rebuild.

Idempotent: succeeds with restarted=0 when no container was running.

Examples:
  crewship crew restart-agents demo-crew
  crewship crew restart-agents demo-crew --format json`,
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

		var result struct {
			Restarted int    `json:"restarted"`
			Error     string `json:"error,omitempty"`
		}
		if err := postJSON(client, "/api/v1/crews/"+crewID+"/restart-agents", nil, &result); err != nil {
			return err
		}

		f := newFormatter()
		switch f.Format {
		case "json":
			return f.JSON(result)
		case "yaml":
			return f.YAML(result)
		}
		if result.Restarted == 0 {
			cli.PrintSuccess(fmt.Sprintf("Crew %q: no running container, nothing to restart.", args[0]))
		} else {
			cli.PrintSuccess(fmt.Sprintf("Crew %q restarted: %d agent%s will pick up the new image on next exec.",
				args[0], result.Restarted, plural(result.Restarted)))
		}
		return nil
	},
}

var crewRebuildCmd = &cobra.Command{
	Use:   "rebuild <slug-or-id>",
	Short: "Invalidate cache and re-provision a crew container",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		crewID, err := resolveCrewID(client, args[0])
		if err != nil {
			return err
		}

		resp, err := client.Post("/api/v1/crews/"+crewID+"/rebuild", nil)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		noWatch, _ := cmd.Flags().GetBool("no-watch")
		if noWatch {
			cli.PrintSuccess(fmt.Sprintf("Cache invalidated and provisioning started for crew %q.", args[0]))
			return nil
		}

		fmt.Fprintf(os.Stdout, "%sRebuilding container image for %q…%s\n", cli.Bold, args[0], cli.Reset)
		return watchProvision(client, crewID, args[0])
	},
}

// watchProvision polls /provision until the job reaches a terminal state and
// re-renders a multi-line checklist in place between polls. Returns nil on
// completed, a wrapping error on failed.
//
// Cap is 10 minutes — longer than any realistic build, short enough to not
// hang a CI pipeline indefinitely if the server forgets a job.
func watchProvision(client *cli.Client, crewID, slug string) error {
	r := &checklistRenderer{}
	const pollInterval = 1 * time.Second
	const maxIdleTicks = 600

	for tick := 0; tick < maxIdleTicks; tick++ {
		resp, err := client.Get("/api/v1/crews/" + crewID + "/provision")
		if err != nil {
			return fmt.Errorf("status fetch: %w", err)
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var status provisionStatusResponse
		if err := cli.ReadJSON(resp, &status); err != nil {
			return err
		}

		r.render(&status)

		switch status.Status {
		case "completed":
			cli.PrintSuccess(fmt.Sprintf("Crew %q provisioned.", slug))
			if status.AgentsPendingRestart > 0 {
				fmt.Fprintf(os.Stdout, "  %s%d agent%s on the old image. Run 'crewship crew restart-agents %s' when ready.%s\n",
					cli.Yellow, status.AgentsPendingRestart, plural(status.AgentsPendingRestart), slug, cli.Reset)
			}
			return nil
		case "failed":
			cli.PrintError(fmt.Sprintf("Provisioning failed: %s", status.Error))
			return fmt.Errorf("provisioning failed")
		}

		time.Sleep(pollInterval)
	}
	return fmt.Errorf("provisioning did not complete within %s", time.Duration(maxIdleTicks)*pollInterval)
}

// checklistRenderer redraws a multi-line checklist in place between polls
// using ANSI cursor escapes. Tracks how many lines it printed last time so
// the next render can move the cursor back up by exactly that many rows.
//
// Falls back to plain append-only output when stdout isn't a TTY (CI logs,
// pipes) — build agents capturing logs would otherwise see escape garbage.
type checklistRenderer struct {
	lastLines int
	noTTY     bool
	once      bool
}

func (r *checklistRenderer) render(status *provisionStatusResponse) {
	if !r.once {
		// Treat NO_COLOR or empty/dumb TERM as non-interactive. Cheaper than
		// pulling isatty here; matches what cli.InitColors already does.
		r.noTTY = os.Getenv("NO_COLOR") != "" ||
			os.Getenv("TERM") == "" ||
			os.Getenv("TERM") == "dumb"
		r.once = true
	}
	if r.noTTY {
		// Print only on transitions, not every poll — keeps CI logs sane.
		if status.Total > 0 && status.Message != "" {
			fmt.Fprintf(os.Stdout, "[%d/%d] %s\n", status.Step, status.Total, status.Message)
		}
		return
	}

	// Move up over the previous render and clear from the cursor to end.
	if r.lastLines > 0 {
		fmt.Fprintf(os.Stdout, "\033[%dA\033[J", r.lastLines)
	}

	lines := 0
	if len(status.Steps) > 0 {
		// Active row: prefer matching by current message (handles SortFeatures
		// reordering vs. the alphabetical plan), fall back to step-1.
		active := status.Step - 1
		if status.Message != "" {
			for i, s := range status.Steps {
				if s == status.Message {
					active = i
					break
				}
			}
		}
		for i, step := range status.Steps {
			switch {
			case i < active:
				fmt.Fprintf(os.Stdout, "  %s✓%s %s%s%s\n", cli.Green, cli.Reset, cli.Dim, step, cli.Reset)
			case i == active:
				fmt.Fprintf(os.Stdout, "  %s%s⏳%s %s%s%s\n", cli.Blue, cli.Bold, cli.Reset, cli.Bold, step, cli.Reset)
			default:
				fmt.Fprintf(os.Stdout, "  %s○%s %s%s%s\n", cli.Gray, cli.Reset, cli.Gray, step, cli.Reset)
			}
			lines++
		}
	} else if status.Total > 0 {
		msg := status.Message
		if msg == "" {
			msg = "Building image…"
		}
		fmt.Fprintf(os.Stdout, "  %s⏳%s %s (%d/%d)\n", cli.Blue, cli.Reset, msg, status.Step, status.Total)
		lines++
	} else {
		fmt.Fprintf(os.Stdout, "  %s⏳%s %sStarting…%s\n", cli.Blue, cli.Reset, cli.Dim, cli.Reset)
		lines++
	}

	r.lastLines = lines
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func init() {
	crewProvisionCmd.Flags().Bool("no-watch", false, "do not watch progress; return immediately after triggering")
	crewProvisionStatusCmd.Flags().Bool("watch", false, "stream live progress until the build completes")
	crewRebuildCmd.Flags().Bool("no-watch", false, "do not watch progress; return immediately after triggering")
	crewProvisionCmd.AddCommand(crewProvisionStatusCmd)
	crewCmd.AddCommand(crewProvisionCmd)
	crewCmd.AddCommand(crewRebuildCmd)
	crewCmd.AddCommand(crewRestartAgentsCmd)
}
