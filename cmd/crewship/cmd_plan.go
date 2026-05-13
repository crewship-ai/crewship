package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// planSystemPrefix is prepended to any prompt run under plan mode.
//
// The agent stays a normal CHAT agent server-side — plan mode is
// achieved by prompting alone, not by a separate API mode. This keeps
// the feature shippable in one PR (no backend mode/flag plumbing) at
// the cost of relying on instruction-following: if the agent ignores
// "do not call tools", we degrade gracefully into a verbose preview.
//
// The wording is deliberately concrete:
//   - "step-by-step plan" — Aider-style architect output
//   - "files you would touch" — Cline-style preview
//   - "do NOT execute any tool" — explicit guard against tool-use
//   - "end with: PLAN READY" — terminator the user / shell can grep
const planSystemPrefix = `[plan-mode] You are in PLAN mode. Do NOT call any tools, do NOT modify files, do NOT run code. Output ONLY:

1. A concise step-by-step plan (numbered).
2. The files you would touch (bulleted).
3. Risks or open questions.
4. End with the literal line: PLAN READY

User request follows:

---

`

var planCmd = &cobra.Command{
	Use:   "plan [prompt]",
	Short: "Get a read-only plan before executing (architect mode)",
	Long: `Run the default agent in PLAN mode: the agent outputs a step-by-step
plan plus the files it would touch, without executing any tools or
modifying anything.

Useful before kicking off a long autonomous run — review the plan,
adjust the prompt, then re-run with 'crewship run' or 'crewship ask'
once the approach looks right.

Examples:
  crewship plan "rewrite auth to use JWT"
  git diff | crewship plan "what would you do next?"
  crewship plan --agent viktor "fix the failing tests"`,
	Args: cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Prepend the plan-mode system prefix as a positional arg so
		// BuildPrompt picks it up. Anything the user typed is joined and
		// appended after the prefix.
		userText := strings.TrimSpace(strings.Join(args, " "))
		flagPrompt, _ := cmd.Flags().GetString("prompt")
		if userText == "" && flagPrompt == "" {
			return fmt.Errorf("plan requires a prompt (positional, --prompt, or stdin)")
		}

		// Inject the plan prefix into --prompt rather than into args so
		// `--prompt @file` / `@-` still work; BuildPrompt treats it as
		// raw text when it doesn't start with '@'.
		combined := planSystemPrefix
		if flagPrompt != "" {
			combined += flagPrompt
		} else {
			combined += userText
		}
		_ = cmd.Flags().Set("prompt", combined)

		// Force quiet by default so the plan output isn't lost in [agent: …] noise.
		if !cmd.Flags().Changed("quiet") {
			_ = cmd.Flags().Set("quiet", "true")
		}

		// Mark plan-mode in chat metadata so the FE / journal can label
		// these sessions distinctly. Backend ignores unknown metadata so
		// this is forward-compatible.
		planModeRequested = true
		defer func() { planModeRequested = false }()

		// Reset positional args — combined prefix is now in --prompt.
		if askCmd.RunE != nil {
			return askCmd.RunE(askCmd, nil)
		}
		return fmt.Errorf("internal: ask command has no RunE")
	},
}

// planModeRequested is a package-level latch so the chat-creation POST
// in ask/run can stamp metadata.plan_mode=true without threading a new
// parameter through every helper. Set by `plan` (or by the `--plan`
// flag handler in cmd_run.go / cmd_ask.go) before RunE; cleared on
// return.
//
// This is deliberately a global rather than a context value:
//   - ask/run/plan all share the same goroutine for the whole CLI run.
//   - There is no concurrent invocation (the CLI is single-shot per
//     process), so no race exists.
//   - It avoids changing the askCmd.RunE signature.
var planModeRequested bool

// ApplyPlanFlag is called by `run` / `ask` when their --plan flag is
// set to make their POST /chats body include metadata.plan_mode=true
// and to prepend the system prefix into the prompt the same way the
// top-level `plan` command does.
//
// Returns the updated prompt text. Callers pass the result into the
// existing BuildPrompt → POST path.
func ApplyPlanFlag(prompt string) string {
	if !planModeRequested {
		return prompt
	}
	if strings.HasPrefix(prompt, "[plan-mode]") {
		return prompt
	}
	return planSystemPrefix + prompt
}

func init() {
	planCmd.Flags().String("agent", "", "Agent slug or ID (overrides default-agent)")
	planCmd.Flags().StringP("prompt", "p", "", "Prompt text, @file, or @- for stdin")
	planCmd.Flags().BoolP("quiet", "q", false, "Only output agent text")
	planCmd.Flags().Bool("with-git-diff", false, "Append `git diff` as context")
	planCmd.Flags().Bool("with-git-staged", false, "Append `git diff --staged` as context")
	planCmd.Flags().Bool("with-git-log", false, "Append last 20 commits as context")
	planCmd.Flags().Bool("with-git-status", false, "Append `git status -s` as context")
	planCmd.Flags().StringSlice("with-file", nil, "Append file content(s) as context (repeatable)")
	planCmd.Flags().StringSlice("with-cmd", nil, "Append shell command output as context (repeatable)")
	planCmd.Flags().Bool("paste", false, "Append the system clipboard as context")
	planCmd.Flags().Bool("markdown", false, "Render markdown ANSI styling (overrides config)")
	planCmd.Flags().Bool("no-markdown", false, "Disable markdown ANSI styling (overrides config)")

	rootCmd.AddCommand(planCmd)
}

// Tiny compile-time guard so unused-import lint passes when other files
// in this package drop the cli import.
var _ = cli.Reset
