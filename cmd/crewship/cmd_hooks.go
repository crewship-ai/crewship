package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/hooks"
	"github.com/spf13/cobra"
)

// hooksCmd is the CLI surface for the lifecycle-hook system: registered
// callbacks that fire on platform lifecycle events. Live against:
//
//	GET    /api/v1/hooks[?crew_id=…]
//	POST   /api/v1/hooks
//	PATCH  /api/v1/hooks/{id}
//	DELETE /api/v1/hooks/{id}
//	POST   /api/v1/hooks/{id}/enable
//	POST   /api/v1/hooks/{id}/disable
//
// Registration used to be Go-only, which meant the whole hooks engine was
// unreachable from a running deployment. `create` / `update` / `delete`
// are the CLI half of the write API that fixed that.
var hooksCmd = &cobra.Command{
	Use:     "hooks",
	Aliases: []string{"hook"},
	Short:   "Lifecycle hooks registry (list/create/update/delete/enable/disable)",
	Long: `Manage the lifecycle-hook registry — shell commands, HTTP webhooks, or
subagents that fire on platform lifecycle events (post_agent_stop,
on_approval_requested, on_guardrail_triggered, …).

pre_tool_call is not a valid --event: there is no interception point before
a tool executes, so nothing could ever fire it. The platform fires
post_tool_call instead, after the tool has run. Every other event this
command accepts has a live dispatch site — see the Hooks guide's coverage
table for the call site behind each one.

--blocking is only accepted on the pre_* events whose call site can still
cancel the operation (pre_task_delegation, pre_agent_start, pre_llm_call,
pre_memory_write, pre_peer_conversation). post_*/on_* events are
observations: they run asynchronously and a Block outcome is recorded in
the journal rather than enforced.

Examples:
  crewship hooks list
  crewship hooks list --crew backend-team
  crewship hooks create --event on_budget_exceeded --handler http \
      --url https://hooks.slack.test/services/XXX
  crewship hooks create --event pre_agent_start --handler shell \
      --command /usr/local/bin/gate.sh --crew backend-team --blocking
  crewship hooks update hk_abc --disabled
  crewship hooks delete hk_abc --yes
  crewship hooks enable hk_abc
  crewship hooks disable hk_abc

Writes require OWNER or ADMIN. Creating or editing a shell hook requires
OWNER — shell handlers execute commands on the crewshipd host.`,
}

// hookEventNames / hookHandlerKinds are rendered from the engine's own
// declarations rather than re-typed here, so a new event shows up in CLI
// help and validation the moment it is added to internal/hooks.
func hookEventNames() []string { return hooks.EventNames() }

var hookHandlerKinds = []string{
	string(hooks.HandlerKindShell),
	string(hooks.HandlerKindHTTP),
	string(hooks.HandlerKindSubagent),
}

// validateHookEvent rejects an unknown event locally. The server rejects it
// too, but a client-side check keeps the fix ("you meant post_tool_call")
// in the same message as the mistake instead of behind an HTTP 400 — the
// same reasoning as validateCSV on `crewship journal`.
func validateHookEvent(event string) error {
	for _, e := range hookEventNames() {
		if e == event {
			return nil
		}
	}
	return fmt.Errorf("invalid --event %q (valid: %s)", event, strings.Join(hookEventNames(), ", "))
}

func validateHookHandler(kind string) error {
	for _, k := range hookHandlerKinds {
		if k == kind {
			return nil
		}
	}
	return fmt.Errorf("invalid --handler %q (valid: %s)", kind, strings.Join(hookHandlerKinds, ", "))
}

// hookTarget renders the one field of handler_config an operator most wants
// to see in a list: the URL, the command, or the subagent. The list view
// used to decode a `target` field the API has never emitted, so the TARGET
// column was permanently blank; deriving it here from handler_config —
// which the API does send — is what makes the column mean something.
func hookTarget(kind string, cfg map[string]any) string {
	str := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := cfg[k].(string); ok && v != "" {
				return v
			}
		}
		return ""
	}
	switch kind {
	case string(hooks.HandlerKindHTTP):
		return str("url")
	case string(hooks.HandlerKindShell):
		return str("command")
	case string(hooks.HandlerKindSubagent):
		return str("agent_id", "agent", "subagent")
	default:
		return ""
	}
}

// hookWriteFlags registers the flag set shared by create and update. They
// take identical inputs; only the "which of these did you actually set"
// question differs, and that is answered per-command via Flags().Changed.
func hookWriteFlags(cmd *cobra.Command) {
	cmd.Flags().String("event", "", "Lifecycle event ("+strings.Join(hookEventNames(), " | ")+")")
	cmd.Flags().String("handler", "", "Handler kind ("+strings.Join(hookHandlerKinds, " | ")+")")
	cmd.Flags().String("url", "", "http handler: destination URL")
	cmd.Flags().String("command", "", "shell handler: command to execute on the host (OWNER only)")
	cmd.Flags().String("subagent", "", "subagent handler: agent slug or ID to dispatch")
	cmd.Flags().String("handler-config", "", "Raw handler_config JSON — escape hatch for keys the flags above don't cover")
	cmd.Flags().String("crew", "", "Scope the hook to one crew (slug or ID); omit for workspace-wide")
	cmd.Flags().String("matcher", "", "Raw matcher JSON — escape hatch for the full predicate")
	cmd.Flags().String("matcher-tools", "", "Comma-separated tool-name regexes the hook fires on")
	cmd.Flags().String("matcher-agents", "", "Comma-separated agent IDs the hook fires for")
	cmd.Flags().String("matcher-crews", "", "Comma-separated crew IDs the hook fires for")
	cmd.Flags().String("matcher-severities", "", "Comma-separated severities the hook fires on")
	cmd.Flags().Bool("blocking", false, "Run synchronously and let a Block outcome cancel the triggering operation")
	cmd.Flags().Bool("disabled", false, "Register/leave the hook disabled")
}

// buildHookBody turns the flag set into the request body. onlyChanged=true
// (update) emits a key ONLY when its flag was explicitly set, so a PATCH
// never carries a default value that would clear a field server-side;
// onlyChanged=false (create) always emits event + handler_kind +
// handler_config and validates that the handler has a target.
func buildHookBody(cmd *cobra.Command, client *cli.Client, onlyChanged bool) (map[string]any, error) {
	changed := func(name string) bool { return cmd.Flags().Changed(name) }
	str := func(name string) string {
		v, _ := cmd.Flags().GetString(name)
		return v
	}

	body := map[string]any{}

	event := str("event")
	if !onlyChanged || changed("event") {
		if event == "" {
			return nil, fmt.Errorf("--event is required (valid: %s)", strings.Join(hookEventNames(), ", "))
		}
		if err := validateHookEvent(event); err != nil {
			return nil, err
		}
		body["event"] = event
	}

	kind := str("handler")
	if !onlyChanged || changed("handler") {
		if kind == "" {
			return nil, fmt.Errorf("--handler is required (valid: %s)", strings.Join(hookHandlerKinds, ", "))
		}
		if err := validateHookHandler(kind); err != nil {
			return nil, err
		}
		body["handler_kind"] = kind
	}

	// handler_config: the raw JSON flag wins outright when given, otherwise
	// it is synthesised from the per-kind convenience flag.
	rawCfg := str("handler-config")
	switch {
	case changed("handler-config") && rawCfg != "":
		cfg := map[string]any{}
		if err := json.Unmarshal([]byte(rawCfg), &cfg); err != nil {
			return nil, fmt.Errorf("--handler-config is not valid JSON: %w", err)
		}
		body["handler_config"] = cfg
	case changed("url"):
		body["handler_config"] = map[string]any{"url": str("url")}
	case changed("command"):
		body["handler_config"] = map[string]any{"command": str("command")}
	case changed("subagent"):
		body["handler_config"] = map[string]any{"agent_id": str("subagent")}
	case !onlyChanged:
		// Create with a handler kind but nothing to point it at. The
		// server would reject this too; naming the missing flag is more
		// useful than relaying "requires handler_config.url".
		switch kind {
		case string(hooks.HandlerKindHTTP):
			return nil, fmt.Errorf("--handler http needs --url (or --handler-config)")
		case string(hooks.HandlerKindShell):
			return nil, fmt.Errorf("--handler shell needs --command (or --handler-config)")
		case string(hooks.HandlerKindSubagent):
			return nil, fmt.Errorf("--handler subagent needs --subagent (or --handler-config)")
		}
	}

	// matcher: same precedence — raw JSON, else assembled from the
	// per-field convenience flags. Assembling only when at least one is
	// set keeps an untouched matcher out of a PATCH body.
	rawMatcher := str("matcher")
	if changed("matcher") && rawMatcher != "" {
		m := map[string]any{}
		if err := json.Unmarshal([]byte(rawMatcher), &m); err != nil {
			return nil, fmt.Errorf("--matcher is not valid JSON: %w", err)
		}
		body["matcher"] = m
	} else if changed("matcher-tools") || changed("matcher-agents") ||
		changed("matcher-crews") || changed("matcher-severities") {
		m := map[string]any{}
		if v := splitCSV(str("matcher-tools")); len(v) > 0 {
			m["tools"] = v
		}
		if v := splitCSV(str("matcher-agents")); len(v) > 0 {
			m["agent_ids"] = v
		}
		if v := splitCSV(str("matcher-crews")); len(v) > 0 {
			m["crew_ids"] = v
		}
		if v := splitCSV(str("matcher-severities")); len(v) > 0 {
			m["severities"] = v
		}
		body["matcher"] = m
	}

	if changed("crew") {
		crewID, err := resolveCrewID(client, str("crew"))
		if err != nil {
			return nil, err
		}
		body["crew_id"] = crewID
	}
	if changed("blocking") {
		v, _ := cmd.Flags().GetBool("blocking")
		if v && event != "" && !hooks.Event(event).SupportsBlocking() {
			return nil, fmt.Errorf("--blocking is not valid for observation event %q; use a pre_* event for a synchronous gate", event)
		}
		body["blocking"] = v
	}
	if changed("disabled") {
		v, _ := cmd.Flags().GetBool("disabled")
		body["enabled"] = !v
	}
	return body, nil
}

var hooksCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Register a new lifecycle hook",
	Long: `Register a hook that fires on a platform lifecycle event.

Requires OWNER or ADMIN. --handler shell additionally requires OWNER: a
shell hook runs its command on the crewshipd host, which is a strictly
larger grant than the rest of the admin surface.

New hooks are enabled unless you pass --disabled.

Examples:
  crewship hooks create --event on_approval_requested --handler http \
      --url https://hooks.slack.test/services/XXX
  crewship hooks create --event pre_agent_start --handler shell \
      --command /usr/local/bin/gate.sh --crew backend-team --blocking
  crewship hooks create --event post_agent_stop --handler subagent \
      --subagent oncall-router --crew backend-team
  crewship hooks create --event on_guardrail_triggered --handler http \
      --handler-config '{"url":"https://x.test/h","method":"PUT"}'`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()

		body, err := buildHookBody(cmd, client, false)
		if err != nil {
			return err
		}
		resp, err := client.Post("/api/v1/hooks", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out hookDetail
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		f := newFormatter()
		return f.AutoHuman(out, func() {
			printHookDetail("Created hook", out)
		})
	},
}

var hooksUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update a registered lifecycle hook",
	Long: `Patch an existing hook. Only the flags you pass are sent — everything
else keeps its current value on the server.

Requires OWNER or ADMIN. Editing a shell hook, or converting a hook INTO a
shell hook, requires OWNER.

Examples:
  crewship hooks update hk_abc --url https://new.test/hook
  crewship hooks update hk_abc --matcher-tools 'Bash,Write'
  crewship hooks update hk_abc --blocking
  crewship hooks update hk_abc --disabled`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()

		body, err := buildHookBody(cmd, client, true)
		if err != nil {
			return err
		}
		if len(body) == 0 {
			return fmt.Errorf("nothing to update — pass at least one of --event, --handler, --url, --command, --subagent, --handler-config, --crew, --matcher*, --blocking, --disabled")
		}
		path := fmt.Sprintf("/api/v1/hooks/%s", url.PathEscape(args[0]))
		resp, err := client.Patch(path, body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out hookDetail
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		f := newFormatter()
		return f.AutoHuman(out, func() {
			printHookDetail("Updated hook", out)
		})
	},
}

var hooksDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a registered lifecycle hook",
	Long: `Remove a hook from the registry. Requires OWNER or ADMIN.

Deletion is permanent and there is no undo — if you only want to stop the
hook firing, 'crewship hooks disable <id>' keeps the definition.

Examples:
  crewship hooks delete hk_abc --yes`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		yes, _ := cmd.Flags().GetBool("yes")
		if !yes {
			return fmt.Errorf("refusing to delete hook %s without --yes (use 'crewship hooks disable %s' to stop it firing without losing the definition)",
				args[0], args[0])
		}
		client := newAPIClient()
		path := fmt.Sprintf("/api/v1/hooks/%s", url.PathEscape(args[0]))
		resp, err := client.Delete(path)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		fmt.Printf("Hook %s: deleted\n", args[0])
		return nil
	},
}

// hookDetail is the single-hook projection create/update return — the same
// shape GET /api/v1/hooks emits per row.
type hookDetail struct {
	ID            string         `json:"id"`
	CrewID        string         `json:"crew_id"`
	Event         string         `json:"event"`
	HandlerKind   string         `json:"handler_kind"`
	HandlerConfig map[string]any `json:"handler_config"`
	Enabled       bool           `json:"enabled"`
	Blocking      bool           `json:"blocking"`
	CreatedAt     string         `json:"created_at"`
}

func printHookDetail(verb string, hk hookDetail) {
	fmt.Printf("%s %s\n", verb, hk.ID)
	fmt.Printf("  event    %s\n", hk.Event)
	fmt.Printf("  handler  %s", hk.HandlerKind)
	if target := hookTarget(hk.HandlerKind, hk.HandlerConfig); target != "" {
		fmt.Printf(" → %s", target)
	}
	fmt.Println()
	if hk.CrewID != "" {
		fmt.Printf("  crew     %s\n", hk.CrewID)
	}
	state := "disabled"
	if hk.Enabled {
		state = "enabled"
	}
	mode := "non-blocking"
	if hk.Blocking {
		mode = "blocking"
	}
	fmt.Printf("  state    %s, %s\n", state, mode)
}

var hooksListCmd = &cobra.Command{
	Use:   "list",
	Short: "List registered lifecycle hooks",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()

		crew, _ := cmd.Flags().GetString("crew")
		q := url.Values{}
		if crew != "" {
			crewID, err := resolveCrewID(client, crew)
			if err != nil {
				return err
			}
			q.Set("crew_id", crewID)
		}
		path := "/api/v1/hooks"
		if enc := q.Encode(); enc != "" {
			path += "?" + enc
		}

		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		// NOTE: this used to decode `target` and `allowed_shell`, neither of
		// which /api/v1/hooks has ever emitted — so the TARGET column was
		// always blank. The target is derived from handler_config below.
		var body struct {
			Rows []struct {
				ID            string         `json:"id"`
				CrewID        string         `json:"crew_id"`
				Event         string         `json:"event"`
				HandlerKind   string         `json:"handler_kind"`
				HandlerConfig map[string]any `json:"handler_config"`
				Enabled       bool           `json:"enabled"`
				Blocking      bool           `json:"blocking"`
				CreatedAt     string         `json:"created_at"`
			} `json:"rows"`
			Count int `json:"count"`
		}
		if err := cli.ReadJSON(resp, &body); err != nil {
			return err
		}

		f := newFormatter()
		return f.AutoHuman(body.Rows, func() {
			if len(body.Rows) == 0 {
				fmt.Println("(no hooks registered)")
				return
			}
			header := []string{"ID", "EVENT", "HANDLER", "TARGET", "ENABLED", "BLOCKING", "CREATED"}
			rows := make([][]string, 0, len(body.Rows))
			for _, r := range body.Rows {
				enabled := "no"
				if r.Enabled {
					enabled = "yes"
				}
				blocking := "no"
				if r.Blocking {
					blocking = "yes"
				}
				target := truncateString(hookTarget(r.HandlerKind, r.HandlerConfig), 40)
				rows = append(rows, []string{r.ID, r.Event, r.HandlerKind, target, enabled, blocking, r.CreatedAt})
			}
			f.Table(header, rows)
		})
	},
}

var hooksEnableCmd = &cobra.Command{
	Use:   "enable <id>",
	Short: "Enable a registered hook",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return hooksToggle(args[0], true)
	},
}

var hooksDisableCmd = &cobra.Command{
	Use:   "disable <id>",
	Short: "Disable a registered hook",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return hooksToggle(args[0], false)
	},
}

// hooksToggle drives the enable/disable subcommands through a single
// code path — the two endpoints differ only in URL suffix, the body
// is empty and the response shape is the same on both. Keeping the
// RunE bodies thin makes the test plan simpler (one path, two URLs).
func hooksToggle(id string, enable bool) error {
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}
	client := newAPIClient()

	verb := "disable"
	if enable {
		verb = "enable"
	}
	path := fmt.Sprintf("/api/v1/hooks/%s/%s", url.PathEscape(id), verb)
	resp, err := client.Post(path, nil)
	if err != nil {
		return err
	}
	if err := cli.CheckError(resp); err != nil {
		return err
	}
	fmt.Printf("Hook %s: %sd\n", id, verb)
	return nil
}

func init() {
	hooksListCmd.Flags().String("crew", "", "Filter by crew slug or ID")
	hookWriteFlags(hooksCreateCmd)
	hookWriteFlags(hooksUpdateCmd)
	hooksDeleteCmd.Flags().Bool("yes", false, "Confirm deletion (required — deletion is permanent)")

	hooksCmd.AddCommand(hooksListCmd)
	hooksCmd.AddCommand(hooksCreateCmd)
	hooksCmd.AddCommand(hooksUpdateCmd)
	hooksCmd.AddCommand(hooksDeleteCmd)
	hooksCmd.AddCommand(hooksEnableCmd)
	hooksCmd.AddCommand(hooksDisableCmd)
}
