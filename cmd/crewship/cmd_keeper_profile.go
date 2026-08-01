package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/keepercfg"
)

// keeperProfileCmd drives the judge-profile half of GET/PUT
// /api/v1/admin/keeper/config — which capabilities the credential-access judge
// is allowed to use, as opposed to which endpoint and model it dials
// (`crewship keeper config`).
//
// Why it is a separate command rather than more flags on `config set`: these
// seven toggles are one decision — how much work the judge should do per
// decision — and it is taken against the model, not with it. An operator moving
// from a 9B local judge to a hosted one changes the model once and the profile
// once, and wants to see the second as its own list with its own provenance.
var keeperProfileCmd = &cobra.Command{
	Use:   "profile",
	Short: "Judge profile: which capabilities decide a credential request",
	Long: `Inspect and change the INSTANCE judge profile (requires OWNER or ADMIN).

Every capability the judge can use is a toggle, because each one makes the
prompt bigger or the decision more expensive — and a small model given more
context decides WORSE, not better. Which way that goes for YOUR model is a
measurement, not a rule, so nothing here is wired shut:

  evidence              a block of computed facts (is this credential bound to
                        this agent, how many times was it granted before, how
                        many denials in the last 7 days, ...) placed ABOVE the
                        conversation history, so facts outrank what the history
                        claims.
  evidence-facts        which of those facts to include. Empty means all.
  hard-gate             refuse an L3/L4 credential the agent has no binding to
                        WITHOUT calling the model at all — faster, cheaper, and
                        it removes a whole class of wrong ALLOW.
  precedent             attach the closest past decisions that a HUMAN resolved,
                        as worked examples.
  precedent-n           how many of them.
  consistency-samples   take this many verdicts on L3/L4 and take the majority.
                        1 means one verdict, i.e. sampling off.
  prompt-budget         cap the assembled prompt in tokens. 0 derives it from
                        the judge's context window. The watch policy, the tier
                        and the facts are never what gets cut.

Three presets set them together:

  lean       evidence + hard gate only. For ~3-9B models and short contexts.
  standard   lean + precedent. For 9-14B with a context window of 8k or more.
  thorough   everything, with 3-sample self-consistency. For hosted judges.

Each toggle is three-state. 'inherit' means "follow the profile" — NOT "off" —
so turning one capability off does not freeze the other six at today's values.

Changes apply to the next credential request; no restart. The profile in force
is recorded on each decision, so two decisions taken under different
capabilities can be told apart.

Examples:
  crewship keeper profile get
  crewship keeper profile set standard
  crewship keeper profile set --precedent off
  crewship keeper profile set --consistency-samples 3
  crewship keeper profile set --evidence-facts credential_bound_to_agent,agent_denies_last_7d
  crewship keeper profile set --precedent inherit    # follow the profile again
  crewship keeper profile reset`,
}

// keeperProfileField mirrors one {value, source, editable} entry of the
// judge_profile block in internal/api/admin_keeper_config.go.
type keeperProfileBoolField struct {
	Value  bool   `json:"value"`
	Source string `json:"source"`
}

type keeperProfileStrField struct {
	Value  string `json:"value"`
	Source string `json:"source"`
}

type keeperProfileIntField struct {
	Value  int64  `json:"value"`
	Source string `json:"source"`
}

type keeperProfileListField struct {
	Value  []string `json:"value"`
	Source string   `json:"source"`
}

type keeperJudgeProfile struct {
	Name               keeperProfileStrField  `json:"name"`
	Evidence           keeperProfileBoolField `json:"evidence"`
	EvidenceFacts      keeperProfileListField `json:"evidence_facts"`
	HardGate           keeperProfileBoolField `json:"hard_gate"`
	Precedent          keeperProfileBoolField `json:"precedent"`
	PrecedentN         keeperProfileIntField  `json:"precedent_n"`
	ConsistencySamples keeperProfileIntField  `json:"consistency_samples"`
	PromptBudgetTokens keeperProfileIntField  `json:"prompt_budget_tokens"`

	Overridden bool     `json:"overridden"`
	Choices    []string `json:"choices"`
	Facts      []string `json:"available_facts"`
	Stamp      string   `json:"stamp"`
}

// keeperProfileEnvelope is the config response narrowed to what this command
// reads. It shares the endpoint with `keeper config`, so it deliberately
// decodes only the profile block rather than duplicating that struct.
type keeperProfileEnvelope struct {
	Profile keeperJudgeProfile `json:"judge_profile"`
}

func getKeeperJudgeProfile(client *cli.Client) (keeperJudgeProfile, error) {
	var env keeperProfileEnvelope
	if err := getJSON(client, keeperConfigPath, &env); err != nil {
		return keeperJudgeProfile{}, keeperPermissionHint(err)
	}
	return env.Profile, nil
}

// printKeeperJudgeProfile renders each toggle with the layer that decided it,
// because "the standard profile turned precedent on" and "somebody turned
// precedent on here" are different facts and only the second one is undone by
// clearing the override.
func printKeeperJudgeProfile(p keeperJudgeProfile) {
	fmt.Printf("%sKeeper judge profile (instance)%s\n", cli.Bold, cli.Reset)
	fmt.Printf("  Profile:      %s %s\n", orUnset(p.Name.Value), profileSourceNote(p.Name.Source))
	fmt.Printf("  Evidence:     %s %s\n", formatToggle(p.Evidence.Value), profileSourceNote(p.Evidence.Source))
	fmt.Printf("  Facts:        %s %s\n", formatEvidenceFacts(p.EvidenceFacts.Value, p.Facts),
		profileSourceNote(p.EvidenceFacts.Source))
	fmt.Printf("  Hard gate:    %s %s\n", formatToggle(p.HardGate.Value), profileSourceNote(p.HardGate.Source))
	fmt.Printf("  Precedent:    %s (%d examples) %s\n", formatToggle(p.Precedent.Value), p.PrecedentN.Value,
		profileSourceNote(p.Precedent.Source))
	fmt.Printf("  Samples:      %s %s\n", formatConsistencySamples(p.ConsistencySamples.Value),
		profileSourceNote(p.ConsistencySamples.Source))
	fmt.Printf("  Prompt cap:   %s %s\n", formatPromptBudget(p.PromptBudgetTokens.Value),
		profileSourceNote(p.PromptBudgetTokens.Source))
	if p.Stamp != "" {
		// The exact string recorded on each decision, so an operator reading
		// `keeper requests` can match a row to a configuration rather than guess.
		fmt.Printf("  %sRecorded on each decision as: %s%s\n", cli.Dim, p.Stamp, cli.Reset)
	}
}

// profileSourceNote extends sourceNote with the layer only the profile has: a
// preset that decided the value. Without it every preset-decided toggle would
// read "built-in default", which is the one thing an operator must not believe
// after they have selected a profile.
func profileSourceNote(source string) string {
	if source == "profile" {
		return cli.Dim + "[from the profile]" + cli.Reset
	}
	return sourceNote(source)
}

func formatToggle(on bool) string {
	if on {
		return cli.Green + "on" + cli.Reset
	}
	return cli.Red + "off" + cli.Reset
}

func formatEvidenceFacts(facts, all []string) string {
	switch {
	case len(facts) == 0:
		return cli.Yellow + "none" + cli.Reset
	case len(all) > 0 && len(facts) == len(all):
		return fmt.Sprintf("all %d", len(all))
	default:
		return strings.Join(facts, ", ")
	}
}

func formatConsistencySamples(n int64) string {
	if n <= 1 {
		return "1 " + cli.Dim + "(self-consistency off)" + cli.Reset
	}
	return fmt.Sprintf("%d %s(majority vote on L3/L4)%s", n, cli.Dim, cli.Reset)
}

func formatPromptBudget(tokens int64) string {
	if tokens <= 0 {
		return "derived from the judge's context window"
	}
	return fmt.Sprintf("%d tokens", tokens)
}

var keeperProfileGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Show which judge capabilities are in force, and what decided each",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		p, err := getKeeperJudgeProfile(client)
		if err != nil {
			return err
		}
		return newFormatter().AutoHuman(p, func() { printKeeperJudgeProfile(p) })
	},
}

var (
	flagKeeperProfileEvidence     string
	flagKeeperProfileFacts        string
	flagKeeperProfileHardGate     string
	flagKeeperProfilePrecedent    string
	flagKeeperProfilePrecedentN   string
	flagKeeperProfileSamples      string
	flagKeeperProfilePromptBudget string
)

var keeperProfileSetCmd = &cobra.Command{
	Use:   "set [lean|standard|thorough]",
	Short: "Select a preset and/or override individual capabilities",
	Long: `Set the judge profile, one or more toggles, or both in one call.

The positional argument selects a preset. The flags override individual
capabilities on top of it, and each one takes on, off or inherit — 'inherit'
hands the toggle back to the profile, which is not the same as 'off'.

  --evidence on|off|inherit
  --evidence-facts <a,b,c>        "" for every fact the judge can compute
  --hard-gate on|off|inherit
  --precedent on|off|inherit
  --precedent-n <1-10>            "" to follow the profile
  --consistency-samples <odd 1-9> 1 = one verdict (sampling off), "" to follow
                                  the profile
  --prompt-budget <tokens>        "" or 0 to derive it from the context window

Only what you pass is changed, so two operators editing different toggles do
not clobber each other.

Examples:
  crewship keeper profile set thorough
  crewship keeper profile set lean --precedent on
  crewship keeper profile set --evidence-facts credential_bound_to_agent
  crewship keeper profile set --consistency-samples ""`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		body := map[string]any{}
		if len(args) == 1 {
			name := strings.ToLower(strings.TrimSpace(args[0]))
			if !keepercfg.KnownProfile(keepercfg.ProfileName(name)) {
				return fmt.Errorf("unknown profile %q: use lean, standard, or thorough", name)
			}
			body["judge_profile"] = name
		}
		for _, t := range []struct {
			flag, field, raw string
		}{
			{"evidence", "judge_evidence", flagKeeperProfileEvidence},
			{"hard-gate", "judge_hard_gate", flagKeeperProfileHardGate},
			{"precedent", "judge_precedent", flagKeeperProfilePrecedent},
		} {
			if !cmd.Flags().Changed(t.flag) {
				continue
			}
			tri, ok := keepercfg.ParseTriBool(t.raw)
			if !ok {
				return fmt.Errorf("invalid --%s %q: use on, off, or inherit", t.flag, t.raw)
			}
			switch tri {
			case keepercfg.TriOn:
				body[t.field] = true
			case keepercfg.TriOff:
				body[t.field] = false
			case keepercfg.TriInherit:
				// Explicit JSON null is how the API expresses "follow the profile".
				body[t.field] = nil
			}
		}
		// Sent when the flag was PASSED, not when it is non-empty: "" is the
		// documented way to go back to every fact, so absent and "" cannot be the
		// same request.
		if cmd.Flags().Changed("evidence-facts") {
			body["judge_evidence_facts"] = strings.TrimSpace(flagKeeperProfileFacts)
		}
		for _, n := range []struct {
			flag, field, raw string
		}{
			{"precedent-n", "judge_precedent_n", flagKeeperProfilePrecedentN},
			{"consistency-samples", "judge_consistency_samples", flagKeeperProfileSamples},
			{"prompt-budget", "judge_prompt_budget_tokens", flagKeeperProfilePromptBudget},
		} {
			if !cmd.Flags().Changed(n.flag) {
				continue
			}
			raw := strings.TrimSpace(n.raw)
			if raw == "" {
				body[n.field] = 0 // clear → follow the profile
				continue
			}
			v, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || v < 0 {
				return fmt.Errorf(`invalid --%s %q: use a whole number, or "" to follow the profile`, n.flag, n.raw)
			}
			body[n.field] = v
		}
		if len(body) == 0 {
			return fmt.Errorf("nothing to change — name a profile, or pass a toggle (see 'crewship keeper profile set --help')")
		}

		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		var out keeperProfileEnvelope
		if err := putJSON(client, keeperConfigPath, body, &out); err != nil {
			return keeperPermissionHint(err)
		}
		return newFormatter().AutoHuman(out.Profile, func() {
			cli.PrintSuccess("Judge profile updated — it applies to the next credential request.")
			printKeeperJudgeProfile(out.Profile)
		})
	},
}

var keeperProfileResetCmd = &cobra.Command{
	Use:   "reset",
	Short: "Drop the profile and every capability override, back to the built-in",
	Long: `Return every judge capability to the built-in profile.

This clears the profile selection and all seven toggles. It does NOT touch the
judge endpoint, model or timeout — use 'crewship keeper config reset' for those.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		// Field-by-field rather than DELETE: the delete endpoint drops the whole
		// instance judge configuration, endpoint and model included, and an
		// operator resetting the PROFILE must not silently un-configure the judge.
		var out keeperProfileEnvelope
		body := map[string]any{
			"judge_profile":              "",
			"judge_evidence":             nil,
			"judge_evidence_facts":       "",
			"judge_hard_gate":            nil,
			"judge_precedent":            nil,
			"judge_precedent_n":          0,
			"judge_consistency_samples":  0,
			"judge_prompt_budget_tokens": 0,
		}
		if err := putJSON(client, keeperConfigPath, body, &out); err != nil {
			return keeperPermissionHint(err)
		}
		return newFormatter().AutoHuman(out.Profile, func() {
			cli.PrintSuccess("Judge profile reset to the built-in.")
			printKeeperJudgeProfile(out.Profile)
		})
	},
}

func init() {
	f := keeperProfileSetCmd.Flags()
	f.StringVar(&flagKeeperProfileEvidence, "evidence", "", "computed-facts block: on, off, or inherit")
	f.StringVar(&flagKeeperProfileFacts, "evidence-facts", "",
		`which facts the block carries, comma-separated ("" for all of them)`)
	f.StringVar(&flagKeeperProfileHardGate, "hard-gate", "",
		"refuse an unbound L3/L4 credential without calling the model: on, off, or inherit")
	f.StringVar(&flagKeeperProfilePrecedent, "precedent", "",
		"few-shot examples from past human-resolved decisions: on, off, or inherit")
	f.StringVar(&flagKeeperProfilePrecedentN, "precedent-n", "",
		`how many precedent examples, 1-10 ("" to follow the profile)`)
	f.StringVar(&flagKeeperProfileSamples, "consistency-samples", "",
		`verdicts to take on L3/L4 before a majority vote, odd 1-9; 1 = off ("" to follow the profile)`)
	f.StringVar(&flagKeeperProfilePromptBudget, "prompt-budget", "",
		`cap the assembled prompt in tokens ("" or 0 to derive it from the context window)`)

	keeperProfileCmd.AddCommand(keeperProfileGetCmd)
	keeperProfileCmd.AddCommand(keeperProfileSetCmd)
	keeperProfileCmd.AddCommand(keeperProfileResetCmd)
	keeperCmd.AddCommand(keeperProfileCmd)
}
