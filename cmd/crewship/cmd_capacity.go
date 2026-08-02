package main

// `crewship capacity` — what host admission control is holding, and why
// (#1668).
//
// This is the CLI half of GET /api/v1/runtime/capacity, and it exists for one
// reason: a container start held because the host is short of memory looks,
// from every other command, exactly like a start that has hung. `crewship now`
// carries the headline; this command carries the detail.

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

type runtimeCapacityHold struct {
	CrewID   string `json:"crew_id"`
	CrewSlug string `json:"crew_slug"`
	Reason   string `json:"reason"`
	Detail   string `json:"detail"`
	Since    string `json:"since"`
	WaitedMs int64  `json:"waited_ms"`
}

type runtimeCapacityHost struct {
	AvailableMB  int64   `json:"AvailableMB"`
	TotalMB      int64   `json:"TotalMB"`
	SomeStallPct float64 `json:"SomeStallPct"`
}

type runtimeCapacityLimits struct {
	MaxConcurrentStarts int     `json:"MaxConcurrentStarts"`
	MinStartInterval    int64   `json:"MinStartInterval"`
	RequiredFreeMB      int64   `json:"RequiredFreeMB"`
	MaxPressurePct      float64 `json:"MaxPressurePct"`
}

type runtimeCapacity struct {
	Enabled             bool                  `json:"enabled"`
	Limits              runtimeCapacityLimits `json:"limits"`
	InFlightStarts      int                   `json:"in_flight_starts"`
	Held                []runtimeCapacityHold `json:"held"`
	HeldTotal           uint64                `json:"held_total"`
	HostSignalAvailable bool                  `json:"host_signal_available"`
	HostSignalError     string                `json:"host_signal_error"`
	Host                runtimeCapacityHost   `json:"host"`
}

// capacitySummary is the one line `crewship now` shows and `crewship capacity`
// leads with.
//
// Four distinct states, and conflating any two of them is what makes a queue
// read as a hang:
//
//   - not configured — no admission control on this instance at all;
//   - inactive host signal — the memory gate cannot run here (macOS);
//   - nothing held — the gate is watching and is happy;
//   - N held — with the count up front, because that is the number a person
//     is looking for when a run has not started.
func capacitySummary(rc runtimeCapacity) string {
	if !rc.Enabled {
		return "Capacity: admission control not configured on this instance"
	}
	var parts []string
	if n := len(rc.Held); n > 0 {
		parts = append(parts, fmt.Sprintf("%d container start(s) held for capacity", n))
	} else {
		parts = append(parts, "0 held")
	}
	parts = append(parts, fmt.Sprintf("%d start(s) in flight", rc.InFlightStarts))
	if rc.HostSignalAvailable {
		mem := fmt.Sprintf("host memory %d/%d MiB available", rc.Host.AvailableMB, rc.Host.TotalMB)
		if rc.Host.SomeStallPct >= 0 {
			mem += fmt.Sprintf(", pressure %.2f%%", rc.Host.SomeStallPct)
		}
		parts = append(parts, mem)
	} else {
		parts = append(parts, "host-memory gate inactive (signal unavailable on this platform)")
	}
	return "Capacity: " + strings.Join(parts, " · ")
}

// capacityHeldLines renders one line per held start. Each has to carry the
// crew, the reason, how long it has waited, and the numbers behind the reason
// — an operator who cannot see the numbers cannot decide whether to wait or
// to go free memory.
func capacityHeldLines(rc runtimeCapacity) []string {
	out := make([]string, 0, len(rc.Held))
	for _, h := range rc.Held {
		name := h.CrewSlug
		if name == "" {
			name = h.CrewID
		}
		out = append(out, fmt.Sprintf("%s  held %s  reason=%s  %s",
			name, formatWaited(h.WaitedMs), h.Reason, h.Detail))
	}
	return out
}

func formatWaited(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	if d < time.Minute {
		return fmt.Sprintf("%.0fs", d.Seconds())
	}
	return d.Round(time.Second).String()
}

// capacityFetchNoise decides whether a failed capacity fetch is worth telling
// the user about on a composite screen like `crewship now`.
//
// A 404 is not: it means the server predates GET /api/v1/runtime/capacity, and
// a permanent "[partial] capacity: 404" line under every `crewship now` is how
// people learn to stop reading the partial-error lines that matter. A 401 is
// not either — the caller's own session handling covers it, and repeating it
// alongside three identical lines adds nothing. Anything else is a real
// failure and is reported.
//
// Returns "" when there is nothing to say.
func capacityFetchNoise(err error) string {
	if err == nil {
		return ""
	}
	var apiErr *cli.APIError
	if errors.As(err, &apiErr) && (apiErr.Status == http.StatusNotFound || apiErr.Status == http.StatusUnauthorized) {
		return ""
	}
	return "capacity: " + err.Error()
}

func fetchCapacity(client *cli.Client) (runtimeCapacity, error) {
	var rc runtimeCapacity
	err := getJSON(client, "/api/v1/runtime/capacity", &rc)
	return rc, err
}

var capacityCmd = &cobra.Command{
	Use:   "capacity",
	Short: "Show host admission control: what container starts are held, and why",
	Long: `Show what host admission control is doing right now.

Crewship holds a crew container start when the host cannot afford another
one — not enough free memory, too many starts already in flight, or a start
admitted moments ago (simultaneous wakes are staggered on purpose: creating a
network namespace takes a global kernel lock whose cost grows sharply with
concurrency).

A held start is NOT a hung one. This command is how to tell them apart.

The thresholds are instance settings:

  runtime.host_memory_reserve_mb          host headroom kept free, on top of
                                          one agent (runtime.agent_min_memory_mb);
                                          0 disables the host-memory gate
  runtime.host_memory_pressure_pct        PSI "some avg10" ceiling; 0 disables
  runtime.max_concurrent_container_starts 0 = unbounded
  runtime.container_start_stagger_ms      0 = no stagger

The host-memory gate reads /proc/meminfo and /proc/pressure/memory, which
exist on Linux only. On macOS it is inactive and this command says so; the
concurrency bound and the stagger still apply there.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		rc, err := fetchCapacity(client)
		if err != nil {
			return err
		}
		f := newFormatter()
		return f.AutoHuman(rc, func() {
			fmt.Printf("%s%s%s\n", cli.Bold, capacitySummary(rc), cli.Reset)
			if !rc.HostSignalAvailable && rc.HostSignalError != "" {
				fmt.Printf("  %s%s%s\n", cli.Dim, rc.HostSignalError, cli.Reset)
			}
			if rc.Enabled {
				fmt.Printf("\n%sLimits:%s memory floor %d MiB · max concurrent starts %d · stagger %s · pressure ceiling %.2f%%\n",
					cli.Bold, cli.Reset,
					rc.Limits.RequiredFreeMB,
					rc.Limits.MaxConcurrentStarts,
					(time.Duration(rc.Limits.MinStartInterval)).String(),
					rc.Limits.MaxPressurePct)
			}
			lines := capacityHeldLines(rc)
			if len(lines) == 0 {
				return
			}
			fmt.Printf("\n%sHeld container starts:%s\n", cli.Bold, cli.Reset)
			for _, l := range lines {
				fmt.Printf("  %s%s%s\n", cli.Yellow, l, cli.Reset)
			}
			fmt.Fprintf(os.Stderr, "%sheld since this daemon started: %d%s\n", cli.Dim, rc.HeldTotal, cli.Reset)
		})
	},
}

func init() {
	rootCmd.AddCommand(capacityCmd)
}
