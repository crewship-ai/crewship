package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// keeperJudgeCmd drives the two endpoints that make configuring a local judge a
// one-minute job instead of a guessing game:
//
//	POST /api/v1/admin/keeper/judge/test    — four stages
//	GET  /api/v1/admin/keeper/judge/models  — what that endpoint serves
//
// Keeper is fail-closed, so every way of being misconfigured arrives as the same
// DENY on every credential request. The stages separate the causes an operator can
// act on: nothing is listening, the model is not pulled, the model cannot produce a
// verdict, or it produces one too slowly for the credential path to wait.
var keeperJudgeCmd = &cobra.Command{
	Use:   "judge",
	Short: "Verify the credential-access judge and list the models it can use",
	Long: `Check the Keeper judge (requires OWNER or ADMIN).

  judge test    reach the endpoint → is the model pulled → does it return a verdict
                → does it answer inside the budget
  judge models  list the models the configured endpoint actually serves

Both accept --endpoint / --model to check values you have not saved yet, so you
can find a working combination before committing it.

Examples:
  crewship keeper judge test
  crewship keeper judge models --endpoint http://192.168.1.222:11434
  crewship keeper judge test --endpoint http://localhost:11434 --model qwen2.5:7b`,
}

// keeperJudgeStage / keeperJudgeTestResult mirror internal/api/admin_keeper_judge.go.
type keeperJudgeStage struct {
	Name      string `json:"name"`
	Label     string `json:"label"`
	OK        bool   `json:"ok"`
	Skipped   bool   `json:"skipped"`
	Detail    string `json:"detail"`
	LatencyMS int64  `json:"latency_ms"`
}

type keeperJudgeTestResult struct {
	OK       bool               `json:"ok"`
	Endpoint string             `json:"endpoint"`
	Model    string             `json:"model"`
	Stages   []keeperJudgeStage `json:"stages"`
	Models   []string           `json:"models"`
	Decision string             `json:"decision"`
}

type keeperJudgeModelsResult struct {
	Endpoint string   `json:"endpoint"`
	Models   []string `json:"models"`
	Error    string   `json:"error"`
}

var (
	flagKeeperJudgeEndpoint string
	flagKeeperJudgeModel    string
)

var keeperJudgeTestCmd = &cobra.Command{
	Use:   "test",
	Short: "Run the four-stage judge check (requires OWNER or ADMIN)",
	Long: `Reach the endpoint, confirm the model is pulled, make the model return a real
verdict on a miniature gatekeeper prompt, and check it did so inside the budget the
credential path allows.

Stage 3 is the one a ping cannot give you: a model that answers in prose, or one
too small to follow the format, passes the first two stages and then denies every
credential request in production.

Stage 4 is the one this check itself used to get wrong. It measured with its own
generous timeout, so a judge that answered in 12s against a 5s credential budget
showed three green ticks and then denied everything. Now the measured latency is
compared with the configured budget ('keeper config set --judge-timeout').

Exits non-zero when any stage fails, so it works in a script or a cron.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		body := map[string]any{}
		if v := strings.TrimSpace(flagKeeperJudgeEndpoint); v != "" {
			body["judge_endpoint_url"] = v
		}
		if v := strings.TrimSpace(flagKeeperJudgeModel); v != "" {
			body["judge_model"] = v
		}

		var out keeperJudgeTestResult
		if err := postJSON(client, "/api/v1/admin/keeper/judge/test", body, &out); err != nil {
			return keeperPermissionHint(err)
		}

		if ferr := newFormatter().AutoHuman(out, func() {
			fmt.Printf("%sJudge check%s  %s", cli.Bold, cli.Reset, out.Endpoint)
			if out.Model != "" {
				fmt.Printf("  ·  %s", out.Model)
			}
			fmt.Println()
			for _, s := range out.Stages {
				mark, colour := "✗", cli.Red
				switch {
				case s.OK:
					mark, colour = "✓", cli.Green
				case s.Skipped:
					mark, colour = "–", cli.Dim
				}
				latency := ""
				if s.LatencyMS > 0 {
					latency = fmt.Sprintf(" %s(%dms)%s", cli.Dim, s.LatencyMS, cli.Reset)
				}
				fmt.Printf("  %s%s%s %-22s %s%s\n", colour, mark, cli.Reset, s.Label, s.Detail, latency)
			}
			if len(out.Models) > 0 {
				fmt.Printf("%sModels on this endpoint:%s %s\n", cli.Dim, cli.Reset, strings.Join(out.Models, ", "))
			}
			if out.OK {
				cli.PrintSuccess("The judge works. Credential decisions will reach this model.")
			}
		}); ferr != nil {
			return ferr
		}
		if !out.OK {
			// The exit code is the machine-readable half of the same answer.
			return cli.WithExitCode(fmt.Errorf("the judge is not usable yet — see the failed stage above"), cli.ExitGeneric)
		}
		return nil
	},
}

var keeperJudgeModelsCmd = &cobra.Command{
	Use:   "models",
	Short: "List the models the judge endpoint serves (requires OWNER or ADMIN)",
	Long: `Ask the configured endpoint what it has pulled, so a model name is something you
pick rather than something you type from memory. --endpoint checks an address you
have not saved yet.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		path := "/api/v1/admin/keeper/judge/models"
		if v := strings.TrimSpace(flagKeeperJudgeEndpoint); v != "" {
			path += queryString("endpoint", v)
		}
		var out keeperJudgeModelsResult
		if err := getJSON(client, path, &out); err != nil {
			return keeperPermissionHint(err)
		}

		if ferr := newFormatter().AutoHuman(out, func() {
			if out.Error != "" {
				cli.PrintError(out.Error)
				return
			}
			fmt.Printf("%sModels on %s%s\n", cli.Bold, out.Endpoint, cli.Reset)
			if len(out.Models) == 0 {
				fmt.Printf("  %s(none pulled — run `ollama pull qwen2.5:7b`)%s\n", cli.Yellow, cli.Reset)
				return
			}
			for _, m := range out.Models {
				fmt.Printf("  %s\n", m)
			}
		}); ferr != nil {
			return ferr
		}
		if out.Error != "" {
			return cli.WithExitCode(fmt.Errorf("could not list models"), cli.ExitGeneric)
		}
		return nil
	},
}

func init() {
	for _, c := range []*cobra.Command{keeperJudgeTestCmd, keeperJudgeModelsCmd} {
		c.Flags().StringVar(&flagKeeperJudgeEndpoint, "endpoint", "", "check this endpoint instead of the saved one")
	}
	keeperJudgeTestCmd.Flags().StringVar(&flagKeeperJudgeModel, "model", "", "check this model instead of the saved one")

	keeperJudgeCmd.AddCommand(keeperJudgeTestCmd)
	keeperJudgeCmd.AddCommand(keeperJudgeModelsCmd)
	keeperCmd.AddCommand(keeperJudgeCmd)
}
