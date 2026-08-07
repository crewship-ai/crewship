package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// automationCmd drives /api/v1/automations — the workspace rules that turn a
// journal event into a deferred routine run.
//
// The CLI is the supported way an agent operates Crewship, so these commands
// exist for the same reason the routes do rather than as a convenience: an
// endpoint an agent cannot reach through the CLI is an endpoint no agent can
// use safely.
var automationCmd = &cobra.Command{
	Use:   "automation",
	Short: "Manage automations: run a routine when a journal event happens",
	Long: `Manage automations.

An automation watches ONE journal event type in this workspace and, when an
entry matches its predicate, parks a deferred run of a routine. It can only
enqueue: it never executes anything inline and holds no veto over anything.

  crewship automation list
  crewship automation create --name "triage on status change" \
      --event mission.status_change --routine triage \
      --payload-equals action=status_changed \
      --input issue='{{ event.mission_id }}'
  crewship automation disable <id>

Inputs may reference the triggering entry with {{ event.mission_id }},
{{ event.agent_id }}, {{ event.crew_id }}, {{ event.run_id }} and
{{ event.payload.<key> }} — the same renderer routine steps use.

A mission.status_change entry carries exactly two payload keys:

  action   what happened, from a closed set: status_changed,
           priority_changed, review_approved, task_failed, … — this is
           the key to write predicates against
  details  human-readable prose, e.g. "BACKLOG → TODO"

Note what that means: "fire when an issue moves to DONE" is NOT
expressible as a predicate. The target status only appears inside
details, which is prose and not stable to match on. Match on the action
and let the routine read {{ event.payload.details }} to decide.`,
}

// automationRow mirrors the API's automation JSON. --format json must pass
// every server field through, so the struct is the full shape rather than the
// columns the table happens to print.
type automationRow struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
	EventType   string `json:"event_type"`
	Matcher     struct {
		CrewIDs       []string       `json:"crew_ids"`
		AgentIDs      []string       `json:"agent_ids"`
		MissionIDs    []string       `json:"mission_ids"`
		Severities    []string       `json:"severities"`
		PayloadEquals map[string]any `json:"payload_equals"`
	} `json:"matcher"`
	ActionKind string `json:"action_kind"`
	Action     struct {
		RoutineSlug string         `json:"routine_slug"`
		Inputs      map[string]any `json:"inputs"`
	} `json:"action"`
	DebounceSeconds int    `json:"debounce_seconds"`
	MaxPerHour      int    `json:"max_per_hour"`
	CreatedBy       string `json:"created_by"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

var automationListCmd = &cobra.Command{
	Use:   "list",
	Short: "List automations in this workspace",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		resp, err := newAPIClient().Get("/api/v1/automations")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var body struct {
			Automations []automationRow `json:"automations"`
			Count       int             `json:"count"`
		}
		if err := cli.ReadJSON(resp, &body); err != nil {
			return err
		}
		f := newFormatter()
		headers := []string{"ID", "NAME", "EVENT", "ROUTINE", "ENABLED", "DEBOUNCE", "MAX/HOUR"}
		var rows [][]string
		for _, a := range body.Automations {
			rows = append(rows, []string{
				a.ID, a.Name, a.EventType, a.Action.RoutineSlug,
				strconv.FormatBool(a.Enabled),
				strconv.Itoa(a.DebounceSeconds) + "s",
				strconv.Itoa(a.MaxPerHour),
			})
		}
		return f.Auto(body.Automations, headers, rows)
	},
}

var automationCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create an automation",
	Long: `Create an automation.

--event takes ONE journal entry type. A rule that fires on "anything" is not
supported: confirm the type exists first with

    crewship journal --type mission.status_change

because a typo produces a rule that is saved, listed, and never fires.

Predicate flags narrow which entries of that type match; every one you pass
must be satisfied, and passing none matches all of them.

--payload-equals has the same silent-failure mode as a typo'd --event, and a
worse one: a key that no emitter writes is accepted and matches nothing. Read
one real entry before you write the predicate —

    crewship journal --type mission.status_change --lines 1 --format json

— and match on a key you can see in its payload.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		body, err := automationWriteBody(cmd, true)
		if err != nil {
			return err
		}
		resp, err := newAPIClient().Post("/api/v1/automations", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var created automationRow
		if err := cli.ReadJSON(resp, &created); err != nil {
			return err
		}
		f := newFormatter()
		return f.AutoHuman(created, func() {
			cli.PrintSuccess(fmt.Sprintf("Automation %s created — %s → routine %s.",
				created.ID, created.EventType, created.Action.RoutineSlug))
		})
	},
}

var automationUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update an automation",
	Long: `Update an automation.

The write is sparse: only the flags you pass are sent, so changing the cap
cannot clobber a matcher somebody edited a moment ago.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		body, err := automationWriteBody(cmd, false)
		if err != nil {
			return err
		}
		if len(body) == 0 {
			return fmt.Errorf("nothing to update — pass at least one field flag")
		}
		return patchAutomation(args[0], body, "updated")
	},
}

var automationEnableCmd = &cobra.Command{
	Use:   "enable <id>",
	Short: "Enable an automation",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		return patchAutomation(args[0], map[string]any{"enabled": true}, "enabled")
	},
}

var automationDisableCmd = &cobra.Command{
	Use:   "disable <id>",
	Short: "Disable an automation without deleting it",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		return patchAutomation(args[0], map[string]any{"enabled": false}, "disabled")
	},
}

var automationDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete an automation",
	Long: `Delete an automation.

The row is soft-deleted: it stops matching immediately, and stays readable in
the database so a run it caused can still explain where it came from.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		resp, err := newAPIClient().Delete("/api/v1/automations/" + args[0])
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()
		cli.PrintSuccess(fmt.Sprintf("Automation %s deleted.", args[0]))
		return nil
	},
}

// patchAutomation is the single write behind update / enable / disable — all
// three are the same PATCH, and giving them one code path means an enable can
// never diverge from an update that sets the same field.
func patchAutomation(id string, body map[string]any, verb string) error {
	resp, err := newAPIClient().Patch("/api/v1/automations/"+id, body)
	if err != nil {
		return err
	}
	if err := cli.CheckError(resp); err != nil {
		return err
	}
	var updated automationRow
	if err := cli.ReadJSON(resp, &updated); err != nil {
		return err
	}
	f := newFormatter()
	return f.AutoHuman(updated, func() {
		cli.PrintSuccess(fmt.Sprintf("Automation %s %s.", id, verb))
	})
}

// automationWriteBody builds the sparse PATCH/POST body from whichever flags
// the caller actually set. cmd.Flags().Changed is what makes it sparse: a
// default value is not an instruction.
func automationWriteBody(cmd *cobra.Command, create bool) (map[string]any, error) {
	body := map[string]any{}

	if name, _ := cmd.Flags().GetString("name"); cmd.Flags().Changed("name") || (create && name != "") {
		body["name"] = name
	}
	if ev, _ := cmd.Flags().GetString("event"); cmd.Flags().Changed("event") || (create && ev != "") {
		body["event_type"] = ev
	}
	if d, _ := cmd.Flags().GetInt("debounce-seconds"); cmd.Flags().Changed("debounce-seconds") {
		body["debounce_seconds"] = d
	}
	if m, _ := cmd.Flags().GetInt("max-per-hour"); cmd.Flags().Changed("max-per-hour") {
		body["max_per_hour"] = m
	}

	matcher := map[string]any{}
	if v, _ := cmd.Flags().GetStringSlice("crew"); len(v) > 0 {
		matcher["crew_ids"] = v
	}
	if v, _ := cmd.Flags().GetStringSlice("agent"); len(v) > 0 {
		matcher["agent_ids"] = v
	}
	if v, _ := cmd.Flags().GetStringSlice("mission"); len(v) > 0 {
		matcher["mission_ids"] = v
	}
	if v, _ := cmd.Flags().GetStringSlice("severity"); len(v) > 0 {
		matcher["severities"] = v
	}
	if v, _ := cmd.Flags().GetStringSlice("payload-equals"); len(v) > 0 {
		pairs, err := parseKeyValues(v, "--payload-equals")
		if err != nil {
			return nil, err
		}
		matcher["payload_equals"] = pairs
	}
	if len(matcher) > 0 {
		body["matcher"] = matcher
	}

	routine, _ := cmd.Flags().GetString("routine")
	inputsRaw, _ := cmd.Flags().GetStringSlice("input")
	if routine != "" || len(inputsRaw) > 0 {
		action := map[string]any{}
		if routine != "" {
			action["routine_slug"] = routine
		} else if !create {
			// A PATCH that carries inputs but no routine would replace the
			// whole action object and blank the target. Refuse rather than
			// silently detach the rule from its routine.
			return nil, fmt.Errorf("--input requires --routine (the action is written as a whole)")
		}
		if len(inputsRaw) > 0 {
			pairs, err := parseKeyValues(inputsRaw, "--input")
			if err != nil {
				return nil, err
			}
			action["inputs"] = pairs
		}
		body["action"] = action
	}

	if create {
		if body["name"] == nil {
			return nil, fmt.Errorf("--name is required")
		}
		if body["event_type"] == nil {
			return nil, fmt.Errorf("--event is required (a journal entry type, e.g. mission.status_change)")
		}
		if routine == "" {
			return nil, fmt.Errorf("--routine is required (the routine slug to run)")
		}
	}
	return body, nil
}

// parseKeyValues turns repeated key=value flags into a map. A value that
// parses as JSON is kept as JSON (so --payload-equals count=3 matches the
// number 3, not the string "3"); everything else stays a string.
func parseKeyValues(pairs []string, flag string) (map[string]any, error) {
	out := make(map[string]any, len(pairs))
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("%s expects key=value, got %q", flag, p)
		}
		var decoded any
		if err := json.Unmarshal([]byte(v), &decoded); err == nil {
			out[k] = decoded
			continue
		}
		out[k] = v
	}
	return out, nil
}

func init() {
	for _, c := range []*cobra.Command{automationCreateCmd, automationUpdateCmd} {
		c.Flags().String("name", "", "Human-readable name")
		c.Flags().String("event", "", "Journal entry type to watch, e.g. mission.status_change")
		c.Flags().String("routine", "", "Routine slug to run when an entry matches")
		c.Flags().StringSlice("input", nil, "Routine input as key=value; values may use {{ event.* }} (repeatable)")
		c.Flags().StringSlice("crew", nil, "Only match entries from these crew IDs (repeatable)")
		c.Flags().StringSlice("agent", nil, "Only match entries from these agent IDs (repeatable)")
		c.Flags().StringSlice("mission", nil, "Only match entries on these mission IDs (repeatable)")
		c.Flags().StringSlice("severity", nil, "Only match these severities: info|notice|warn|error (repeatable)")
		c.Flags().StringSlice("payload-equals", nil, "Only match when the journal payload field equals this, as key=value (repeatable)")
		c.Flags().Int("debounce-seconds", 0, "Hold the enqueued run open this long for further events to coalesce into")
		c.Flags().Int("max-per-hour", 0, "Cap runs this automation may cause per hour")
	}

	automationCmd.AddCommand(automationListCmd)
	automationCmd.AddCommand(automationCreateCmd)
	automationCmd.AddCommand(automationUpdateCmd)
	automationCmd.AddCommand(automationEnableCmd)
	automationCmd.AddCommand(automationDisableCmd)
	automationCmd.AddCommand(automationDeleteCmd)
}
