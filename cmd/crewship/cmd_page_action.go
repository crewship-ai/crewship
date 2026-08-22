package main

// `crewship page action` — dispatch a panel's declared action
// (docs/prd/pages.md §8b, §11's "every API endpoint gets a CLI command").
//
//	GET  /api/v1/pages/{slug}/panels/{id}/actions             page actions <slug>/<panel>
//	POST /api/v1/pages/{slug}/panels/{id}/actions/{actionId}  page action  <slug>/<panel> <id>
//
// Three things this command deliberately does NOT do, each of them a §8b
// property that would be undone by a convenience flag:
//
//  1. There is no --routine. The routine is read from the stored page spec at
//     dispatch time (§8b.2); a flag naming one would be the field the wire
//     format is specified not to have, and the server would ignore it anyway.
//     `page actions` prints the routine each action resolves to, so an operator
//     can see what a click will run without being able to change it.
//
//  2. It never waits for the run. The endpoint answers 202 with a pending id
//     and the run is watched elsewhere (§8b.3) — a CLI that polled to
//     completion would reintroduce the held connection the 202 exists to avoid.
//
//  3. It sends an Idempotency-Key on every dispatch, generated locally, one per
//     invocation. Pages is the first consumer of that header in this codebase
//     (§8b.3 — "zero frontend call sites send it today"); a retried command,
//     from a flaky link or a shell loop, resolves to the original dispatch
//     rather than starting a second one. --idempotency-key pins it explicitly
//     so a script can retry across processes.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

// pageActionJSON is one declared action as `GET …/actions` serves it.
type pageActionJSON struct {
	ID      string                `json:"id"`
	Kind    string                `json:"kind"`
	Label   string                `json:"label"`
	Style   string                `json:"style"`
	Routine string                `json:"routine"`
	Confirm *pageActionConfirmSON `json:"confirm"`
	Inputs  []pageActionInputJSON `json:"inputs"`
	Target  []string              `json:"target"`
	Ref     *pageActionRefJSON    `json:"ref"`
}

type pageActionConfirmSON struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

type pageActionInputJSON struct {
	Name     string   `json:"name"`
	Label    string   `json:"label"`
	Type     string   `json:"type"`
	Required bool     `json:"required"`
	Default  string   `json:"default"`
	Options  []string `json:"options"`
}

type pageActionRefJSON struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// pageActionDispatchJSON is the 202 receipt.
type pageActionDispatchJSON struct {
	Status    string `json:"status"`
	PendingID string `json:"pending_id"`
	FireAt    string `json:"fire_at"`
	Deduped   bool   `json:"deduped"`
	Coalesced bool   `json:"coalesced"`
	Page      string `json:"page"`
	Panel     string `json:"panel"`
	Action    string `json:"action"`
	Routine   string `json:"routine"`
}

// ── actions (list) ─────────────────────────────────────────────────────────

var pageActionsCmd = &cobra.Command{
	Use:   "actions <slug>/<panel>",
	Short: "List the actions a panel declares, and the routine each one runs",
	Long: `List a panel's declared actions.

The list comes from the page's stored spec — the same list the server
resolves a click against — so what you see here is exactly what can be
dispatched. Only "call" actions reach the server; a "link" navigates, a
"toggle" is client-side panel state, and a "custom" action resolves to a
handler built into the web client.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		slug, panel, err := splitPagePanelRef(args[0])
		if err != nil {
			return err
		}
		client, err := pageClient()
		if err != nil {
			return err
		}
		resp, err := client.Get(fmt.Sprintf("/api/v1/pages/%s/panels/%s/actions",
			pagePathEscape(slug), pagePathEscape(panel)))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := pageCheckError(resp); err != nil {
			return err
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("read response: %w", err)
		}

		f := newFormatter()
		switch f.Format {
		case "json", "yaml", "ndjson":
			return pageEmitMachine(f, body, "{}")
		}
		var doc struct {
			Actions []pageActionJSON `json:"actions"`
		}
		if err := json.Unmarshal(body, &doc); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		if len(doc.Actions) == 0 {
			// Under `quiet` the caller is a pipe expecting one action id per
			// line, and "declares no actions." is a line — it would be read as
			// an id. Empty means emit nothing; the exit code already says the
			// call succeeded.
			if f.Format == "quiet" {
				return nil
			}
			fmt.Printf("Panel %s/%s declares no actions.\n", slug, panel)
			fmt.Println("Actions are declared in the page document, never at click time — see docs/cli/page.")
			return nil
		}
		printPageActions(slug, panel, doc.Actions)
		return nil
	},
}

func printPageActions(slug, panel string, actions []pageActionJSON) {
	f := newFormatter()
	rows := make([][]string, 0, len(actions))
	for _, a := range actions {
		confirm := "—"
		if a.Confirm != nil {
			confirm = "yes"
		}
		rows = append(rows, []string{
			a.ID, pageDash(a.Kind), pageDash(a.Label),
			pageActionTargetLabel(a), confirm, pageActionInputLabel(a),
		})
	}
	// Column 0 is the action id, which is the argument `page action` takes —
	// so `page actions x/y -f quiet` feeds the next command directly.
	f.Table([]string{"ID", "KIND", "LABEL", "RUNS", "CONFIRM", "INPUTS"}, rows)
	if f.Format == "quiet" {
		return
	}
	fmt.Println()
	fmt.Printf("Dispatch one:  crewship page action %s/%s <id> [--input k=v]\n", slug, panel)
}

// pageActionTargetLabel says what the action does, in the vocabulary of the
// kind. A link shows its entity ref rather than a URL, because there is no URL
// — §8 rule 3 removed the field, and printing one would imply otherwise.
func pageActionTargetLabel(a pageActionJSON) string {
	switch a.Kind {
	case "call":
		return "routine/" + pageDash(a.Routine)
	case "link":
		if a.Ref != nil {
			return a.Ref.Kind + "/" + a.Ref.ID
		}
		return "—"
	case "toggle":
		if len(a.Target) > 0 {
			return "panels " + strings.Join(a.Target, ",")
		}
		return "—"
	default:
		return "client handler"
	}
}

func pageActionInputLabel(a pageActionJSON) string {
	if len(a.Inputs) == 0 {
		return "—"
	}
	names := make([]string, 0, len(a.Inputs))
	for _, in := range a.Inputs {
		if in.Required {
			names = append(names, in.Name+"*")
			continue
		}
		names = append(names, in.Name)
	}
	return strings.Join(names, ",")
}

// ── action (dispatch) ──────────────────────────────────────────────────────

var pageActionCmd = &cobra.Command{
	Use:   "action <slug>/<panel> <action-id>",
	Short: "Dispatch a panel's declared action",
	Long: `Dispatch one of a panel's declared actions.

  crewship page action fleet-201/sluzby restart-api
  crewship page action fleet-201/sluzby scale --input replicas=4

The action id is resolved against the page's STORED spec and the server
dispatches the routine named there. There is no way to name a routine on
this command line and there is no field for one on the wire: that is what
makes the page's declared list an allow-list rather than a suggestion.

--input is repeatable and takes k=v. Values are validated server-side
against the action's own declaration: an input the action did not declare
is refused rather than passed through, and a required one that is missing
is refused before anything runs.

The command returns as soon as the run is QUEUED, with a pending id. It
does not wait for the run to finish — watch it with 'crewship routine
runs' or on the page itself.

Every dispatch carries a locally generated Idempotency-Key, so a retried
command resolves to the original dispatch instead of starting a second
one. Pass --idempotency-key to pin it across processes; reusing a key
with DIFFERENT inputs is refused (409) rather than silently deduped.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		slug, panel, err := splitPagePanelRef(args[0])
		if err != nil {
			return err
		}
		actionID := strings.TrimSpace(args[1])
		if actionID == "" {
			return cli.WithExitCode(fmt.Errorf("an action id is required"), cli.ExitValidation)
		}
		raw, _ := cmd.Flags().GetStringArray("input")
		inputs, err := parsePageActionInputs(raw)
		if err != nil {
			return err
		}

		client, err := pageClient()
		if err != nil {
			return err
		}
		key, _ := cmd.Flags().GetString("idempotency-key")
		if strings.TrimSpace(key) == "" {
			// A UUIDv4 per logical click, the Stripe pattern §8b.3 names.
			key = uuid.NewString()
		}
		client = client.WithHeader("Idempotency-Key", key)

		path := fmt.Sprintf("/api/v1/pages/%s/panels/%s/actions/%s",
			pagePathEscape(slug), pagePathEscape(panel), pagePathEscape(actionID))
		resp, err := client.Post(path, map[string]any{"inputs": inputs})
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := pageCheckError(resp); err != nil {
			return err
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("read response: %w", err)
		}

		f := newFormatter()
		switch f.Format {
		case "json", "yaml", "ndjson":
			return pageEmitMachine(f, body, "{}")
		}
		printPageActionReceipt(resp, body, slug, panel, actionID)
		return nil
	},
}

// printPageActionReceipt says what was queued, and what it will run.
//
// It reports the routine the SERVER resolved, not one the caller supplied,
// because the caller supplied none. That line is the operator-visible half of
// §8b.2.
func printPageActionReceipt(resp *http.Response, body []byte, slug, panel, actionID string) {
	var r pageActionDispatchJSON
	if err := json.Unmarshal(body, &r); err != nil {
		fmt.Printf("Dispatched %s on %s/%s.\n", actionID, slug, panel)
		return
	}
	verb := "Queued"
	if r.Deduped {
		verb = "Already queued (same idempotency key)"
	} else if r.Coalesced {
		verb = "Coalesced into the dispatch already queued"
	}
	fmt.Printf("%s: %s on %s/%s runs routine/%s.\n",
		verb, actionID, slug, panel, pageDash(r.Routine))
	fmt.Printf("  pending: %s\n", pageDash(r.PendingID))
	if r.FireAt != "" {
		fmt.Printf("  fires:   %s\n", r.FireAt)
	}
	if resp.StatusCode == http.StatusAccepted && !r.Deduped {
		// Say that this returned before the run did, so nobody reads a clean
		// exit code as a successful run.
		fmt.Println("  This returned when the run was accepted, not when it finished.")
	}
}

// parsePageActionInputs turns repeated --input k=v into the map the body
// carries. Repeatable rather than comma-separated for the reason §11b decision
// 13 gives for --bind: a value may plausibly contain a comma.
func parsePageActionInputs(raw []string) (map[string]any, error) {
	out := map[string]any{}
	for _, kv := range raw {
		k, v, ok := strings.Cut(kv, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, cli.WithExitCode(fmt.Errorf(
				"--input takes k=v, got %q", kv), cli.ExitValidation)
		}
		if _, dup := out[k]; dup {
			return nil, cli.WithExitCode(fmt.Errorf(
				"--input %s was given twice; an input has one value", k), cli.ExitValidation)
		}
		// Sent as the string it was typed as. The server coerces it to the type
		// the action declared (number, boolean, select) and refuses what does
		// not fit — guessing here would mean a `--input version=1.10` silently
		// becoming a float on one side of the wire and a string on the other.
		out[k] = v
	}
	return out, nil
}

// splitPagePanelRef parses the <page>/<panel> address `page set` established.
func splitPagePanelRef(ref string) (slug, panel string, err error) {
	slug, panel, ok := strings.Cut(ref, "/")
	if !ok || strings.TrimSpace(slug) == "" || strings.TrimSpace(panel) == "" {
		return "", "", cli.WithExitCode(fmt.Errorf(
			"expected <page>/<panel>, got %q", ref), cli.ExitValidation)
	}
	return slug, panel, nil
}

func init() {
	pageActionCmd.Flags().StringArray("input", nil,
		"An input the action declared, as k=v. Repeatable.")
	pageActionCmd.Flags().String("idempotency-key", "",
		"Pin the dedupe key (default: a fresh UUID per invocation). Reusing one with different inputs is refused.")

	pageCmd.AddCommand(pageActionsCmd)
	pageCmd.AddCommand(pageActionCmd)
}
