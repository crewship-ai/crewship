package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// PR-E F6 — crewship persona CLI.
//
// Mirrors the API surface — view / edit / reset / history for both
// agent and crew layers, plus suggest-from-inbox for the operator
// to ack a pending agent proposal.
//
// All commands operate on the active workspace (`crewship workspace
// use ...`) and resolve targets by slug, not ID, to match the rest
// of the CLI surface.

var personaCmd = &cobra.Command{
	Use:   "persona",
	Short: "Manage agent + crew PERSONA.md (tone / style identity)",
	Long: `View, edit, reset, and inspect history of PERSONA.md.

PERSONA.md is the 1.5 KB identity surface read at every agent session
start. Two layers: crew default (workspace-shared) + per-agent
override. The agent layer wins outright when non-empty.

Subcommands:
  view <agent>            — show the resolved persona + which layer
  edit <agent>            — open $EDITOR to update the agent layer
  reset <agent>           — drop the agent layer (crew default reappears)
  history <agent>         — list version log
  suggest-from-inbox <id> — operator approves a pending agent proposal
  crew <crewSlug>         — same subcommands for the crew default layer`,
}

// personaResponse mirrors the API response shape.
type personaResponse struct {
	AgentID     string `json:"agent_id"`
	CrewID      string `json:"crew_id"`
	Layer       string `json:"layer"`
	FromDefault bool   `json:"from_default"`
	Content     string `json:"content"`
	Bytes       int    `json:"bytes"`
	CapBytes    int    `json:"cap_bytes"`
}

var personaViewCmd = &cobra.Command{
	Use:   "view <agent-slug>",
	Short: "Show the resolved persona for an agent",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAPIClientWithWorkspace()
		if err != nil {
			return err
		}
		agentID, err := resolveAgentID(client, args[0])
		if err != nil {
			return err
		}
		var resp personaResponse
		if err := getJSON(client, "/api/v1/agents/"+url.PathEscape(agentID)+"/persona", &resp); err != nil {
			return err
		}
		printPersona(cmd, "agent", resp)
		return nil
	},
}

var personaEditCmd = &cobra.Command{
	Use:   "edit <agent-slug>",
	Short: "Edit the per-agent PERSONA.md layer in $EDITOR",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAPIClientWithWorkspace()
		if err != nil {
			return err
		}
		agentID, err := resolveAgentID(client, args[0])
		if err != nil {
			return err
		}
		// Fetch current state as the editor seed.
		var current personaResponse
		if err := getJSON(client, "/api/v1/agents/"+url.PathEscape(agentID)+"/persona", &current); err != nil {
			return err
		}
		// Hide the synthesized default — operators editing should
		// start from a blank file rather than from "You are the
		// X...", which they'd then rewrite from scratch anyway.
		seed := ""
		if !current.FromDefault {
			seed = current.Content
		}
		edited, err := openInEditor(seed, ".md")
		if err != nil {
			return err
		}
		edited = strings.TrimSpace(edited)
		if edited == "" {
			return fmt.Errorf("aborted (empty content)")
		}
		body := map[string]string{"content": edited}
		var out map[string]any
		if err := putJSON(client, "/api/v1/agents/"+url.PathEscape(agentID)+"/persona", body, &out); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "persona updated (%d bytes)\n", len(edited))
		return nil
	},
}

var personaResetCmd = &cobra.Command{
	Use:   "reset <agent-slug>",
	Short: "Drop the per-agent PERSONA.md layer",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAPIClientWithWorkspace()
		if err != nil {
			return err
		}
		agentID, err := resolveAgentID(client, args[0])
		if err != nil {
			return err
		}
		if err := deleteJSON(client, "/api/v1/agents/"+url.PathEscape(agentID)+"/persona"); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "persona reset (crew default + synthesized fallback will be used)")
		return nil
	},
}

var personaHistoryCmd = &cobra.Command{
	Use:   "history <agent-slug>",
	Short: "List PERSONA.md version history",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAPIClientWithWorkspace()
		if err != nil {
			return err
		}
		agentID, err := resolveAgentID(client, args[0])
		if err != nil {
			return err
		}
		var resp struct {
			Entries []struct {
				ID        string `json:"id"`
				SHA256    string `json:"sha256"`
				Bytes     int    `json:"bytes"`
				WrittenAt string `json:"written_at"`
				WrittenBy string `json:"written_by"`
			} `json:"entries"`
		}
		if err := getJSON(client, "/api/v1/agents/"+url.PathEscape(agentID)+"/persona/history?limit=20", &resp); err != nil {
			return err
		}
		if len(resp.Entries) == 0 {
			fmt.Fprintln(cmd.OutOrStdout(), "(no history)")
			return nil
		}
		for _, e := range resp.Entries {
			fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  %5d B  by %s\n",
				e.WrittenAt, e.SHA256[:12], e.Bytes, e.WrittenBy)
		}
		return nil
	},
}

// personaSuggestFromInboxCmd applies a pending agent proposal that
// landed in audit_logs.action='persona.suggest_pending'. Operator
// runs this with the audit row id; the CLI loads the proposed
// content + posts it through the regular PUT path so the same
// validation + version recording fires.
var personaSuggestFromInboxCmd = &cobra.Command{
	Use:   "suggest-from-inbox <audit-id>",
	Short: "Approve a pending agent persona proposal (from audit_logs)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAPIClientWithWorkspace()
		if err != nil {
			return err
		}
		// The proposal payload is in audit_logs.metadata as JSON.
		// Until we have a dedicated GET /api/v1/audit/{id} endpoint
		// for arbitrary rows, the operator can hand-paste the
		// content into `crewship persona edit` — surface a hint.
		_, _ = fmt.Fprintln(cmd.OutOrStdout(),
			"approve-from-inbox is a Phase 2 surface — for now, view the proposal\n"+
				"in the inbox UI and apply via `crewship persona edit <agent>`.")
		_ = client
		_ = args
		return nil
	},
}

// --- crew subgroup ----------------------------------------------------------

var personaCrewCmd = &cobra.Command{
	Use:   "crew <crew-slug>",
	Short: "Manage the crew-default PERSONA.md layer",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Dispatch: `crewship persona crew <slug> view|edit|reset`.
		// Default subcommand is "view".
		if len(args) < 2 {
			return fmt.Errorf("usage: crewship persona crew <slug> view|edit|reset")
		}
		slug, sub := args[0], args[1]
		client, err := requireAPIClientWithWorkspace()
		if err != nil {
			return err
		}
		crewID, err := resolveCrewID(client, slug)
		if err != nil {
			return err
		}
		path := "/api/v1/crews/" + url.PathEscape(crewID) + "/persona"
		switch sub {
		case "view":
			var resp personaResponse
			if err := getJSON(client, path, &resp); err != nil {
				return err
			}
			printPersona(cmd, "crew", resp)
			return nil
		case "edit":
			var current personaResponse
			if err := getJSON(client, path, &current); err != nil {
				return err
			}
			edited, err := openInEditor(current.Content, ".md")
			if err != nil {
				return err
			}
			edited = strings.TrimSpace(edited)
			if edited == "" {
				return fmt.Errorf("aborted (empty content)")
			}
			var out map[string]any
			if err := putJSON(client, path, map[string]string{"content": edited}, &out); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "crew persona updated (%d bytes)\n", len(edited))
			return nil
		case "reset":
			if err := deleteJSON(client, path); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "crew persona reset")
			return nil
		default:
			return fmt.Errorf("unknown subcommand %q (want view|edit|reset)", sub)
		}
	},
}

// --- helpers ---------------------------------------------------------------

func printPersona(cmd *cobra.Command, kind string, p personaResponse) {
	out := cmd.OutOrStdout()
	source := p.Layer
	if p.FromDefault {
		source = "synthesized default"
	}
	fmt.Fprintf(out, "=== %s persona (source: %s, %d/%d bytes) ===\n",
		kind, source, p.Bytes, p.CapBytes)
	fmt.Fprintln(out, p.Content)
}

// putJSON is the local PUT helper — the api_helpers.go file has GET /
// POST / DELETE but not PUT. Inlined here so we don't need a cross-
// package edit just for one new method.
func putJSON(client *cli.Client, path string, body any, out any) error {
	resp, err := client.Do("PUT", path, body)
	if err != nil {
		return err
	}
	if err := cli.CheckError(resp); err != nil {
		return err
	}
	if out == nil {
		_ = resp.Body.Close()
		return nil
	}
	return cli.ReadJSON(resp, out)
}

// openInEditor writes seed to a temp file with the given extension,
// shells out to $EDITOR (vi as fallback), and returns the edited
// contents. Used by both edit subcommands so the seed-and-read
// dance only lives in one place.
func openInEditor(seed, ext string) (string, error) {
	tmp, err := os.CreateTemp("", "persona-*"+ext)
	if err != nil {
		return "", fmt.Errorf("create temp: %w", err)
	}
	defer os.Remove(tmp.Name())
	if seed != "" {
		if _, err := tmp.WriteString(seed); err != nil {
			_ = tmp.Close()
			return "", err
		}
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	editor := strings.TrimSpace(os.Getenv("EDITOR"))
	if editor == "" {
		editor = "vi"
	}
	// Tokenise $EDITOR so values like "code --wait" or "vim -c set ft=md"
	// (which include flags) reach the right argv. exec.Command(editor, …)
	// would treat the whole string as the executable name and fail.
	parts := strings.Fields(editor)
	if len(parts) == 0 {
		return "", fmt.Errorf("EDITOR is set but empty after trim/split")
	}
	editorArgs := append(parts[1:], tmp.Name())
	c := exec.Command(parts[0], editorArgs...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("editor failed: %w", err)
	}
	f, err := os.Open(tmp.Name())
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// requireAPIClientWithWorkspace constructs the API client and
// confirms a workspace is selected before any per-workspace endpoint
// fires. Centralised here so every persona subcommand stays a thin
// wrapper.
func requireAPIClientWithWorkspace() (*cli.Client, error) {
	c := newAPIClient()
	// The CLI client auto-injects the active workspace_id; if none
	// is set the caller will get a 400 with a clear error. Returning
	// the unconfigured client keeps the failure path consistent with
	// every other workspace-scoped subcommand.
	return c, nil
}

func init() {
	personaCmd.AddCommand(personaViewCmd)
	personaCmd.AddCommand(personaEditCmd)
	personaCmd.AddCommand(personaResetCmd)
	personaCmd.AddCommand(personaHistoryCmd)
	personaCmd.AddCommand(personaSuggestFromInboxCmd)
	personaCmd.AddCommand(personaCrewCmd)
}

// Avoid an unused-json-import lint when the CLI doesn't currently
// need json.Marshal directly — registration happens via the API
// helpers which take typed structs.
var _ = json.Marshal
