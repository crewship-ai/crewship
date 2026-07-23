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
		}
		_ = json.Unmarshal(data, &out)

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			if out.Error != "" {
				return fmt.Errorf("reap-orphan-containers failed (HTTP %d): %s", resp.StatusCode, out.Error)
			}
			return fmt.Errorf("reap-orphan-containers failed: HTTP %d", resp.StatusCode)
		}

		if out.Count == 0 {
			fmt.Println("No orphaned crew containers found — nothing to reap.")
			return nil
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
		return nil
	},
}

func init() {
	adminReapOrphanCmd.Flags().BoolVar(&adminReapOrphanApply, "apply", false,
		"actually stop+remove the orphaned containers (default: report only)")
	adminCmd.AddCommand(adminReapOrphanCmd)
}
