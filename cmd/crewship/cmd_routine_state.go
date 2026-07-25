package main

// Cross-run routine state CLI (#1420 follow-up) — the operator front end for
// pipeline_routine_state.
//
// A routine's watermark ({{ routine.state.* }} read, `state_write` step write)
// is the primitive behind "process only what's new since last run". It was
// write-only from inside a run: nothing could show an operator the current
// cursor, and nothing could correct a bad one. That makes the failure silent
// and permanent — a cursor set to a future timestamp means every later run
// finds nothing to do and reports success forever.
//
// `crewship routine state` closes that: list (all buckets by default, because
// the stuck cursor is usually in a bucket you can't guess), set, rm, clear.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// routineStateEntry mirrors internal/pipeline.StateEntry.
type routineStateEntry struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	UpdatedAt string `json:"updated_at"`
}

// routineStateBucket mirrors internal/pipeline.StateBucket.
type routineStateBucket struct {
	ScheduleID string              `json:"schedule_id"`
	Entries    []routineStateEntry `json:"entries"`
}

// routineStateResp mirrors internal/api.routineStateResponse.
type routineStateResp struct {
	Slug    string               `json:"slug"`
	Buckets []routineStateBucket `json:"buckets"`
}

var routineStateCmd = &cobra.Command{
	Use:   "state",
	Short: "Inspect and repair a routine's durable cross-run state (watermarks)",
	Long: `A routine's cross-run state is the durable key/value bucket behind
{{ routine.state.* }} and a step's state_write binding — the watermark
that makes "process only what's new since last run" work.

State is scoped per (routine, schedule): two schedules of the same
routine keep INDEPENDENT cursors, and manual/webhook runs share a
separate bucket shown as "(manual/webhook)". That isolation is why
` + "`state list`" + ` shows every bucket unless you narrow it — when a routine
"stops seeing new items", the stuck cursor is usually in a bucket you
would not have guessed.

Mutations are ADMIN-tier: a watermark governs what every future
UNATTENDED run does, so rewriting one has the same blast radius as
disabling the routine.

Examples:
  crewship routine state list my-routine
  crewship routine state list my-routine --schedule psched_abc123
  crewship routine state set my-routine cursor 2026-07-25 --schedule psched_abc123
  crewship routine state rm my-routine cursor --schedule psched_abc123
  crewship routine state clear my-routine --schedule psched_abc123
`,
}

// stateBucketLabel names a bucket for humans. The empty schedule id is the
// shared manual/webhook bucket — printing a bare "" would read as a bug.
func stateBucketLabel(scheduleID string) string {
	if scheduleID == "" {
		return "(manual/webhook)"
	}
	return scheduleID
}

// stateScheduleQuery builds the ?schedule_id= suffix. It returns an empty
// string when the flag was never passed so the server falls through to
// all-buckets — an explicitly empty --schedule "" still selects the manual
// bucket, which is why this keys off Changed() rather than the value.
func stateScheduleQuery(cmd *cobra.Command) string {
	if !cmd.Flags().Changed("schedule") {
		return ""
	}
	v, _ := cmd.Flags().GetString("schedule")
	return "?schedule_id=" + url.QueryEscape(v)
}

var routineStateListCmd = &cobra.Command{
	Use:     "list <slug>",
	Aliases: []string{"ls", "get"},
	Short:   "Show a routine's durable state, grouped by schedule bucket",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		resp, err := client.Get(fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/state%s",
			ws, url.PathEscape(args[0]), stateScheduleQuery(cmd)))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out routineStateResp
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			if len(out.Buckets) == 0 {
				fmt.Printf("%s: no cross-run state written yet.\n", out.Slug)
				fmt.Println("A routine writes state via a step's state_write binding and reads it back as {{ routine.state.<key> }}.")
				return
			}
			for i, b := range out.Buckets {
				if i > 0 {
					fmt.Println()
				}
				fmt.Printf("schedule: %s\n", stateBucketLabel(b.ScheduleID))
				w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintln(w, "KEY\tVALUE\tUPDATED")
				for _, e := range b.Entries {
					fmt.Fprintf(w, "%s\t%s\t%s\n", e.Key, e.Value, e.UpdatedAt)
				}
				if err := w.Flush(); err != nil {
					fmt.Fprintf(os.Stderr, "warning: %v\n", err)
				}
			}
		})
	},
}

var routineStateSetCmd = &cobra.Command{
	Use:   "set <slug> <key> <value>",
	Short: "Set one state key (ADMIN) — e.g. rewind a stuck watermark",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		scheduleID, _ := cmd.Flags().GetString("schedule")
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		resp, err := client.Put(
			fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/state/%s",
				ws, url.PathEscape(args[0]), url.PathEscape(args[1])),
			map[string]any{"value": args[2], "schedule_id": scheduleID})
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("%s state %s=%s set for schedule %s.",
			args[0], args[1], args[2], stateBucketLabel(scheduleID)))
		fmt.Println("The next run of this routine reads the new value from {{ routine.state." + args[1] + " }}.")
		return nil
	},
}

var routineStateRmCmd = &cobra.Command{
	Use:     "rm <slug> <key>",
	Aliases: []string{"delete", "unset"},
	Short:   "Remove one state key (ADMIN)",
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		scheduleID, _ := cmd.Flags().GetString("schedule")
		if err := confirmAction(cmd, fmt.Sprintf(
			"Remove state key %q from routine %q (schedule %s)? The next run reads it as empty.",
			args[1], args[0], stateBucketLabel(scheduleID))); err != nil {
			return err
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		resp, err := client.Delete(fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/state/%s?schedule_id=%s",
			ws, url.PathEscape(args[0]), url.PathEscape(args[1]), url.QueryEscape(scheduleID)))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Removed state key %q from %q (schedule %s).",
			args[1], args[0], stateBucketLabel(scheduleID)))
		return nil
	},
}

var routineStateClearCmd = &cobra.Command{
	Use:   "clear <slug>",
	Short: "Drop every state key in one schedule's bucket (ADMIN)",
	Long: `Clears one (routine, schedule) bucket. There is deliberately no
"clear every schedule" form: each schedule's cursor is an independent
watermark, and dropping all of them at once makes every schedule
reprocess its whole backlog with no undo.

A routine whose watermark is gone starts over from its DSL's default —
for most incremental routines that means a full reprocess on the next
run. Prefer ` + "`state set`" + ` to a known-good value when you can.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		scheduleID, _ := cmd.Flags().GetString("schedule")
		if err := confirmAction(cmd, fmt.Sprintf(
			"Clear ALL cross-run state for routine %q (schedule %s)? The next run may reprocess its whole backlog.",
			args[0], stateBucketLabel(scheduleID))); err != nil {
			return err
		}
		client := newAPIClient()
		ws := client.GetWorkspaceID()
		resp, err := client.Delete(fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/state?schedule_id=%s",
			ws, url.PathEscape(args[0]), url.QueryEscape(scheduleID)))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out struct {
			Removed int64 `json:"removed"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		cli.PrintSuccess(fmt.Sprintf("Cleared %d state key(s) from %q (schedule %s).",
			out.Removed, args[0], stateBucketLabel(scheduleID)))
		return nil
	},
}

func init() {
	const scheduleUsage = "schedule id that owns the bucket; omit on list to show every bucket, " +
		"pass an empty value for the shared manual/webhook bucket"
	routineStateListCmd.Flags().String("schedule", "", scheduleUsage)
	routineStateSetCmd.Flags().String("schedule", "", scheduleUsage)
	routineStateRmCmd.Flags().String("schedule", "", scheduleUsage)
	routineStateClearCmd.Flags().String("schedule", "", scheduleUsage)
	routineStateRmCmd.Flags().BoolP("yes", "y", false, "Skip confirmation")
	routineStateClearCmd.Flags().BoolP("yes", "y", false, "Skip confirmation")

	routineStateCmd.AddCommand(routineStateListCmd)
	routineStateCmd.AddCommand(routineStateSetCmd)
	routineStateCmd.AddCommand(routineStateRmCmd)
	routineStateCmd.AddCommand(routineStateClearCmd)
	pipelineCmd.AddCommand(routineStateCmd)
}
