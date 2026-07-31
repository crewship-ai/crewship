package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/keepercfg"
)

// keeperConfigCmd drives GET/PUT/DELETE /api/v1/admin/keeper/config — the
// INSTANCE judge configuration, as opposed to the per-workspace governance the
// rest of `crewship keeper` manages.
//
// Why it exists: `keeper.enabled`, the judge endpoint and the judge model used to
// be readable only from the status card and changeable only by editing KEEPER_*
// and restarting the server. An operator running Ollama on their own box — the
// case the product is pitched on — could see that the judge was not running and
// had no way to fix it without shell access. A change made here takes effect on
// the next credential request.
var keeperConfigCmd = &cobra.Command{
	Use:   "config",
	Short: "Instance judge configuration: whether Keeper runs, and what decides",
	Long: `Inspect and change the INSTANCE-level Keeper judge (requires OWNER or ADMIN).

This is the layer under per-workspace governance: whether the Keeper engine runs
at all, and which endpoint and model the credential-access judge asks. Each field
either has an instance override or inherits the KEEPER_* value the server booted
with, and 'config get' shows which — "env" means inherited, "instance" means set
here, "default" means nothing is configured anywhere.

Changes apply to the next credential request; no restart. Turning Keeper on or
off also changes how SECRET credentials reach agents (withheld and requested
versus injected directly), which takes effect for runs started afterwards — a
container already running keeps the environment it was handed.

Keeper is fail-closed, so it cannot be enabled without a judge: a request to
enable it with no endpoint or model is refused rather than accepted into an
instance that would DENY every credential request.

SCOPE. This judge is instance-wide and speaks the NATIVE OLLAMA API only — the
provider and wire are reported by 'config get', but native Ollama is the only
value either accepts. A hosted judge (Anthropic, or any OpenAI-compatible
endpoint) sources its endpoint or API key from a vault credential, and the vault
is per workspace, so it is configured with 'crewship keeper model set' instead.
That setting overrides this one for its workspace, and falls back to this judge
if its credential is revoked.

Examples:
  crewship keeper config get
  crewship keeper config set --endpoint http://192.168.1.40:11434 --model qwen2.5:7b
  crewship keeper config set --enabled on
  crewship keeper config set --model ""          # clear the override, inherit KEEPER_MODEL
  crewship keeper config set --enabled inherit   # stop overriding KEEPER_ENABLED
  crewship keeper config reset`,
}

// keeperConfigBoolField / keeperConfigStrField mirror the per-field
// {value, source, editable} shape of internal/api/admin_keeper_config.go.
type keeperConfigBoolField struct {
	Value    bool   `json:"value"`
	Source   string `json:"source"`
	Editable bool   `json:"editable"`
}

type keeperConfigStrField struct {
	Value    string `json:"value"`
	Source   string `json:"source"`
	Editable bool   `json:"editable"`
}

type keeperConfigIntField struct {
	Value    int64  `json:"value"`
	Source   string `json:"source"`
	Editable bool   `json:"editable"`
}

type keeperInstanceConfig struct {
	Enabled     keeperConfigBoolField `json:"enabled"`
	Provider    keeperConfigStrField  `json:"judge_provider"`
	EndpointURL keeperConfigStrField  `json:"judge_endpoint_url"`
	Wire        keeperConfigStrField  `json:"judge_wire"`
	Model       keeperConfigStrField  `json:"judge_model"`
	TimeoutMS   keeperConfigIntField  `json:"judge_timeout_ms"`

	Overridden      bool   `json:"overridden"`
	UpdatedAt       string `json:"updated_at"`
	UpdatedBy       string `json:"updated_by"`
	JudgeConfigured bool   `json:"judge_configured"`
}

const keeperConfigPath = "/api/v1/admin/keeper/config"

func getKeeperInstanceConfig(client *cli.Client) (keeperInstanceConfig, error) {
	var cfg keeperInstanceConfig
	if err := getJSON(client, keeperConfigPath, &cfg); err != nil {
		return keeperInstanceConfig{}, keeperPermissionHint(err)
	}
	return cfg, nil
}

// printKeeperInstanceConfig renders the effective config with provenance, so
// "the judge is not running" and "somebody turned the judge off here" are
// visibly different states.
func printKeeperInstanceConfig(cfg keeperInstanceConfig) {
	fmt.Printf("%sKeeper judge (instance)%s\n", cli.Bold, cli.Reset)
	fmt.Printf("  Engine:     %s %s\n", formatKeeperEnabled(cfg.Enabled.Value), sourceNote(cfg.Enabled.Source))
	fmt.Printf("  Endpoint:   %s %s\n", orUnset(cfg.EndpointURL.Value), sourceNote(cfg.EndpointURL.Source))
	fmt.Printf("  Model:      %s %s\n", orUnset(cfg.Model.Value), sourceNote(cfg.Model.Source))
	fmt.Printf("  Provider:   %s (%s wire) %s\n", orUnset(cfg.Provider.Value), orUnset(cfg.Wire.Value), sourceNote(cfg.Provider.Source))
	// The provider and wire are reported but not settable, and "reported" reads
	// as "settable" on a line that sits between two fields that are. Saying
	// which scope this row is and naming the command for the other one is the
	// difference between the CLI teaching the split and a 400 teaching it
	// (#1558).
	fmt.Printf("              %sinstance-wide, native Ollama only — for an Anthropic or OpenAI-compatible judge use 'crewship keeper model set' (per workspace)%s\n",
		cli.Dim, cli.Reset)
	// Printed next to the model because it is a property OF the model choice: a
	// bigger judge needs a bigger budget, and a judge slower than the budget
	// denies every credential request while still reporting as reachable.
	fmt.Printf("  Budget:     %s per decision %s\n",
		formatAuxTimeout(cfg.TimeoutMS.Value), sourceNote(cfg.TimeoutMS.Source))
	if cfg.Overridden && cfg.UpdatedAt != "" {
		by := cfg.UpdatedBy
		if by == "" {
			by = "unknown"
		}
		fmt.Printf("  Changed:    %s by %s\n", cfg.UpdatedAt, by)
	}
	if cfg.Enabled.Value && !cfg.JudgeConfigured {
		fmt.Printf("%sKeeper is enabled but has no judge endpoint/model — every credential request will be denied.%s\n",
			cli.Red, cli.Reset)
	}
}

func formatKeeperEnabled(on bool) string {
	if on {
		return cli.Green + "enabled" + cli.Reset
	}
	return cli.Red + "disabled" + cli.Reset
}

func orUnset(v string) string {
	if v == "" {
		return cli.Yellow + "(not set)" + cli.Reset
	}
	return v
}

// sourceNote turns provenance into the sentence an operator needs: whether
// clearing the override would change anything.
func sourceNote(source string) string {
	switch source {
	case "instance":
		return cli.Dim + "[instance override]" + cli.Reset
	case "env":
		return cli.Dim + "[inherited from server config]" + cli.Reset
	case "default":
		return cli.Dim + "[built-in default]" + cli.Reset
	default:
		return ""
	}
}

var keeperConfigGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Show the effective instance judge configuration and where each value comes from",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		cfg, err := getKeeperInstanceConfig(client)
		if err != nil {
			return err
		}
		return newFormatter().AutoHuman(cfg, func() { printKeeperInstanceConfig(cfg) })
	},
}

var (
	flagKeeperCfgEnabled  string
	flagKeeperCfgEndpoint string
	flagKeeperCfgModel    string
	flagKeeperCfgTimeout  string
)

var keeperConfigSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Change the instance judge (requires OWNER or ADMIN)",
	Long: `Set one or more instance judge fields. Only the flags you pass are changed —
this is a partial update, so two operators editing different fields do not
clobber each other.

  --enabled on|off|inherit   whether the Keeper engine runs. 'inherit' drops the
                             override and goes back to KEEPER_ENABLED.
  --endpoint <url>           the judge's endpoint, e.g. http://192.168.1.40:11434.
                             Pass "" to clear the override and inherit
                             KEEPER_OLLAMA_URL. This is JUDGE-SCOPED: it does not
                             move the episodic embedder or the chat summarizer.
  --model <name>             the judge model, e.g. qwen2.5:7b. Pass "" to inherit
                             KEEPER_MODEL.
  --judge-timeout <dur>      how long one credential decision may take, e.g. 40s.
                             A judge slower than this DENIES every request (it is
                             fail-closed), so raise it if you move to a bigger
                             model. Pass "" for the built-in default.

Examples:
  crewship keeper config set --endpoint http://192.168.1.40:11434 --model qwen2.5:7b --enabled on
  crewship keeper config set --model qwen3:4b
  crewship keeper config set --enabled off`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		body := map[string]any{}
		if cmd.Flags().Changed("enabled") {
			tri, ok := keepercfg.ParseTriBool(flagKeeperCfgEnabled)
			if !ok {
				return fmt.Errorf("invalid --enabled %q: use on, off, or inherit", flagKeeperCfgEnabled)
			}
			switch tri {
			case keepercfg.TriOn:
				body["enabled"] = true
			case keepercfg.TriOff:
				body["enabled"] = false
			case keepercfg.TriInherit:
				// Explicit JSON null is how the API expresses "stop overriding".
				body["enabled"] = nil
			}
		}
		// Sent when the flag was PASSED, not when it is non-empty: an empty value
		// is the documented way to clear an override, so absent and "" cannot be
		// the same request.
		if cmd.Flags().Changed("endpoint") {
			body["judge_endpoint_url"] = strings.TrimSpace(flagKeeperCfgEndpoint)
		}
		if cmd.Flags().Changed("model") {
			body["judge_model"] = strings.TrimSpace(flagKeeperCfgModel)
		}
		if cmd.Flags().Changed("judge-timeout") {
			raw := strings.TrimSpace(flagKeeperCfgTimeout)
			if raw == "" {
				body["judge_timeout_ms"] = 0 // clear → the built-in default
			} else {
				d, err := time.ParseDuration(raw)
				if err != nil {
					return fmt.Errorf("invalid --judge-timeout %q: use a duration like 40s or 90s", raw)
				}
				if d <= 0 {
					return fmt.Errorf(`invalid --judge-timeout %q: must be positive (pass "" for the default)`, raw)
				}
				body["judge_timeout_ms"] = d.Milliseconds()
			}
		}
		if len(body) == 0 {
			return fmt.Errorf("nothing to change — pass --enabled, --endpoint, --model and/or --judge-timeout (see 'crewship keeper config set --help')")
		}

		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		var out keeperInstanceConfig
		if err := putJSON(client, keeperConfigPath, body, &out); err != nil {
			return keeperPermissionHint(err)
		}
		return newFormatter().AutoHuman(out, func() {
			cli.PrintSuccess("Keeper judge configuration updated — it applies to the next credential request.")
			printKeeperInstanceConfig(out)
		})
	},
}

var keeperConfigResetCmd = &cobra.Command{
	Use:   "reset",
	Short: "Drop every instance override and go back to the server's KEEPER_* config",
	Long: `Remove all instance overrides. Every field returns to the value the server
booted with (KEEPER_ENABLED / KEEPER_OLLAMA_URL / KEEPER_MODEL), which may mean
Keeper stops running — 'config get' first if you are not sure what that is.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		if err := deleteJSON(client, keeperConfigPath); err != nil {
			return keeperPermissionHint(err)
		}
		// The DELETE helper discards the body, so read back what is now in force
		// rather than printing an optimistic guess.
		cfg, err := getKeeperInstanceConfig(client)
		if err != nil {
			return err
		}
		return newFormatter().AutoHuman(cfg, func() {
			cli.PrintSuccess("Instance judge overrides cleared — the server configuration is back in force.")
			printKeeperInstanceConfig(cfg)
		})
	},
}

func init() {
	keeperConfigSetCmd.Flags().StringVar(&flagKeeperCfgEnabled, "enabled", "", "whether the Keeper engine runs: on, off, or inherit")
	keeperConfigSetCmd.Flags().StringVar(&flagKeeperCfgEndpoint, "endpoint", "", `judge endpoint URL, e.g. http://192.168.1.40:11434 ("" to inherit)`)
	keeperConfigSetCmd.Flags().StringVar(&flagKeeperCfgModel, "model", "", `judge model, e.g. qwen2.5:7b ("" to inherit)`)
	keeperConfigSetCmd.Flags().StringVar(&flagKeeperCfgTimeout, "judge-timeout", "",
		`how long one credential decision may take, e.g. 40s ("" for the default)`)

	keeperConfigCmd.AddCommand(keeperConfigGetCmd)
	keeperConfigCmd.AddCommand(keeperConfigSetCmd)
	keeperConfigCmd.AddCommand(keeperConfigResetCmd)
	keeperCmd.AddCommand(keeperConfigCmd)
}
