package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/provider"
)

// crewContainerStatusCmd surfaces a crew's runtime container state — the CLI
// counterpart to GET /api/v1/crews/{crewId}/container-status. It's the quick
// way to confirm a crew's container came back up after a network-policy change
// (which stops the container so it's recreated with the new policy).
var crewContainerStatusCmd = &cobra.Command{
	Use:   "container-status <slug-or-id>",
	Short: "Show a crew's runtime container status (running / stopped / …)",
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

		resp, err := client.Get("/api/v1/crews/" + crewID + "/container-status")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		// Decoded twice, deliberately. The MAP is what `--format json/yaml`
		// emits: this payload is the agent-facing contract (core rule #3 — the
		// CLI is how an agent drives Crewship), so #1681's
		// configured_memory_mb / effective_cpus / config_drift have to be
		// reachable there. Emitting the struct instead would silently drop
		// every field this command does not print today, including the ones
		// added next. The STRUCT drives the human block only.
		var payload map[string]any
		if err := cli.ReadJSON(resp, &payload); err != nil {
			return err
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("re-encode container status: %w", err)
		}

		var status crewContainerStatus
		if err := json.Unmarshal(raw, &status); err != nil {
			return fmt.Errorf("decode container status: %w", err)
		}

		return newFormatter().AutoHuman(payload, func() {
			printCrewContainerStatus(args[0], status)
		})
	},
}

// crewContainerStatus is the slice of the container-status payload the human
// block renders. It is not the whole payload — `--format json` emits that
// verbatim, and this struct must never become the machine contract.
type crewContainerStatus struct {
	CrewID string `json:"crew_id"`
	Status string `json:"status"`
	Uptime string `json:"uptime"`
	// RuntimeContract is absent when the provider has no opinion, and absent
	// is not "current" — nothing is printed in that case.
	RuntimeContract string `json:"runtime_contract"`
	// ConfigDrift names the per-crew settings the container does not carry —
	// its memory / CPU limits, which are applied at container create and
	// nowhere else (#1681). Empty whenever the container matches its crew, and
	// whenever the provider reported no limits to compare against.
	ConfigDrift []struct {
		Field      string  `json:"field"`
		Configured float64 `json:"configured"`
		Effective  float64 `json:"effective"`
	} `json:"config_drift"`
}

// running reports whether the container is actually serving right now.
//
// Load-bearing for the wording below: a limits gap on a running container is
// something the operator has to act on, and the same gap on a stopped one
// closes itself on the next wake. Saying the running thing about a stopped
// container is the exact defect the rest of #1681 exists to remove — a surface
// asserting a state it did not check.
func (s crewContainerStatus) running() bool { return s.Status == "running" }

// printCrewContainerStatus renders the human block. Split out of the RunE so
// the state-dependent wording has one home, and so --format routing (which
// must skip all of it) reads as one call.
//
// crewArg is echoed into the remedy lines and is sanitized at every use.
func printCrewContainerStatus(crewArg string, status crewContainerStatus) {
	fmt.Printf("%sContainer:%s %s%s%s\n", cli.Bold, cli.Reset,
		containerStatusColor(status.Status), sanitizeTerminal(status.Status), cli.Reset)
	if status.Uptime != "" {
		fmt.Printf("%sStarted:%s   %s\n", cli.Bold, cli.Reset, sanitizeTerminal(status.Uptime))
	}
	// The crew container is created once and reused for days, so a merged
	// change to how it is created — the init process, the core-dump ulimit,
	// supplementary groups, swap, /dev/shm — reaches only crews whose
	// container is recreated afterwards. This is where an operator finds out
	// which those are (#1642).
	switch status.RuntimeContract {
	case provider.RuntimeContractCurrent:
		fmt.Printf("%sConfig:%s    %scurrent%s\n", cli.Bold, cli.Reset, cli.Green, cli.Reset)
	case provider.RuntimeContractStale:
		fmt.Printf("%sConfig:%s    %sfrom an older build%s\n", cli.Bold, cli.Reset, cli.Yellow, cli.Reset)
		fmt.Printf("           This container was created before the current container configuration and does not\n")
		fmt.Printf("           carry the settings added since — including hardening that is applied at create time\n")
		fmt.Printf("           and nowhere else. It picks them up the next time the container is recreated: an\n")
		fmt.Printf("           idle-TTL stop, or `crewship crew restart-agents %s`.\n", sanitizeTerminal(crewArg))
	}
	// The same shape of gap, one level down: not "this container is older than
	// the build" but "this container is older than THIS CREW'S configuration".
	// A memory or CPU edit is written to the row, reported by `crew get`, and
	// applied to the cgroup only at create time — so until the container is
	// recreated the crew runs under the old figure, and this is the surface
	// that says so (#1681).
	if len(status.ConfigDrift) == 0 {
		return
	}
	fmt.Printf("%sLimits:%s    %sconfigured, not yet in effect%s\n", cli.Bold, cli.Reset, cli.Yellow, cli.Reset)
	for _, d := range status.ConfigDrift {
		// "running with" only when it is: on a stopped container the figure is
		// what it was CREATED with and nothing is running under it.
		carrying := "container created with"
		if status.running() {
			carrying = "container running with"
		}
		fmt.Printf("           %s: %s configured, %s %s\n",
			sanitizeTerminal(d.Field), formatLimit(d.Configured), carrying, formatLimit(d.Effective))
	}
	if status.running() {
		fmt.Printf("           Container limits are applied when the container is created and cannot be changed\n")
		fmt.Printf("           on a running one. The crew picks these up the next time it is recreated: an\n")
		fmt.Printf("           idle-TTL stop, or `crewship crew restart-agents %s`.\n", sanitizeTerminal(crewArg))
		return
	}
	// Not running: #1681's reconcile rebuilds a stopped container whose limits
	// no longer match its crew, so this gap is informational. Naming a remedy
	// here would send the operator to force a recreation they already get for
	// free — and `restart-agents` on a stopped crew reports "nothing to
	// restart", which reads as the report being wrong.
	fmt.Printf("           Container limits are applied when the container is created. This container is\n")
	fmt.Printf("           %s, so the crew is recreated with the configured limits the next time it starts.\n",
		sanitizeTerminal(status.Status))
	fmt.Printf("           No action needed.\n")
}

// formatLimit renders a limit figure the way it was written: 8192 rather than
// 8192.000000, and 1.5 rather than 2, because container_cpus is fractional and
// rounding it in the report would make a real drift look like a display bug.
func formatLimit(v float64) string {
	return fmt.Sprintf("%g", v)
}

// containerStatusColor maps a container state to a terminal colour.
func containerStatusColor(state string) string {
	switch state {
	case "running":
		return cli.Green
	case "creating":
		return cli.Blue
	case "stopped", "not_configured":
		return cli.Yellow
	case "error", "unknown":
		return cli.Red
	default:
		return ""
	}
}
