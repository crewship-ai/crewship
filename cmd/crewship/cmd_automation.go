package main

import (
	"encoding/json"
	"fmt"
	"sort"
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

A mission.status_change entry carries:

  action   what happened, from a closed set: status_changed,
           priority_changed, review_approved, task_failed, … — this is
           the key to write predicates against
  details  human-readable prose, e.g. "BACKLOG → TODO". For display,
           NOT for matching: it is a sentence and breaks the day
           somebody rewords it
  from     the status before the change — only on action=status_changed
  to       the status after it — only on action=status_changed

So "fire when an issue moves to DONE" is one predicate:

  crewship automation create --name "on close" \
      --event mission.status_change --payload-equals to=DONE \
      --routine post-close

--event and --payload-equals are checked at save time: --event must be a
registered journal entry type, and for a curated set of event types
(mission.status_change among them — see the guide) --payload-equals keys
are checked against the ones that type's emitter actually writes. Other
event types have no key check yet, so a key the emitter never writes is
still accepted there and matches nothing, silently. Either way, check a
rule against real history before trusting it:

  crewship automation preview --event mission.status_change \
      --payload-equals to=DONE`,
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
supported. The server checks it against the journal's closed registry and
refuses anything unregistered, naming real alternatives — but a value can be
well-formed AND registered and still be the wrong type for what you meant,
so confirm it first:

    crewship journal --type mission.status_change

Predicate flags narrow which entries of that type match; every one you pass
must be satisfied, and passing none matches all of them.

--payload-equals keys are checked at save time for a curated set of event
types (mission.status_change among them). For any event type NOT on that
list, a key that no emitter writes is still accepted and matches nothing,
silently — same as an unchecked --event used to be. Read one real entry
before you write the predicate —

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
cannot clobber a matcher somebody edited a moment ago.

The MATCHER is the exception, because the API writes it as a whole object.
Passing any predicate flag replaces every predicate, so a rule matched on a
crew and a payload key, updated with --payload-equals alone, would silently
stop being crew-scoped. That is refused: re-supply the predicates you want to
keep, or pass --replace-matcher to say you meant to drop them.`,
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
		if m, ok := body["matcher"].(map[string]any); ok {
			replace, _ := cmd.Flags().GetBool("replace-matcher")
			if err := refuseSilentMatcherLoss(args[0], m, replace); err != nil {
				return err
			}
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

	matcher, err := matcherFromFlags(cmd)
	if err != nil {
		return nil, err
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
	// preview takes the predicate flags too — the whole point is checking a
	// candidate before it is saved, which needs the same vocabulary the save
	// takes. It skips --routine and the burst controls: they do not change
	// what a rule MATCHES.
	automationUpdateCmd.Flags().Bool("replace-matcher", false,
		"Replace the whole matcher, dropping any predicate not re-supplied")
	for _, c := range []*cobra.Command{automationCreateCmd, automationUpdateCmd, automationPreviewCmd} {
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
		c.Flags().Int("max-per-hour", 0, "Burst brake: cap runs this automation may cause per hour (per server process; a restart clears it)")
	}

	automationCmd.AddCommand(automationListCmd)
	automationCmd.AddCommand(automationPreviewCmd)
	automationCmd.AddCommand(automationCreateCmd)
	automationCmd.AddCommand(automationUpdateCmd)
	automationCmd.AddCommand(automationEnableCmd)
	automationCmd.AddCommand(automationDisableCmd)
	automationCmd.AddCommand(automationDeleteCmd)
}

// automationPreviewCmd answers the question `automation create` warns about
// in its own help text and could not answer: would this rule ever fire?
//
// A matcher is written blind today — save it, wait, notice nothing happened.
// The shipped example predicated on a payload key the event does not carry,
// so the first rule most readers built did nothing and told them nothing.
// Judging a candidate against history moves that answer to before the rule
// is trusted, and names the clause responsible when the answer is no.
var automationPreviewCmd = &cobra.Command{
	Use:   "preview [id]",
	Short: "Replay recent history against a rule and report what it would have caught",
	Long: `Replay recent journal entries against a matcher and report what it would
have caught — without saving anything and without starting a run.

With an id, previews a saved rule. Without one, previews a candidate built
from the same flags ` + "`create`" + ` takes, so a predicate can be checked before it
is committed to.

When nothing matches, the clause that excluded the most entries is named,
along with what it wanted and what was actually there. A predicate on a
payload key the event never carries is called out as such: no change of
value can rescue it.

Examples:
  crewship automation preview aut_1234
  crewship automation preview --event mission.status_change --payload-equals action=status_changed`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		body := map[string]any{}
		if len(args) == 1 {
			body["automation_id"] = args[0]
		} else {
			event, _ := cmd.Flags().GetString("event")
			if event == "" {
				return fmt.Errorf("give an automation id, or --event to preview a candidate rule")
			}
			body["event_type"] = event
			matcher, err := matcherFromFlags(cmd)
			if err != nil {
				return err
			}
			body["matcher"] = matcher
		}

		resp, err := newAPIClient().Post("/api/v1/automations/preview", body)
		if err != nil {
			return err
		}
		var out struct {
			EventType   string `json:"event_type"`
			WindowHours int    `json:"window_hours"`
			Scanned     int    `json:"scanned"`
			Matched     int    `json:"matched"`
			TopReject   struct {
				Clause    string `json:"clause"`
				Count     int    `json:"count"`
				Detail    string `json:"detail"`
				KeyAbsent bool   `json:"key_absent"`
			} `json:"top_rejection"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}

		f := newFormatter()
		return f.AutoHuman(out, func() {
			fmt.Printf("%s · last %dh\n", out.EventType, out.WindowHours)
			switch {
			case out.Scanned == 0:
				// Not a verdict on the rule. Saying "0 matched" here would
				// send a reader to edit a matcher that may be fine.
				fmt.Printf("  no %s entries in the window — nothing to judge this rule against yet\n",
					out.EventType)
			case out.Matched > 0:
				fmt.Printf("  would have fired %d time(s) out of %d entries\n", out.Matched, out.Scanned)
			default:
				fmt.Printf("  would NOT have fired: 0 of %d entries matched\n", out.Scanned)
				if out.TopReject.Clause != "" {
					fmt.Printf("  %s excluded %d of them — %s\n",
						out.TopReject.Clause, out.TopReject.Count, out.TopReject.Detail)
					if out.TopReject.KeyAbsent {
						fmt.Println("  that key is absent, so no value will match; predicate on a key the event carries")
					}
				}
			}
		})
	},
}

// matcherFromFlags builds a matcher out of the predicate flags.
//
// Shared by `create`, `update` and `preview` deliberately: a preview that
// interpreted a flag differently from the command that saves it would be a
// preview of a rule nobody is going to run.
func matcherFromFlags(cmd *cobra.Command) (map[string]any, error) {
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
	return matcher, nil
}

// refuseSilentMatcherLoss stops an update from dropping predicates the caller
// did not mention.
//
// The API writes `matcher` as a whole object, so a PATCH carrying
// --payload-equals alone replaces every predicate the rule had. A rule scoped
// to one crew silently widens to the entire workspace and starts firing on
// events it was never meant to see — and the CLI reported success.
//
// Same doctrine the action object already follows a few lines up: refuse
// rather than silently detach. --replace-matcher is the way to say you meant
// it.
//
// A failed read is NOT a reason to block the write: the check is a guard
// against a silent mistake, not an authorisation, and turning a transient GET
// failure into "you cannot edit your rule" trades one bad outcome for a worse
// one. It falls through and lets the PATCH proceed.
func refuseSilentMatcherLoss(id string, next map[string]any, replace bool) error {
	if replace {
		return nil
	}
	// Read through LIST, not GET /automations/{id} — that route does not exist
	// (the API registers list, patch and delete only), and the repo's route
	// contract test catches the call. Worth stating because the failure would
	// have been silent: the helper falls through on a read error, so a 404 here
	// would leave a guard that is present, reports nothing, and protects
	// nothing.
	resp, err := newAPIClient().Get("/api/v1/automations")
	if err != nil {
		return nil
	}
	var listed struct {
		Automations []struct {
			ID      string         `json:"id"`
			Matcher map[string]any `json:"matcher"`
		} `json:"automations"`
	}
	if err := cli.ReadJSON(resp, &listed); err != nil {
		return nil
	}
	var stored map[string]any
	for _, a := range listed.Automations {
		if a.ID == id {
			stored = a.Matcher
			break
		}
	}
	if stored == nil {
		// Unknown id: let the PATCH answer 404 rather than inventing one here.
		return nil
	}
	lost := droppedPredicates(stored, next)
	if len(lost) == 0 {
		return nil
	}
	return matcherLossError(lost)
}

// droppedPredicates lists the predicates a stored matcher has that the
// replacement does not. Split from the network call so the decision — the part
// that can be wrong — is testable without a server.
func droppedPredicates(cur, next map[string]any) []string {
	var lost []string
	for k, v := range cur {
		if isEmptyPredicate(v) {
			continue
		}
		if _, kept := next[k]; !kept {
			lost = append(lost, k)
		}
	}
	sort.Strings(lost)
	return lost
}

func matcherLossError(lost []string) error {
	return fmt.Errorf("this update would delete %d predicate(s) the rule has and you did not "+
		"re-supply: %s\n"+
		"The matcher is written as a whole object, so passing any predicate flag replaces all of "+
		"them. Re-supply the ones you want to keep, or pass --replace-matcher if dropping them is "+
		"what you meant.", len(lost), strings.Join(lost, ", "))
}

// isEmptyPredicate treats a present-but-empty value as absent: the server
// stores {} / [] for a predicate never set, and reporting those as "lost"
// would refuse an update that removes nothing.
func isEmptyPredicate(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	case string:
		return t == ""
	}
	return false
}
