package main

// CLI counterpart to /api/v1/credentials/bindings and
// /api/v1/agents/{id}/credential-bindings (PRD-CREDENTIALS-V2 §2.5b).
//
// A binding is (scope, slot) → credential: which account lands in a container,
// under which environment variable. Before it existed, credentials.name was
// both the account's name and the env var, under a workspace-wide UNIQUE — so
// a workspace could hold exactly one GitHub account, because the second one
// would also have had to be called GH_TOKEN.
//
// Three commands, because there are three questions:
//
//	credential bind      — point a slot at an account, in one scope
//	credential bindings  — what is bound where
//	credential resolve   — what will THIS agent actually get, and which rule won
//
// `resolve` is the one worth having. "Which GitHub account is this agent
// pushing as?" previously had no answer short of starting the container and
// looking, which is a large part of why the fused name/env-var went unnoticed
// for as long as it did. It reports the mapping only — never a value.

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

type credBindingOut struct {
	ID             string  `json:"id"`
	CredentialID   string  `json:"credential_id"`
	CredentialName string  `json:"credential_name"`
	Scope          string  `json:"scope"`
	CrewID         *string `json:"crew_id"`
	AgentID        *string `json:"agent_id"`
	Slot           string  `json:"slot"`
	CreatedAt      string  `json:"created_at"`
}

type credResolvedSlotOut struct {
	Slot           string `json:"slot"`
	CredentialID   string `json:"credential_id"`
	CredentialName string `json:"credential_name"`
	Source         string `json:"source"`
}

// resolveBindingScope turns the --crew/--agent flags into the (scope, owner)
// pair the API expects. Workspace is the default because it is the scope with
// no owner to name; requiring an explicit --workspace flag would only make the
// common case longer.
func resolveBindingScope(client *cli.Client, crewRef, agentRef string) (scope, crewID, agentID string, err error) {
	switch {
	case crewRef != "" && agentRef != "":
		return "", "", "", fmt.Errorf("pass --crew or --agent, not both: a binding has exactly one scope")
	case crewRef != "":
		id, err := resolveCrewID(client, crewRef)
		if err != nil {
			return "", "", "", err
		}
		return "CREW", id, "", nil
	case agentRef != "":
		id, err := resolveAgentID(client, agentRef)
		if err != nil {
			return "", "", "", err
		}
		return "AGENT", "", id, nil
	default:
		return "WORKSPACE", "", "", nil
	}
}

var credBindCmd = &cobra.Command{
	Use:   "bind <credential-name-or-id> --slot <ENV_VAR>",
	Short: "Bind a credential to an environment variable slot in one scope",
	Long: `Bind a credential to an environment variable slot.

The credential's NAME is the account (github-acme). The SLOT is what the agent
reads (GH_TOKEN). Keeping them apart is what lets one workspace hold ten GitHub
accounts: ten crews can each bind GH_TOKEN to a different one.

Scope is workspace by default; --crew or --agent narrows it. When more than one
scope binds the same slot the most specific wins: agent, then crew, then
workspace.

Within one scope a slot points at exactly one credential. Binding a slot that is
already taken fails with a conflict rather than replacing the existing row —
silently repointing every agent in a crew at a different account is not
something a command should do on your behalf. Unbind first.`,
	Example: `  # Give every agent in the acme crew the acme bot's token as GH_TOKEN
  crewship credential bind github-acme --slot GH_TOKEN --crew acme

  # A second account in the same crew needs a slot of its own
  crewship credential bind github-acme-ro --slot GH_TOKEN_READONLY --crew acme

  # Workspace-wide default
  crewship credential bind npm-publish --slot NPM_TOKEN`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		slot := strings.TrimSpace(mustFlagString(cmd, "slot"))
		if slot == "" {
			return fmt.Errorf("--slot is required: it is the environment variable the agent reads")
		}

		client := newAPIClient()
		credID, err := resolveCredentialID(client, args[0])
		if err != nil {
			return err
		}
		scope, crewID, agentID, err := resolveBindingScope(client,
			mustFlagString(cmd, "crew"), mustFlagString(cmd, "agent"))
		if err != nil {
			return err
		}

		body := map[string]string{"credential_id": credID, "scope": scope, "slot": slot}
		if crewID != "" {
			body["crew_id"] = crewID
		}
		if agentID != "" {
			body["agent_id"] = agentID
		}
		resp, err := client.Post("/api/v1/credentials/bindings", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out credBindingOut
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}

		f := newFormatter()
		return f.AutoHuman(out, func() {
			fmt.Printf("Bound %s → %s (%s scope).\n", out.Slot, out.CredentialName, strings.ToLower(out.Scope))
			fmt.Printf("Agents pick this up on their next start.\n")
		})
	},
}

var credBindingsCmd = &cobra.Command{
	Use:     "bindings",
	Aliases: []string{"binding-list"},
	Short:   "List credential → slot bindings in the workspace",
	Long: `List credential bindings.

Each row is one (scope, slot) → credential claim. Filter with --crew, --agent or
--credential to narrow it. This shows what is CONFIGURED; to see what a
particular agent will actually receive after resolution, use:

  crewship credential resolve <agent>`,
	Example: `  crewship credential bindings
  crewship credential bindings --crew acme
  crewship credential bindings --format json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		q := url.Values{}
		if ref := mustFlagString(cmd, "crew"); ref != "" {
			id, err := resolveCrewID(client, ref)
			if err != nil {
				return err
			}
			q.Set("crew_id", id)
		}
		if ref := mustFlagString(cmd, "agent"); ref != "" {
			id, err := resolveAgentID(client, ref)
			if err != nil {
				return err
			}
			q.Set("agent_id", id)
		}
		if ref := mustFlagString(cmd, "credential"); ref != "" {
			id, err := resolveCredentialID(client, ref)
			if err != nil {
				return err
			}
			q.Set("credential_id", id)
		}

		path := "/api/v1/credentials/bindings"
		if len(q) > 0 {
			path += "?" + q.Encode()
		}
		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var env struct {
			Bindings []credBindingOut `json:"bindings"`
		}
		if err := cli.ReadJSON(resp, &env); err != nil {
			return err
		}

		f := newFormatter()
		return f.AutoHuman(env.Bindings, func() {
			if len(env.Bindings) == 0 {
				fmt.Println("No bindings. Credentials without one are delivered under their own name (the pre-binding behaviour).")
				return
			}
			rows := make([][]string, 0, len(env.Bindings))
			for _, b := range env.Bindings {
				rows = append(rows, []string{b.Slot, b.CredentialName, strings.ToLower(b.Scope), bindingOwner(b), b.ID})
			}
			f.Table([]string{"SLOT", "CREDENTIAL", "SCOPE", "OWNER", "ID"}, rows)
		})
	},
}

// bindingOwner renders the crew or agent a binding is scoped to. Ids, not
// names: the list endpoint returns what it stores, and a second round trip per
// row to prettify it would turn a list into an N+1.
func bindingOwner(b credBindingOut) string {
	switch {
	case b.CrewID != nil:
		return *b.CrewID
	case b.AgentID != nil:
		return *b.AgentID
	default:
		return "-"
	}
}

var credUnbindCmd = &cobra.Command{
	Use:   "unbind <binding-id>",
	Short: "Remove a credential → slot binding",
	Long: `Remove a binding by id (see ` + "`crewship credential bindings`" + `), or by naming the
slot and scope directly with --slot plus --crew/--agent.

Removing a binding frees the slot. If the credential is also linked to the crew
it reverts to being delivered under its own name — the pre-binding behaviour —
rather than disappearing.`,
	Example: `  crewship credential unbind clx123abc
  crewship credential unbind --slot GH_TOKEN --crew acme`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		bindingID := ""
		if len(args) == 1 {
			bindingID = args[0]
		}
		slot := strings.TrimSpace(mustFlagString(cmd, "slot"))
		if bindingID == "" {
			if slot == "" {
				return fmt.Errorf("pass a binding id, or --slot with the scope (--crew/--agent) that holds it")
			}
			scope, crewID, agentID, err := resolveBindingScope(client,
				mustFlagString(cmd, "crew"), mustFlagString(cmd, "agent"))
			if err != nil {
				return err
			}
			bindingID, err = lookupBindingID(client, scope, crewID, agentID, slot)
			if err != nil {
				return err
			}
		}

		resp, err := client.Delete("/api/v1/credentials/bindings/" + bindingID)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		fmt.Printf("Binding %s removed. Agents pick this up on their next start.\n", bindingID)
		return nil
	},
}

// lookupBindingID finds the single binding for (scope, owner, slot). The server
// guarantees at most one — that is the invariant — so a miss is a genuine "not
// bound here" and not an ambiguity we have to resolve.
func lookupBindingID(client *cli.Client, scope, crewID, agentID, slot string) (string, error) {
	// url.Values, not string concatenation: a slot is user input, and an
	// unescaped one would silently truncate the query at the first `&`.
	q := url.Values{"scope": {scope}, "slot": {slot}}
	if crewID != "" {
		q.Set("crew_id", crewID)
	}
	if agentID != "" {
		q.Set("agent_id", agentID)
	}
	resp, err := client.Get("/api/v1/credentials/bindings?" + q.Encode())
	if err != nil {
		return "", err
	}
	if err := cli.CheckError(resp); err != nil {
		return "", err
	}
	var env struct {
		Bindings []credBindingOut `json:"bindings"`
	}
	if err := cli.ReadJSON(resp, &env); err != nil {
		return "", err
	}
	if len(env.Bindings) == 0 {
		return "", cli.NotFoundf("no %s binding for slot %q", strings.ToLower(scope), slot)
	}
	return env.Bindings[0].ID, nil
}

var credResolveCmd = &cobra.Command{
	Use:   "resolve <agent-slug-or-id>",
	Short: "Show which credential fills each slot for one agent",
	Long: `Show the slot map an agent will actually boot with, and which rule produced it.

Sources, most specific first:

  agent_grant        an explicit per-agent assignment (crewship credential assign)
  agent_binding      a binding scoped to this agent
  crew_binding       a binding scoped to this agent's crew
  workspace_binding  a workspace-wide binding
  crew_link          no binding at all — delivered under the credential's own
                     name, which is the pre-binding behaviour

Values are never shown. This answers "which account", not "what is the secret".`,
	Example: `  crewship credential resolve deploy-bot
  crewship credential resolve deploy-bot --format json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		agentID, err := resolveAgentID(client, args[0])
		if err != nil {
			return err
		}
		resp, err := client.Get("/api/v1/agents/" + agentID + "/credential-bindings")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var env struct {
			AgentID string                `json:"agent_id"`
			Slots   []credResolvedSlotOut `json:"slots"`
		}
		if err := cli.ReadJSON(resp, &env); err != nil {
			return err
		}

		f := newFormatter()
		return f.AutoHuman(env, func() {
			if len(env.Slots) == 0 {
				fmt.Printf("Agent %s receives no credentials.\n", args[0])
				return
			}
			rows := make([][]string, 0, len(env.Slots))
			for _, s := range env.Slots {
				rows = append(rows, []string{s.Slot, s.CredentialName, s.Source})
			}
			f.Table([]string{"SLOT", "CREDENTIAL", "SOURCE"}, rows)
		})
	},
}

// mustFlagString reads a string flag, treating a lookup failure as empty. Every
// flag below is declared in this file, so a miss is a programming error the
// zero value already handles.
func mustFlagString(cmd *cobra.Command, name string) string {
	v, _ := cmd.Flags().GetString(name)
	return strings.TrimSpace(v)
}

func init() {
	credBindCmd.Flags().String("slot", "", "Environment variable the agent will read (e.g. GH_TOKEN)")
	credBindCmd.Flags().String("crew", "", "Scope the binding to one crew (slug or ID)")
	credBindCmd.Flags().String("agent", "", "Scope the binding to one agent (slug or ID)")

	credBindingsCmd.Flags().String("crew", "", "Only bindings scoped to this crew (slug or ID)")
	credBindingsCmd.Flags().String("agent", "", "Only bindings scoped to this agent (slug or ID)")
	credBindingsCmd.Flags().String("credential", "", "Only bindings for this credential (name or ID)")

	credUnbindCmd.Flags().String("slot", "", "Slot to unbind, when no binding id is given")
	credUnbindCmd.Flags().String("crew", "", "Crew that holds the slot (slug or ID)")
	credUnbindCmd.Flags().String("agent", "", "Agent that holds the slot (slug or ID)")

	credentialCmd.AddCommand(credBindCmd)
	credentialCmd.AddCommand(credBindingsCmd)
	credentialCmd.AddCommand(credUnbindCmd)
	credentialCmd.AddCommand(credResolveCmd)
}
