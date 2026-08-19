package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// keeperAuxCmd drives /api/v1/admin/keeper/aux — the models behind the Keeper
// Reviews sweeps and the behaviour watchdog, as opposed to `keeper config`, which
// is the credential-access judge.
//
// The split matters for cost: the judge is a local model and costs nothing per
// decision, while every evaluator call bills per token against ANTHROPIC_API_KEY.
// Those five slots used to be settable only through CREWSHIP_AUX_* at boot, so
// the one Keeper spend decision an operator could see on the admin page was the
// one they could not make.
var keeperAuxCmd = &cobra.Command{
	Use:   "aux",
	Short: "Evaluator models: what the Keeper Reviews sweeps and the watchdog run on",
	Long: `Inspect and change the INSTANCE-level evaluator models (requires OWNER or ADMIN).

These are the paid models in the Keeper stack. The credential-access judge
('crewship keeper config') is a local model and costs nothing per decision; each
of these slots calls a hosted model and bills per token:

  curator        skill review + memory consolidation (the consolidation
                 summariser resolves this slot per run, falling back to
                 KEEPER_OLLAMA_URL + KEEPER_MODEL when it cannot be built)
  behavior       tool-call behaviour monitor
  memory_health  memory-health audit
  negative       failure → lessons extraction
  run_summary    run summary verdicts
  fallback       used when a slot itself is unset

Each field either has an instance override or inherits the CREWSHIP_AUX_* value
the server booted with, and 'aux list' shows which — "instance" means set here,
"env" means inherited, "default" means the shipped value.

Every slot applies on the next evaluation — no restart, for any of them, and
that includes '--timeout': it is a real per-call deadline on every slot above.
The one exception is the memory-consolidation prompt behind 'curator', which is
batch work and stays on its provider's client timeout.

A hosted slot bills a key. By default that is the one in the server's own
environment; 'aux set <slot> --credential <name>' points it at a stored vault
key instead, which is how an instance holding several subscriptions says which
one a sweep spends.

Examples:
  crewship keeper aux list
  crewship keeper aux set behavior --model claude-opus-5
  crewship keeper aux set behavior --credential prod-anthropic
  crewship keeper aux set curator --provider ollama --model qwen2.5:7b
  crewship keeper aux test behavior      # call that evaluator's model once
  crewship keeper aux use-judge          # every slot onto the local judge, no per-token cost
  crewship keeper aux reset behavior
  crewship keeper aux reset --all`,
}

// The wire shapes mirror internal/api/admin_keeper_aux.go.
type keeperAuxIntField struct {
	Value    int64  `json:"value"`
	Source   string `json:"source"`
	Editable bool   `json:"editable"`
}

type keeperAuxSlot struct {
	Slot         string               `json:"slot"`
	Label        string               `json:"label"`
	Provider     keeperConfigStrField `json:"provider"`
	Model        keeperConfigStrField `json:"model"`
	TimeoutMS    keeperAuxIntField    `json:"timeout_ms"`
	CredentialID keeperConfigStrField `json:"credential_id"`

	Overridden bool   `json:"overridden"`
	UpdatedAt  string `json:"updated_at"`
	UpdatedBy  string `json:"updated_by"`
}

type keeperAuxConfig struct {
	Slots         []keeperAuxSlot `json:"slots"`
	Providers     []string        `json:"providers"`
	JudgeProvider string          `json:"judge_provider"`
	JudgeModel    string          `json:"judge_model"`
	AnyOverridden bool            `json:"any_overridden"`
}

const keeperAuxPath = "/api/v1/admin/keeper/aux"

func getKeeperAuxConfig(client *cli.Client) (keeperAuxConfig, error) {
	var cfg keeperAuxConfig
	if err := getJSON(client, keeperAuxPath, &cfg); err != nil {
		return keeperAuxConfig{}, keeperPermissionHint(err)
	}
	return cfg, nil
}

func printKeeperAuxConfig(cfg keeperAuxConfig) {
	fmt.Printf("%sKeeper evaluator models (instance)%s\n", cli.Bold, cli.Reset)
	for _, s := range cfg.Slots {
		fmt.Printf("  %s%-14s%s %s\n", cli.Bold, s.Slot, cli.Reset, cli.Dim+s.Label+cli.Reset)
		fmt.Printf("    Model:    %s / %s %s\n",
			orUnset(s.Provider.Value), orUnset(s.Model.Value), sourceNote(s.Model.Source))
		fmt.Printf("    Timeout:  %s %s\n",
			formatAuxTimeout(s.TimeoutMS.Value), sourceNote(s.TimeoutMS.Source))
		// Which subscription this slot bills. Only printed when one is pinned:
		// on an instance that never set one, "the server's own key" is noise on
		// every row, and every row is the default.
		if s.CredentialID.Value != "" {
			fmt.Printf("    Key:      %s %s\n", s.CredentialID.Value, sourceNote(s.CredentialID.Source))
		}
		if s.Overridden && s.UpdatedAt != "" {
			by := s.UpdatedBy
			if by == "" {
				by = "unknown"
			}
			fmt.Printf("    %sChanged %s by %s%s\n", cli.Dim, s.UpdatedAt, by, cli.Reset)
		}
	}
	if !cfg.AnyOverridden {
		fmt.Printf("%sNothing is overridden here — every slot runs on the server's configuration.%s\n",
			cli.Dim, cli.Reset)
	}
	if cfg.JudgeModel != "" {
		fmt.Printf("%sLocal judge: %s / %s — 'crewship keeper aux use-judge' points every slot at it (no per-token cost).%s\n",
			cli.Dim, orUnset(cfg.JudgeProvider), cfg.JudgeModel, cli.Reset)
	}
}

// formatAuxTimeout prints milliseconds as the duration an operator typed.
func formatAuxTimeout(ms int64) string {
	if ms <= 0 {
		return cli.Yellow + "(not set)" + cli.Reset
	}
	return (time.Duration(ms) * time.Millisecond).String()
}

var keeperAuxListCmd = &cobra.Command{
	Use:   "list",
	Short: "Show every evaluator slot and where its model comes from",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		cfg, err := getKeeperAuxConfig(client)
		if err != nil {
			return err
		}
		return newFormatter().AutoHuman(cfg, func() { printKeeperAuxConfig(cfg) })
	},
}

var (
	flagKeeperAuxProvider   string
	flagKeeperAuxModel      string
	flagKeeperAuxTimeout    string
	flagKeeperAuxCredential string
	flagKeeperAuxResetAll   bool
)

var keeperAuxSetCmd = &cobra.Command{
	Use:   "set <slot>",
	Short: "Change one evaluator slot (requires OWNER or ADMIN)",
	Long: `Set one or more fields on a single slot. Only the flags you pass are changed.

  --provider <name>  anthropic, openai, or ollama. 'ollama' means the instance
                     judge's endpoint, so that slot stops billing per token.
                     Pass "" to clear the override.
  --model <id>       e.g. claude-opus-5, claude-haiku-4-5, qwen2.5:7b.
                     Pass "" to clear the override.
  --timeout <dur>    per-call deadline, e.g. 30s. Pass "" to inherit.
  --credential <name>
                     which stored API_KEY this slot spends, BY NAME. Several
                     Anthropic keys is the normal case — each carries its own
                     subscription limit — and without this the slot bills
                     whatever key the server process was started with. Pass ""
                     to go back to that. Ignored by an 'ollama' slot, which
                     dials the local judge and needs no key.

A provider needs a model: the builder needs both, and a provider alone would
resolve to the fallback slot, which looks like the override was ignored.

Examples:
  crewship keeper aux set behavior --model claude-opus-5
  crewship keeper aux set curator --provider ollama --model qwen2.5:7b
  crewship keeper aux set memory_health --timeout 45s
  crewship keeper aux set behavior --credential prod-anthropic
  crewship keeper aux set behavior --credential ""            # server's own key
  crewship keeper aux set behavior --provider "" --model ""   # back to inherited`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		slot := strings.TrimSpace(args[0])
		body := map[string]any{}
		// Sent when the flag was PASSED, not when it is non-empty: "" is the
		// documented clear, so absent and empty cannot be the same request.
		if cmd.Flags().Changed("provider") {
			body["provider"] = strings.ToLower(strings.TrimSpace(flagKeeperAuxProvider))
		}
		if cmd.Flags().Changed("model") {
			body["model"] = strings.TrimSpace(flagKeeperAuxModel)
		}
		if cmd.Flags().Changed("timeout") {
			raw := strings.TrimSpace(flagKeeperAuxTimeout)
			if raw == "" {
				body["timeout_ms"] = 0 // 0 is the API's clear-to-inherit
			} else {
				d, err := time.ParseDuration(raw)
				if err != nil {
					return fmt.Errorf("invalid --timeout %q: use a duration like 30s or 1500ms", raw)
				}
				if d <= 0 {
					return fmt.Errorf(`invalid --timeout %q: must be positive (pass "" to inherit)`, raw)
				}
				body["timeout_ms"] = d.Milliseconds()
			}
		}
		wantCredential := cmd.Flags().Changed("credential")
		if len(body) == 0 && !wantCredential {
			return fmt.Errorf("nothing to change — pass --provider, --model, --timeout and/or --credential (see 'crewship keeper aux set --help')")
		}

		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		// --credential takes a NAME. "Which of my three Anthropic keys" is a
		// question an operator answers by name and never by CUID, so the name→id
		// resolution happens here rather than being pushed onto them. "" is the
		// documented clear and is deliberately NOT a lookup — a credential that
		// has since been deleted must still be clearable.
		if wantCredential {
			raw := strings.TrimSpace(flagKeeperAuxCredential)
			if raw == "" {
				body["credential_id"] = ""
			} else {
				id, rerr := resolveCredentialID(client, raw)
				if rerr != nil {
					return rerr
				}
				body["credential_id"] = id
			}
		}
		var out keeperAuxConfig
		if err := putJSON(client, keeperAuxPath+"/"+slot, body, &out); err != nil {
			return keeperPermissionHint(err)
		}
		return newFormatter().AutoHuman(out, func() {
			cli.PrintSuccess(fmt.Sprintf("Evaluator slot %q updated.", slot))
			printKeeperAuxConfig(out)
		})
	},
}

var keeperAuxUseJudgeCmd = &cobra.Command{
	Use:   "use-judge",
	Short: "Point every evaluator at the local instance judge (no per-token cost)",
	Long: `Set every evaluator slot to the instance judge's provider and model.

This is the "stop paying per token for governance" action: the sweeps and the
behaviour watchdog run on the same local model that already decides credential
access. It writes explicit per-slot overrides rather than a mode flag, so 'aux
list' still shows what each slot resolves to and 'aux reset <slot>' still returns
one slot to the server's configuration.

Local models are smaller than the hosted defaults, so expect blunter findings —
this is a cost decision, not a free one. Requires a configured judge; run
'crewship keeper config get' first if you are not sure there is one.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		var out keeperAuxConfig
		if err := postJSON(client, keeperAuxPath+"/use-judge", map[string]any{}, &out); err != nil {
			return keeperPermissionHint(err)
		}
		return newFormatter().AutoHuman(out, func() {
			cli.PrintSuccess("Every evaluator now runs on the local judge — no per-token cost.")
			printKeeperAuxConfig(out)
		})
	},
}

var keeperAuxTestCmd = &cobra.Command{
	Use:   "test <slot>",
	Short: "Call one evaluator's model once and report what happened",
	Long: `Run a single real evaluation against the model a slot resolves to.

The Judge models card reports every evaluator as "not checked", because rendering
a status page must not call a paid API. That default is right and it leaves one
question unanswered: whether any of these judges actually works. You only find out
when a sweep runs and fails, which is the worst moment.

This is that check, on request, one slot at a time. It reports the same stages the
judge check does — whether a verdict came back, and whether it arrived inside the
budget the credential path allows — so a local and a hosted evaluator are held to
the same bar.

It costs one model call. Hosted slots bill for it, and it shares the instance-wide
probe rate limit with 'keeper judge test'.

Examples:
  crewship keeper aux test behavior
  crewship keeper aux test curator`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		slot := strings.TrimSpace(args[0])
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		var out keeperJudgeTestResult
		if err := postJSON(client, keeperAuxPath+"/"+slot+"/probe", map[string]any{}, &out); err != nil {
			return keeperPermissionHint(err)
		}
		if ferr := newFormatter().AutoHuman(out, func() {
			fmt.Printf("%sEvaluator check%s  %s", cli.Bold, cli.Reset, slot)
			if out.Model != "" {
				fmt.Printf("  ·  %s", out.Model)
			}
			fmt.Println()
			printJudgeStages(out.Stages)
			if out.OK {
				cli.PrintSuccess("This evaluator works.")
			}
		}); ferr != nil {
			return ferr
		}
		if !out.OK {
			// The exit code is the machine-readable half of the same answer.
			return cli.WithExitCode(fmt.Errorf("evaluator %q is not usable — see the failed stage above", slot), cli.ExitGeneric)
		}
		return nil
	},
}

var keeperAuxResetCmd = &cobra.Command{
	Use:   "reset [slot]",
	Short: "Drop the override for one slot, or for every slot with --all",
	Long: `Return a slot to the value the server booted with (CREWSHIP_AUX_* or the
shipped default). Pass --all to clear every slot at once.

If the slots were pointed at the local judge, resetting puts them back on the
hosted default — which starts billing per token again.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		slot := ""
		if len(args) == 1 {
			slot = strings.TrimSpace(args[0])
		}
		// Requiring one or the other keeps a bare `aux reset` from silently
		// clearing every slot when the operator meant one and mistyped.
		switch {
		case slot == "" && !flagKeeperAuxResetAll:
			return fmt.Errorf("name a slot to reset, or pass --all to clear every slot")
		case slot != "" && flagKeeperAuxResetAll:
			return fmt.Errorf("pass a slot or --all, not both")
		}

		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		path := keeperAuxPath
		if slot != "" {
			path += "/" + slot
		}
		if err := deleteJSON(client, path); err != nil {
			return keeperPermissionHint(err)
		}
		// The DELETE helper discards the body, so read back what is now in force
		// rather than printing an optimistic guess.
		cfg, err := getKeeperAuxConfig(client)
		if err != nil {
			return err
		}
		return newFormatter().AutoHuman(cfg, func() {
			if slot != "" {
				cli.PrintSuccess(fmt.Sprintf("Override cleared for %q — the server configuration is back in force.", slot))
			} else {
				cli.PrintSuccess("Every evaluator override cleared — the server configuration is back in force.")
			}
			printKeeperAuxConfig(cfg)
		})
	},
}

func init() {
	keeperAuxSetCmd.Flags().StringVar(&flagKeeperAuxProvider, "provider", "", `anthropic, openai, or ollama ("" to inherit)`)
	keeperAuxSetCmd.Flags().StringVar(&flagKeeperAuxModel, "model", "", `model id, e.g. claude-opus-5 or qwen2.5:7b ("" to inherit)`)
	keeperAuxSetCmd.Flags().StringVar(&flagKeeperAuxTimeout, "timeout", "", `per-call deadline, e.g. 30s ("" to inherit)`)
	keeperAuxSetCmd.Flags().StringVar(&flagKeeperAuxCredential, "credential", "", `vault API_KEY this slot spends, by name ("" for the server's own key)`)
	keeperAuxResetCmd.Flags().BoolVar(&flagKeeperAuxResetAll, "all", false, "clear the override on every slot")

	keeperAuxCmd.AddCommand(keeperAuxListCmd)
	keeperAuxCmd.AddCommand(keeperAuxTestCmd)
	keeperAuxCmd.AddCommand(keeperAuxSetCmd)
	keeperAuxCmd.AddCommand(keeperAuxUseJudgeCmd)
	keeperAuxCmd.AddCommand(keeperAuxResetCmd)
	keeperCmd.AddCommand(keeperAuxCmd)
}
