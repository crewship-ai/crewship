//go:build !clionly

package main

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

var adminReapOrphanApply bool

// adminReapOrphanCmd detects — and with --apply reaps — crew containers left
// orphaned by an internal-token master rotation across a server restart
// (#1385). Such a container survives the restart holding a crew-bound token
// minted under the OLD master, which the new process rejects forever ("invalid
// crew-bound token"): its credential sync is silently broken and it spams the
// server log every reap interval. PR #1387's persisted master stops FUTURE
// restarts from creating orphans; this command clears the ones that outlived
// the deploy that first rotates the master.
//
// Like prune-legacy / prune-crew-runtimes it is HTTP-backed: the docker daemon
// lives behind the running server, so it needs an authenticated session and a
// reachable server. Dry-run by default — it only reaps when --apply is passed.
var adminReapOrphanCmd = &cobra.Command{
	Use:   "reap-orphan-containers",
	Short: "Detect (and with --apply reap) crew containers holding a stale internal token (admin; needs a running server)",
	Long: `Finds crew containers whose sidecar holds a crew-bound internal token
minted under a PREVIOUS internal-token master — orphaned when a server restart
rotated the master. The new server rejects their token forever ("invalid
crew-bound token"), silently breaking credential sync and spamming the log.

Detection is positive and fail-safe: a container is only listed when its
sidecar advertises a token fingerprint that definitively disagrees with the one
the server would mint today. Healthy, unreachable, or crew-less containers are
never touched.

By default this only REPORTS the orphaned containers (dry-run). Pass --apply to
stop+remove them; the next dispatch to each crew recreates the container fresh
and it re-mints a valid token. Any agent inside an orphaned container is already
broken (its credentials can't sync), so recreation restores it.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		path := "/api/v1/admin/reap-orphan-containers"
		if adminReapOrphanApply {
			path += "?apply=true"
		}
		client := newAPIClient()
		resp, err := client.Post(path, nil)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		var out struct {
			Error   string `json:"error"`
			Applied bool   `json:"applied"`
			Count   int    `json:"count"`
			Orphans []struct {
				CrewID      string `json:"crew_id"`
				Slug        string `json:"slug"`
				ContainerID string `json:"container_id"`
				Reaped      bool   `json:"reaped"`
			} `json:"orphans"`
			// #1390 coverage: how many containers the sweep reached and how
			// many it could actually classify.
			//
			// Pointers on purpose. A server predating this field omits it, and
			// a plain int would decode to 0 — indistinguishable from a server
			// that genuinely inspected nothing. That would have the CLI assert
			// "no running crew containers" against an older instance where the
			// truth is unknown. nil = not reported → fall back to the old,
			// non-committal wording.
			Inspected     *int `json:"inspected"`
			Identified    *int `json:"identified"`
			DetectorInert bool `json:"detector_inert"`
		}
		_ = json.Unmarshal(data, &out)

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			if out.Error != "" {
				return fmt.Errorf("reap-orphan-containers failed (HTTP %d): %s", resp.StatusCode, out.Error)
			}
			return fmt.Errorf("reap-orphan-containers failed: HTTP %d", resp.StatusCode)
		}

		// -f json/yaml/ndjson must emit the decoded payload, not the prose
		// below. This command predates the formatter helpers and printed
		// straight to stdout on every path, so `--format json` silently
		// returned the human text — including the coverage fields the docs
		// promise are machine-readable.
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			// "Nothing found" is only reassuring if the detector could actually
			// look. An empty token fingerprint fails SAFE (never reaped), which
			// means a slot whose sidecar binary was never re-pointed reports a
			// clean sweep forever — the #1390 failure. Say which one this was.
			if out.DetectorInert {
				inspected := 0
				if out.Inspected != nil {
					inspected = *out.Inspected
				}
				fmt.Printf("DETECTOR INERT — inspected %d running crew container(s), but NONE advertised a token fingerprint.\n", inspected)
				fmt.Println("\"No orphans\" here means \"could not tell\", not \"none\": the sidecar binary is")
				fmt.Println("probably stale (pre-#1385), so this sweep cannot detect an orphan at all.")
				fmt.Println("Fix the slot's sidecar (it must be rebuilt + re-pointed on reconcile, #1390),")
				fmt.Println("then re-run. Nothing was reaped.")
				return
			}

			if out.Count == 0 {
				if out.Inspected == nil || out.Identified == nil {
					// Server predates the coverage fields — don't invent certainty
					// it never expressed.
					fmt.Println("No orphaned crew containers found — nothing to reap.")
					fmt.Println("(This server does not report detector coverage; upgrade it to tell a clean sweep from an inert detector — see #1390.)")
					return
				}
				if *out.Inspected == 0 {
					fmt.Println("No running crew containers to inspect — nothing to reap.")
					return
				}
				fmt.Printf("No orphaned crew containers found — %d of %d inspected container(s) reported a token fingerprint and matched.\n",
					*out.Identified, *out.Inspected)
				if *out.Identified < *out.Inspected {
					fmt.Printf("Note: %d container(s) advertised no fingerprint and could not be classified.\n",
						*out.Inspected-*out.Identified)
				}
				return
			}

			if out.Applied {
				fmt.Printf("Found %d orphaned crew container(s):\n", out.Count)
			} else {
				fmt.Printf("Found %d orphaned crew container(s) (dry-run — re-run with --apply to reap):\n", out.Count)
			}
			for _, o := range out.Orphans {
				status := "stale token"
				if out.Applied {
					if o.Reaped {
						status = "reaped"
					} else {
						status = "reap FAILED (see server log)"
					}
				}
				fmt.Printf("  - crew %s (%s) container %s — %s\n", o.Slug, o.CrewID, o.ContainerID, status)
			}
		})
	},
}

func init() {
	adminReapOrphanCmd.Flags().BoolVar(&adminReapOrphanApply, "apply", false,
		"actually stop+remove the orphaned containers (default: report only)")
	adminCmd.AddCommand(adminReapOrphanCmd)
}
