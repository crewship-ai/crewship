package main

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

func addPoolLifecycleCommands(root *cobra.Command, check func(*cobra.Command, []string) error) {
	for _, method := range []string{"update", "delete"} {
		cmd := &cobra.Command{Use: method + " <pool-id> --revision <revision>", Short: method + " an administrative provider pool definition", Args: cobra.ExactArgs(1), PreRunE: check}
		cmd.Flags().Int64("revision", 0, "Revision from pool get; stale changes are rejected")
		if method == "update" {
			cmd.Flags().String("name", "", "Replacement pool name")
			cmd.Flags().StringArray("member", nil, "Complete replacement membership: ID or ID=priority; repeat for each account")
			cmd.Flags().Bool("allow-cross-owner", false, "Explicit consent to pooling different owners; must be supplied again when updating")
		}
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			revision, _ := cmd.Flags().GetInt64("revision")
			if revision < 1 {
				return cli.WithExitCode(fmt.Errorf("provide --revision from pool get"), cli.ExitValidation)
			}
			client := newAPIClient().WithContext(cmd.Context()).WithHeader("If-Match", `"`+strconv.FormatInt(revision, 10)+`"`)
			path := "/api/v1/provider-logins/pools/" + url.PathEscape(args[0])
			if method == "delete" {
				resp, err := client.Delete(path)
				if err != nil {
					return err
				}
				if err := cli.CheckError(resp); err != nil {
					return err
				}
				resp.Body.Close()
				return newFormatter().AutoDetail(map[string]any{"deleted": true, "id": args[0]}, [][]string{{"Pool", args[0]}, {"Status", "Removed; provider accounts retained"}})
			}
			name := strings.TrimSpace(mustFlagString(cmd, "name"))
			raw, _ := cmd.Flags().GetStringArray("member")
			if name == "" || len(raw) < 1 || len(raw) > 100 {
				return cli.WithExitCode(fmt.Errorf("provide --name and 1–100 --member IDs"), cli.ExitValidation)
			}
			members := make([]poolMemberOut, 0, len(raw))
			seen := map[string]bool{}
			for _, value := range raw {
				id, rank, has := strings.Cut(value, "=")
				id = strings.TrimSpace(id)
				priority := 0
				if has {
					var err error
					priority, err = strconv.Atoi(rank)
					if err != nil {
						return cli.WithExitCode(fmt.Errorf("member priority must be an integer"), cli.ExitValidation)
					}
				}
				if id == "" || seen[id] {
					return cli.WithExitCode(fmt.Errorf("member IDs must be nonempty and unique"), cli.ExitValidation)
				}
				seen[id] = true
				members = append(members, poolMemberOut{id, priority})
			}
			consent, _ := cmd.Flags().GetBool("allow-cross-owner")
			resp, err := client.Put(path, map[string]any{"name": name, "members": members, "allow_cross_owner": consent})
			if err != nil {
				return err
			}
			if err := cli.CheckError(resp); err != nil {
				return err
			}
			resp.Body.Close()
			return newFormatter().AutoDetail(map[string]any{"id": args[0], "revision": revision + 1}, [][]string{{"Pool", args[0]}, {"Revision", strconv.FormatInt(revision+1, 10)}, {"Status", "Definition updated; no account access granted"}})
		}
		root.AddCommand(cmd)
	}
}
