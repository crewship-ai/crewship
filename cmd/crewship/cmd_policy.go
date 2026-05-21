package main

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// policyCmd groups the PR-B F2 per-crew autonomy + behavior-mode
// commands. The server-side state lives in crews.autonomy_level /
// crews.behavior_mode (v98), and PUTting through this CLI is the
// canonical way operators rotate a crew between strict prod-safe
// defaults and looser settings for research / discovery workflows.
//
// Subcommand split:
//   - get   — single crew, full policy + audit triple
//   - set   — single crew, atomic UPDATE; loose transitions confirmed
//   - list  — workspace-wide overview (sorted by crew name)
//
// Slug → ID resolution goes through the existing resolveCrewID helper
// so a typo surfaces as "crew not found" rather than a 404 from the
// API (which would point at the path-param, not the user's flag).
var policyCmd = &cobra.Command{
	Use:   "policy",
	Short: "Manage per-crew autonomy and behavior-mode policy (PR-B F2)",
	Long: `Manage per-crew autonomy and behavior-mode policy.

Each crew carries an autonomy_level (strict | guided | trusted | full)
and a behavior_mode (warn | block) that together drive every HITL
decision the orchestrator makes — memory writes, skill creation,
behavior-monitor escalations, ephemeral spawns.

Subcommands:
  get   Show the current policy for one crew, including the audit triple
        (who set it, when, optional reason).
  set   Update a crew's policy. Loose transitions (any → trusted/full)
        confirm interactively unless --yes is passed; --level full
        additionally requires --reason (the API rejects without one).
  list  Show every crew's policy in the current workspace.

Examples:
  crewship policy get   --crew engineering
  crewship policy set   --crew engineering --level trusted --behavior warn
  crewship policy set   --crew prod-ops --level full --behavior warn \
    --reason "Friday production freeze — operator on call" --yes
  crewship policy list  --format json`,
}

// validAutonomyLevels mirrors policy.AutonomyLevel; duplicated here
// (rather than importing the package) so the CLI can validate input
// without dragging the resolver + DB types into the cmd build.
// internal/policy/types.go is the source of truth — keep the two in
// lockstep when a new level is added.
var validAutonomyLevels = map[string]struct{}{
	"strict":  {},
	"guided":  {},
	"trusted": {},
	"full":    {},
}

// validBehaviorModes mirrors policy.BehaviorMode for the same reason
// as validAutonomyLevels.
var validBehaviorModes = map[string]struct{}{
	"warn":  {},
	"block": {},
}

// looseLevels are the autonomy levels that trigger an interactive
// confirmation on `policy set` (unless --yes is passed). Strict and
// guided don't qualify — they're the prod-safe defaults; only opt-in
// trust extensions warrant the extra friction.
var looseLevels = map[string]struct{}{
	"trusted": {},
	"full":    {},
}

var policyGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Show the current policy for one crew",
	Long: `Fetch the current autonomy_level + behavior_mode + audit triple
for one crew and render it via the standard formatter.

Examples:
  crewship policy get --crew engineering
  crewship policy get --crew engineering --format json | jq .reason`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		crewSlug, _ := cmd.Flags().GetString("crew")
		if crewSlug == "" {
			return errors.New("--crew is required")
		}
		client := newAPIClient()
		crewID, err := resolveCrewID(client, crewSlug)
		if err != nil {
			return err
		}

		resp, err := client.Get("/api/v1/crews/" + url.PathEscape(crewID) + "/policy")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var p policyWire
		if err := cli.ReadJSON(resp, &p); err != nil {
			return err
		}

		f := newFormatter()
		// Detail view (key/value) for single-crew get; json/yaml/ndjson
		// pass through the wire object verbatim so jq/yq pipelines see
		// the canonical field names.
		pairs := [][]string{
			{"CREW", p.CrewID},
			{"AUTONOMY", p.AutonomyLevel},
			{"BEHAVIOR", p.BehaviorMode},
			{"SET BY", dashIfEmpty(p.SetByUserID)},
			{"SET AT", dashIfEmpty(p.SetAt)},
			{"REASON", dashIfEmpty(p.Reason)},
		}
		return f.AutoDetail(p, pairs)
	},
}

var policySetCmd = &cobra.Command{
	Use:   "set",
	Short: "Update a crew's autonomy_level and behavior_mode",
	Long: `Update a crew's policy. Atomic single-PUT; on success the
shared resolver cache is invalidated server-side so downstream
subsystems (memory write gating, skill creation HITL, behavior
monitor, ephemeral spawn) see the new state immediately.

Validation:
  --level    must be one of: strict | guided | trusted | full
  --behavior must be one of: warn | block (default: warn)
  --reason   required when --level=full (the API rejects without it)

Loose transitions (any → trusted | full) prompt for confirmation
unless --yes is passed. This matches the safety idiom used by
crewship crew delete / crewship checkpoint delete.

Examples:
  crewship policy set --crew engineering --level trusted
  crewship policy set --crew prod-ops --level full --behavior warn \
    --reason "Friday production freeze — operator on call" --yes
  crewship policy set --crew sandbox --level strict --behavior block`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		crewSlug, _ := cmd.Flags().GetString("crew")
		level, _ := cmd.Flags().GetString("level")
		behavior, _ := cmd.Flags().GetString("behavior")
		reason, _ := cmd.Flags().GetString("reason")

		if crewSlug == "" {
			return errors.New("--crew is required")
		}
		if level == "" {
			return errors.New("--level is required (strict|guided|trusted|full)")
		}
		// Normalise to lowercase so "STRICT", "Trusted", etc. pass —
		// the API check is case-sensitive but operator memory isn't.
		level = strings.ToLower(strings.TrimSpace(level))
		if _, ok := validAutonomyLevels[level]; !ok {
			return fmt.Errorf("invalid --level %q (want strict|guided|trusted|full)", level)
		}

		if behavior == "" {
			behavior = "warn"
		}
		behavior = strings.ToLower(strings.TrimSpace(behavior))
		if _, ok := validBehaviorModes[behavior]; !ok {
			return fmt.Errorf("invalid --behavior %q (want warn|block)", behavior)
		}

		// Local enforcement of the PRD §6 F2 "reason required for full"
		// rule — also enforced server-side, but failing fast here means
		// the operator sees a clean error before the network round-trip.
		if level == "full" && strings.TrimSpace(reason) == "" {
			return errors.New("--reason is required when --level=full")
		}

		// Loose transitions get an interactive confirmation. We can't
		// tell whether the previous level was already loose without a
		// GET round-trip — keeping the prompt unconditional for any
		// move TO trusted/full is the simpler, safer policy and matches
		// the "loud on irreversible-ish actions" idiom the rest of the
		// CLI uses for delete-like commands.
		if _, loose := looseLevels[level]; loose {
			msg := fmt.Sprintf("Set autonomy_level=%s for crew %q? Memory writes and skill assignments will auto-execute.", level, crewSlug)
			if err := confirmAction(cmd, msg); err != nil {
				return err
			}
		}

		client := newAPIClient()
		crewID, err := resolveCrewID(client, crewSlug)
		if err != nil {
			return err
		}

		body := map[string]string{
			"autonomy_level": level,
			"behavior_mode":  behavior,
		}
		if reason != "" {
			body["reason"] = strings.TrimSpace(reason)
		}

		resp, err := client.Put("/api/v1/crews/"+url.PathEscape(crewID)+"/policy", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var p policyWire
		if err := cli.ReadJSON(resp, &p); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Policy updated for crew %s: %s / %s", p.CrewID, p.AutonomyLevel, p.BehaviorMode))
		return nil
	},
}

var policyListCmd = &cobra.Command{
	Use:   "list",
	Short: "List every crew's policy in the current workspace",
	Long: `Render the policy for every crew in the current workspace,
sorted by crew name (case-insensitive). Useful for fleet-wide audits
and for spotting crews that haven't been moved off the guided/warn
defaults yet.

The crew NAME column is enriched from /api/v1/crews because the
policy API itself only returns CrewID — sorting on a CUID would
produce ordering that's stable but useless to a human reader.

Examples:
  crewship policy list
  crewship policy list --format json
  crewship policy list --format yaml`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()

		// Fetch policies first — that's the authoritative payload. If
		// it fails (workspace context error, 500) we surface the error
		// before even bothering with the crew-name enrichment.
		resp, err := client.Get("/api/v1/policies")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var policies []policyWire
		if err := cli.ReadJSON(resp, &policies); err != nil {
			return err
		}

		// Best-effort crew-name enrichment. A 200-with-empty list is
		// fine (treat all crews as nameless); a network/HTTP failure
		// is non-fatal because the policies themselves are usable
		// without the friendly name — fall back to CrewID + log via
		// stderr so the operator knows enrichment was skipped.
		nameByID := map[string]string{}
		if crewsResp, err := client.Get("/api/v1/crews"); err == nil {
			if cli.CheckError(crewsResp) == nil {
				var crews []struct {
					ID   string `json:"id"`
					Name string `json:"name"`
					Slug string `json:"slug"`
				}
				if err := cli.ReadJSON(crewsResp, &crews); err == nil {
					for _, c := range crews {
						display := c.Name
						if display == "" {
							display = c.Slug
						}
						nameByID[c.ID] = display
					}
				}
			} else {
				crewsResp.Body.Close()
			}
		}

		// Pre-compute the display name once per row so the sort
		// comparator doesn't re-look-up on every comparison (an O(n²)
		// map lookup pattern is wasteful even at small crew counts).
		type row struct {
			policyWire
			DisplayName string
		}
		rows := make([]row, 0, len(policies))
		for _, p := range policies {
			rows = append(rows, row{policyWire: p, DisplayName: nameByID[p.CrewID]})
		}
		sort.SliceStable(rows, func(i, j int) bool {
			a := rows[i].DisplayName
			if a == "" {
				a = rows[i].CrewID
			}
			b := rows[j].DisplayName
			if b == "" {
				b = rows[j].CrewID
			}
			return strings.ToLower(a) < strings.ToLower(b)
		})

		headers := []string{"CREW", "ID", "AUTONOMY", "BEHAVIOR", "SET AT", "REASON"}
		tableRows := make([][]string, 0, len(rows))
		for _, r := range rows {
			name := r.DisplayName
			if name == "" {
				name = "—"
			}
			tableRows = append(tableRows, []string{
				name,
				r.CrewID,
				r.AutonomyLevel,
				r.BehaviorMode,
				dashIfEmpty(r.SetAt),
				dashIfEmpty(truncateString(r.Reason, 48)),
			})
		}
		return newFormatter().Auto(policies, headers, tableRows)
	},
}

// policyWire mirrors internal/api/crew_policy.go crewPolicyResponse —
// duplicated rather than imported to keep cmd build slim. Adding a
// field on the API side requires extending this struct in lockstep
// (the CLI silently drops unknown JSON fields on decode).
type policyWire struct {
	CrewID        string `json:"crew_id"`
	AutonomyLevel string `json:"autonomy_level"`
	BehaviorMode  string `json:"behavior_mode"`
	SetByUserID   string `json:"set_by_user_id,omitempty"`
	SetAt         string `json:"set_at,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

func init() {
	policyGetCmd.Flags().String("crew", "", "Crew slug or ID (required)")

	policySetCmd.Flags().String("crew", "", "Crew slug or ID (required)")
	policySetCmd.Flags().String("level", "", "Autonomy level: strict|guided|trusted|full (required)")
	policySetCmd.Flags().String("behavior", "warn", "Behavior mode: warn|block")
	policySetCmd.Flags().String("reason", "", "Operator-supplied reason (required when --level=full)")
	policySetCmd.Flags().Bool("yes", false, "Skip the loose-transition confirmation prompt")

	policyCmd.AddCommand(policyGetCmd)
	policyCmd.AddCommand(policySetCmd)
	policyCmd.AddCommand(policyListCmd)
}
