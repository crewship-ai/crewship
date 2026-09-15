package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// memoryInventoryCmd lists the CURRENT knowledge documents of one agent or one
// crew through the server (GET /api/v1/agents/{agentId}/memory and
// GET /api/v1/crews/{crewId}/memory). It is the CLI half of the crew Memory
// tab: the same read, over the same route, with the same per-scope states.
//
// It sits next to `memory versions` rather than `memory search` because it is
// a server read, not a local FTS query — the local commands only work on the
// host that holds the .memory directories, this one works from any machine
// with a login token.
var memoryInventoryCmd = &cobra.Command{
	Use:   "inventory",
	Short: "List an agent's or a crew's current knowledge documents (server API)",
	Long: `List the current knowledge documents the server can read for one agent or
one crew: AGENT.md / CREW.md / BRIEF.md / LEAD.md, pins, lessons, learned-*
notes and daily notes. Personal user and peer files are never included, and
PERSONA.md has its own persona commands.

--agent reads the agent's own documents plus its crew's shared ones; --crew
reads the crew's shared documents. Both add workspace-wide documents when the
server has a workspace memory root configured. Exactly one of the two is
required.

Each scope reports a state alongside its documents:

  available     the directory was read and its documents are listed
  empty         the directory does not exist or holds no knowledge document
  unavailable   the directory could not be read, or holds more entries than
                the server is willing to inventory (it refuses a partial list)

A document's own state is 'available' or 'unavailable' — an unreadable or
oversized file is listed with no content rather than as empty content. The
table shows one row per document; --format json carries the server's full
response, including each readable document's content and its history_path
for 'crewship memory versions list'.

Examples:
  crewship memory inventory --agent martin
  crewship memory inventory --crew backend
  crewship memory inventory --agent martin --format json | jq -r '.documents[] | select(.id=="agent:AGENT.md") | .content'`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		agentRef, _ := cmd.Flags().GetString("agent")
		crewRef, _ := cmd.Flags().GetString("crew")
		if (agentRef == "") == (crewRef == "") {
			return fmt.Errorf("exactly one of --agent and --crew is required")
		}
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		var path string
		if agentRef != "" {
			agentID, err := resolveAgentID(client, agentRef)
			if err != nil {
				return err
			}
			path = "/api/v1/agents/" + agentID + "/memory"
		} else {
			crewID, err := resolveCrewID(client, crewRef)
			if err != nil {
				return err
			}
			path = "/api/v1/crews/" + crewID + "/memory"
		}
		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out memoryInventory
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"ID", "STATE", "BYTES", "UPDATED", "REVISION"}
		rows := make([][]string, 0, len(out.Documents))
		for _, d := range out.Documents {
			size := ""
			if d.Bytes != nil {
				size = strconv.FormatInt(*d.Bytes, 10)
			}
			rows = append(rows, []string{d.ID, d.State, size, d.UpdatedAt, truncateID(d.Revision, 12)})
		}
		// The scope line goes above the table in human output only: it is
		// what tells "no rows because nothing is written" from "no rows
		// because the server could not look", which the table alone cannot.
		// Machine formats carry `scopes` in the envelope; quiet is for pipes.
		if f.RoutesToHuman() && f.Format != "quiet" {
			fmt.Println(out.scopeLine())
		}
		return f.Auto(out, headers, rows)
	},
}

// memoryInventory mirrors the inventory envelope of both memory routes
// (internal/api/memory_inventory.go). Every field is on the struct so that
// --format json re-marshals what the server sent, content included.
type memoryInventory struct {
	Source         string                    `json:"source" yaml:"source"`
	Scopes         map[string]string         `json:"scopes" yaml:"scopes"`
	Documents      []memoryInventoryDocument `json:"documents" yaml:"documents"`
	PeerGeneration string                    `json:"peer_generation" yaml:"peer_generation"`
}

type memoryInventoryDocument struct {
	ID          string `json:"id" yaml:"id"`
	Name        string `json:"name" yaml:"name"`
	Scope       string `json:"scope" yaml:"scope"`
	Content     string `json:"content,omitempty" yaml:"content,omitempty"`
	State       string `json:"state" yaml:"state"`
	Bytes       *int64 `json:"bytes" yaml:"bytes"`
	Revision    string `json:"revision,omitempty" yaml:"revision,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty" yaml:"updated_at,omitempty"`
	HistoryPath string `json:"history_path,omitempty" yaml:"history_path,omitempty"`
}

// scopeLine renders the per-scope states in a fixed order so the line is
// greppable: `Scopes: agent=available crew=empty workspace=unavailable`.
func (m memoryInventory) scopeLine() string {
	names := make([]string, 0, len(m.Scopes))
	for name := range m.Scopes {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+"="+m.Scopes[name])
	}
	return "Scopes: " + strings.Join(parts, " ")
}

func init() {
	memoryInventoryCmd.Flags().String("agent", "", "agent slug or id: its own documents plus its crew's shared ones")
	memoryInventoryCmd.Flags().String("crew", "", "crew slug or id: the crew's shared documents")
	memoryCmd.AddCommand(memoryInventoryCmd)
}
