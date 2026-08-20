package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// crewStartCmd brings a crew's runtime container up — the CLI half of
// POST /api/v1/crews/{crewId}/container-start.
//
// It exists because `crew provision` looks like it and is not. Provision
// builds an image; the container was only ever created lazily by the
// first agent run, so the documented remedy for a crew-file 409 ("start
// the crew and retry") named no command anybody could type, and the
// workaround was to run an agent with a throwaway prompt — spending
// tokens for a side effect.
//
// Idempotent: starting a running crew returns its container and exits 0,
// so a deploy script can start-then-write without branching.
var crewStartCmd = &cobra.Command{
	Use:   "start <slug-or-id>",
	Short: "Start a crew's runtime container (builds its image first if needed)",
	Long: `Start a crew's runtime container and wait until it is running.

Unlike 'crewship crew provision', which only builds the container IMAGE,
this creates and starts the container itself — the same sequence an agent
run performs, including the crew's provisioned image and any declared
sidecar services.

Use it before writing into a crew-owned tree: files under shared/ are
owned by the container user, so 'crewship crew files save' can only
overwrite them while the crew is running.

Starting an already-running crew is a no-op that succeeds.`,
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

		// Progress goes to stderr, not stdout. A cold crew blocks here on
		// an image build, so the line has to be printed BEFORE the call
		// rather than folded into the human renderer — and on stdout it
		// would sit above the JSON body and break `-f json | jq`, which
		// is the form an agent drives this through.
		fmt.Fprintf(os.Stderr, "%sStarting crew %q…%s\n", cli.Bold, sanitizeTerminal(args[0]), cli.Reset)

		timeout, _ := cmd.Flags().GetDuration("timeout")
		if timeout <= 0 {
			timeout = crewStartTimeout
		}
		resp, err := client.WithTimeout(timeout).Post("/api/v1/crews/"+crewID+"/container-start", nil)
		if err != nil {
			return err
		}
		// CheckError turns the 503 (no container runtime) and the 502
		// (image build or start failed) into a non-zero exit carrying the
		// server's sentence. A script that reads exit 0 here would go on
		// to write files into a crew that is not running.
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		// Decoded as a map so --format json/yaml emits the payload whole:
		// the CLI is the contract agents drive Crewship through, and a
		// typed struct would silently drop every field added after it.
		var payload map[string]any
		if err := cli.ReadJSON(resp, &payload); err != nil {
			return err
		}
		return newFormatter().AutoHuman(payload, func() {
			status, _ := payload["status"].(string)
			if status == "" {
				status = "unknown"
			}
			fmt.Fprintf(os.Stdout, "%sContainer:%s %s%s%s\n", cli.Bold, cli.Reset,
				containerStatusColor(status), sanitizeTerminal(status), cli.Reset)
			if id, ok := payload["container_id"].(string); ok && id != "" {
				fmt.Fprintf(os.Stdout, "%sID:%s        %s\n",
					cli.Bold, cli.Reset, sanitizeTerminal(shortContainerID(id)))
			}

			// Anything the start had to do without. Printed, not logged:
			// the operator asked for this crew to be up, and "up, but
			// without its declared postgres" changes what they do next.
			notices, ok := payload["notices"].([]any)
			if !ok || len(notices) == 0 {
				return
			}
			fmt.Fprintln(os.Stdout)
			fmt.Fprintf(os.Stdout, "%sNotices:%s\n", cli.Yellow, cli.Reset)
			for _, n := range notices {
				if s, ok := n.(string); ok {
					fmt.Fprintf(os.Stdout, "  ! %s\n", sanitizeTerminal(s))
				}
			}
		})
	},
}

// crewStartTimeout is the per-request cap for a synchronous container
// start.
//
// It has to clear the SERVER's own ceiling or the flag is a lie: a crew
// with no image is provisioned first, and EnsureProvisioned applies a
// 15-minute default for exactly that (a large base image plus features
// on a cold daemon takes many minutes). The CLI's 30s default would
// abort the request at 30s AND — because Go's client cancels the request
// on timeout, which cancels the handler's context — tear down the build
// it was waiting for. The operator would get `context deadline exceeded`
// and no container, on the never-provisioned crew this command exists to
// rescue.
const crewStartTimeout = 20 * time.Minute

func init() {
	crewStartCmd.Flags().Duration("timeout", 0,
		"Max wait for the container to come up (default 20m; a cold crew is provisioned first)")
	crewCmd.AddCommand(crewStartCmd)
	crewCmd.AddCommand(crewStopCmd)
}

// crewStopCmd is the counterpart to `crew start` — POST
// /api/v1/crews/{crewId}/container-stop.
//
// It exists because until now a crew container could be started on
// purpose but only stopped by accident: an idle TTL expiring, or a
// network-policy edit dropping it as a side effect. An operator who
// started three crews to land a restore had no way to give the memory
// back.
//
// Stopping an already-stopped crew succeeds. That is the state the
// caller asked for, and an error would make every script wrap the call
// in a status check.
var crewStopCmd = &cobra.Command{
	Use:   "stop <slug-or-id>",
	Short: "Stop a crew's runtime container and its sidecar services",
	Long: `Stop a crew's runtime container, along with any sidecar services it
declared.

The crew's image and its shared files are untouched — only the running
container goes away. The next agent run (or 'crewship crew start') brings
it back, and it is recreated with the crew's current memory/CPU limits,
so this is also how a resize takes effect.

Stopping an already-stopped crew succeeds.`,
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

		resp, err := client.Post("/api/v1/crews/"+crewID+"/container-stop", nil)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var payload map[string]any
		if err := cli.ReadJSON(resp, &payload); err != nil {
			return err
		}
		return newFormatter().AutoHuman(payload, func() {
			status, _ := payload["status"].(string)
			if status == "" {
				status = "unknown"
			}
			fmt.Fprintf(os.Stdout, "%sContainer:%s %s%s%s\n", cli.Bold, cli.Reset,
				containerStatusColor(status), sanitizeTerminal(status), cli.Reset)
		})
	},
}
