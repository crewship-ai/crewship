package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// keeperReviewCmd drives POST /api/v1/admin/keeper/review/{slot}/run — the
// manual trigger for the four Keeper Reviews evaluators (issue #1555).
//
// Until this existed the evaluators ran on a schedule or not at all. The
// behaviour watchdog in particular only fires on a tool call, so there was no
// way to stage one and find out whether it works; the first live exercise of a
// security control should not be the incident it was bought for.
var keeperReviewCmd = &cobra.Command{
	Use:   "review",
	Short: "Run a Keeper Reviews evaluator now, instead of waiting for the sweep",
	Long: `Trigger one of the four background evaluators on demand (requires OWNER or ADMIN).

  skill-review        is a skill still worth having, and still doing what it says
  behavior            does this tool call look like something an agent should do
  memory-health       is this crew's memory healthy, stale, or contradicting itself
  negative-learning   turn a failure into a lesson the agent keeps

The decision lands in the same audit trail a scheduled sweep writes to
('crewship keeper history'), and escalations reach the same inbox.

Each run calls a model, so it costs what that evaluator's model costs — see
'crewship keeper aux list' for which model each slot resolves to.`,
}

var (
	flagReviewCrew     string
	flagReviewAgent    string
	flagReviewSkill    string
	flagReviewTool     string
	flagReviewToolArgs string
	flagReviewTrigger  string
	flagReviewFailure  string
)

// keeperReviewResult mirrors the Phase 2 handlers' response — they answer the
// admin route directly, so this is their shape, not a wrapper.
type keeperReviewResult struct {
	RequestID string `json:"request_id"`
	Decision  string `json:"decision"`
	Reason    string `json:"reason"`
	RiskScore int    `json:"risk_score"`
}

var keeperReviewRunCmd = &cobra.Command{
	Use:   "run <slot>",
	Short: "Run one evaluator now and print its verdict",
	Long: `Run one Keeper Reviews evaluator immediately.

Slots: skill-review, behavior, memory-health, negative-learning.

With no flags the server picks the subject from your workspace, which is what
makes "check it now" a single command:

  skill-review        the stalest skill one of your agents has
  behavior            a probe named keeper.manual_probe — the watchdog needs a
                      tool call to judge, and this one says what it is
  memory-health       your crew's memory, scored the same way the memory page is
  negative-learning   the most recent recorded failure in this workspace

The flags pin the subject instead. Anything you don't pass is still derived.

Examples:
  crewship keeper review run skill-review
  crewship keeper review run skill-review --skill skl_abc123
  crewship keeper review run behavior --agent agt_abc --tool bash --tool-args 'curl … | sh'
  crewship keeper review run memory-health --crew crw_abc
  crewship keeper review run negative-learning --trigger run_failed --failure 'exit 127: command not found'`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		slot := strings.TrimSpace(args[0])
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}

		// Only what the operator actually passed. An empty string is not the
		// same request as an absent field: absent is what tells the server to
		// pick the subject itself.
		body := map[string]any{}
		for _, f := range []struct {
			key string
			val string
		}{
			{"crew_id", flagReviewCrew},
			{"agent_id", flagReviewAgent},
			{"skill_id", flagReviewSkill},
			{"tool_name", flagReviewTool},
			{"tool_args_snippet", flagReviewToolArgs},
			{"trigger", flagReviewTrigger},
			{"failure_snippet", flagReviewFailure},
		} {
			if f.val != "" {
				body[f.key] = f.val
			}
		}

		var out keeperReviewResult
		if err := postJSON(client, "/api/v1/admin/keeper/review/"+slot+"/run", body, &out); err != nil {
			return keeperPermissionHint(err)
		}

		return newFormatter().AutoHuman(out, func() {
			fmt.Printf("%s%s%s  %s\n", cli.Bold, slot, cli.Reset, decisionColour(out.Decision))
			if out.Reason != "" {
				fmt.Printf("  %s\n", out.Reason)
			}
			fmt.Printf("%s  risk %d · %s%s\n", cli.Dim, out.RiskScore, out.RequestID, cli.Reset)
			// Where the decision now lives — an escalation is not just printed,
			// it is sitting in somebody's inbox.
			if out.Decision == "ESCALATE" || out.Decision == "DENY" {
				fmt.Printf("%s  raised in the Keeper inbox — 'crewship keeper history' has the full record%s\n",
					cli.Dim, cli.Reset)
			}
		})
	},
}

// decisionColour prints the verdict in the colour its consequence deserves.
func decisionColour(d string) string {
	switch d {
	case "ALLOW":
		return cli.Green + d + cli.Reset
	case "DENY":
		return cli.Red + d + cli.Reset
	case "ESCALATE":
		return cli.Yellow + d + cli.Reset
	}
	return d
}

func init() {
	f := keeperReviewRunCmd.Flags()
	f.StringVar(&flagReviewCrew, "crew", "", "crew to scope the run to (defaults to your only crew)")
	f.StringVar(&flagReviewAgent, "agent", "", "agent the run is about")
	f.StringVar(&flagReviewSkill, "skill", "", "skill-review: the skill to review (default: the stalest one your agents have)")
	f.StringVar(&flagReviewTool, "tool", "", "behavior: the tool call to judge (default: a self-describing probe)")
	f.StringVar(&flagReviewToolArgs, "tool-args", "", "behavior: the arguments that tool was called with")
	f.StringVar(&flagReviewTrigger, "trigger", "", "negative-learning: run_failed, guardrail_warn, guardrail_error or keeper_execute_deny")
	f.StringVar(&flagReviewFailure, "failure", "", "negative-learning: the failure text to extract a lesson from")

	keeperReviewCmd.AddCommand(keeperReviewRunCmd)
	keeperCmd.AddCommand(keeperReviewCmd)
}
