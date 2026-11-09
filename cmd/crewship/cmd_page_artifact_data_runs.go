package main

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

// newArtifactDataRunsCommand records producer runs of dashboard datasets. It
// works with a CLI token only: CREWSHIP_AGENT_TOKEN runs map to artifact tool
// operations, which have no run operations yet. A run never writes data; the producer writes with `data batch` between start
// and finish, and readers see Refreshing, Failed and the last success.
func newArtifactDataRunsCommand() *cobra.Command {
	root := &cobra.Command{Use: "runs", Short: "Record and read producer refresh runs of dashboard datasets"}
	root.Long = `Record when a producer refreshes a dashboard collection.

start opens a run with an idempotency key; repeating the same key returns the
same run (exit 0, HTTP 200). Write the data with "data batch", then finish the
run as succeeded or failed. A failure reason is cleaned on the server (control
characters and common credential patterns removed, at most 500 bytes) and is shown to
the owner and editors only. The producer is the authenticated principal; it
cannot be supplied. The newest 20 runs and the newest success are kept per
collection. Starting and finishing need the same right as "data batch", and
any writer may finish any run. Agent runs that use the artifact tools cannot
record runs yet; use a CLI token.`
	root.Example = `  crewship page artifact data runs start sales-dashboard sales --base-version 3 --key nightly-2026-11-09
  crewship page artifact data runs finish sales-dashboard sales run_0123456789abcdef01234567 --base-version 3 --state succeeded
  crewship page artifact data runs list sales-dashboard --version 3`

	start := &cobra.Command{Use: "start <slug> <collection>", Short: "Start a producer run (idempotent per key)", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		base, _ := cmd.Flags().GetInt64("base-version")
		key, _ := cmd.Flags().GetString("key")
		if base < 1 || base > artifacts.MaxDataVersion {
			return fmt.Errorf("--base-version must be the current code version")
		}
		if key == "" || len(key) > artifacts.MaxDataRunIdempotent {
			return fmt.Errorf("--key is required (at most %d characters)", artifacts.MaxDataRunIdempotent)
		}
		return artifactCLIRequest(cmd, http.MethodPost, "/api/v1/pages/"+pagePathEscape(args[0])+"/artifact/data/"+pagePathEscape(args[1])+"/runs", map[string]any{"base_version": base, "idempotency_key": key})
	}}
	start.Flags().Int64("base-version", 0, "Current code version declaring the collection (required)")
	start.Flags().String("key", "", "Idempotency key: letters, digits, . _ : - (required)")

	finish := &cobra.Command{Use: "finish <slug> <collection> <run-id>", Short: "Finish a producer run as succeeded or failed", Args: cobra.ExactArgs(3), RunE: func(cmd *cobra.Command, args []string) error {
		base, _ := cmd.Flags().GetInt64("base-version")
		state, _ := cmd.Flags().GetString("state")
		reason, _ := cmd.Flags().GetString("reason")
		if reason == "-" {
			raw, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 4*artifacts.MaxDataRunReason+1))
			if err != nil {
				return err
			}
			reason = strings.TrimRight(string(raw), "\r\n")
		}
		if base < 1 || base > artifacts.MaxDataVersion {
			return fmt.Errorf("--base-version must be the current code version")
		}
		if state != "succeeded" && state != "failed" {
			return fmt.Errorf("--state must be succeeded or failed")
		}
		if state == "succeeded" && reason != "" {
			return fmt.Errorf("--reason is only accepted with --state failed")
		}
		body := map[string]any{"base_version": base, "state": state}
		if reason != "" {
			body["reason"] = reason
		}
		return artifactCLIRequest(cmd, http.MethodPost, "/api/v1/pages/"+pagePathEscape(args[0])+"/artifact/data/"+pagePathEscape(args[1])+"/runs/"+pagePathEscape(args[2]), body)
	}}
	finish.Flags().Int64("base-version", 0, "Current code version declaring the collection (required)")
	finish.Flags().String("state", "", "succeeded or failed (required)")
	finish.Flags().String("reason", "", "Failure reason shown to the owner and editors; - reads standard input")

	list := &cobra.Command{Use: "list <slug>", Short: "Show the latest run, last attempt and last success per collection", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		version, _ := cmd.Flags().GetInt64("version")
		if version < 1 || version > artifacts.MaxDataVersion {
			return fmt.Errorf("--version must be an exact positive code version")
		}
		return artifactCLIRequest(cmd, http.MethodGet, "/api/v1/pages/"+pagePathEscape(args[0])+"/artifact/data-runs?version="+strconv.FormatInt(version, 10), nil)
	}}
	list.Flags().Int64("version", 0, "Exact code version whose collections are listed (required)")
	root.AddCommand(start, finish, list)
	return root
}
