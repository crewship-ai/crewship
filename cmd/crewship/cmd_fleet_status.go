package main

// Fleet-wide reads that had no CLI command (#2147): the agent-status
// histogram, per-agent load, and the crewshipd sidecar's own health probe.
//
// Naming: GET /api/v1/agents/crews-status is misleadingly named — despite
// "crews" in the path, AgentHandler.CrewsStatus (internal/api/agents.go)
// counts AGENTS by status, workspace-wide; it is not per-crew. 'crew status
// <slug>' already owns the per-crew, slug-scoped detail view (agents +
// assignments + escalations for ONE crew) and requires a positional arg, so
// reusing that name for a slug-free fleet aggregate would be confusing
// stacked on top of a wrong name. Both new agent commands live under `agent`
// instead, next to 'agent list' / 'agent get': `agent status` for the
// histogram, `agent load` for the per-agent breakdown behind it.
//
// GET /api/v1/crewshipd is a distinct daemon from the crewship API server:
// it is the sidecar that runs crew containers, reached over a Unix socket.
// 'system health' already means the API server's own health (uptime, log
// level, disk) — conflating the two under one name would misreport which
// process an operator is being told about. `system crewshipd` names the
// daemon the route names, using the same thin getJSON + emitFormattedJSON
// passthrough as its sibling 'system health' / 'system log-level'
// (cmd_system_observability.go), since the sidecar's /health payload is not
// owned by this repo and shouldn't be given a CLI-side type that can drift.

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// ---- agent status (fleet-wide agent-status histogram) ----

// agentStatusSummary mirrors AgentHandler.CrewsStatus's response struct
// (internal/api/agents.go) — GET /api/v1/agents/crews-status.
type agentStatusSummary struct {
	Total   int `json:"total" yaml:"total"`
	Running int `json:"running" yaml:"running"`
	Error   int `json:"error" yaml:"error"`
	Idle    int `json:"idle" yaml:"idle"`
	Queued  int `json:"queued" yaml:"queued"`
}

var agentStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Fleet-wide agent status counts (total / running / error / idle / queued)",
	Long: `Counts every agent in the workspace by status, plus the number of
QUEUED assignments waiting on one. This is the workspace toolbar snapshot
— for one agent or one crew's detail, see 'agent get <slug>' and
'crew status <slug>' instead.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		var s agentStatusSummary
		if err := getJSON(client, "/api/v1/agents/crews-status", &s); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(s, func() {
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "TOTAL\tRUNNING\tERROR\tIDLE\tQUEUED")
			fmt.Fprintf(w, "%d\t%d\t%d\t%d\t%d\n", s.Total, s.Running, s.Error, s.Idle, s.Queued)
			_ = w.Flush()
		})
	},
}

// ---- agent load (per-agent workload) ----

// agentLoadRow mirrors agentLoadEntry (internal/api/agents_query.go) — one
// row of GET /api/v1/agent-load.
type agentLoadRow struct {
	AgentID         string `json:"agent_id" yaml:"agent_id"`
	AgentName       string `json:"agent_name" yaml:"agent_name"`
	AgentSlug       string `json:"agent_slug" yaml:"agent_slug"`
	AgentStatus     string `json:"agent_status" yaml:"agent_status"`
	ActiveTasks     int    `json:"active_tasks" yaml:"active_tasks"`
	PendingTasks    int    `json:"pending_tasks" yaml:"pending_tasks"`
	CompletedToday  int    `json:"completed_today" yaml:"completed_today"`
	TokensUsedToday int    `json:"tokens_used_today" yaml:"tokens_used_today"`
	TokenBudget     int    `json:"token_budget" yaml:"token_budget"`
}

var agentLoadCmd = &cobra.Command{
	Use:   "load",
	Short: "Per-agent workload: active/pending tasks, tokens used today, budget",
	Long: `Every agent in the workspace with its live task counts (active,
pending), today's completions, and today's token usage vs budget — the
per-agent breakdown behind 'agent status'. 'agent get <slug>' has an
agent's static config; this is the live load view across the whole fleet.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		var rows []agentLoadRow
		if err := getJSON(client, "/api/v1/agent-load", &rows); err != nil {
			return err
		}
		if rows == nil {
			rows = []agentLoadRow{}
		}
		var flush tabFlush
		if err := resolvedFormatter(cmd).AutoHuman(rows, func() {
			if len(rows) == 0 {
				fmt.Println("No agents.")
				return
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "SLUG\tNAME\tSTATUS\tACTIVE\tPENDING\tDONE_TODAY\tTOKENS_TODAY\tBUDGET")
			for _, r := range rows {
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\n",
					r.AgentSlug, r.AgentName, r.AgentStatus, r.ActiveTasks, r.PendingTasks,
					r.CompletedToday, r.TokensUsedToday, r.TokenBudget)
			}
			flush.of(w)
		}); err != nil {
			return err
		}
		return flush.err
	},
}

// ---- system crewshipd (sidecar daemon health probe) ----

var systemCrewshipdCmd = &cobra.Command{
	Use:   "crewshipd",
	Short: "Probe the crewshipd sidecar daemon's health (uptime, connections)",
	Long: `Hits GET /api/v1/crewshipd, a thin proxy to the crewshipd sidecar's
own /health endpoint over its Unix socket. Distinct from 'system health',
which reports the crewship API SERVER's own health (uptime, log level,
disk) — this is the sidecar daemon that runs crew containers.

An unreachable sidecar answers {"status":"unreachable"} with HTTP 200
rather than failing the request, so check the "status" field rather than
relying on a non-zero exit code.

  crewship system crewshipd
  crewship system crewshipd -f json | jq .status`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		var body any
		if err := getJSON(client, "/api/v1/crewshipd", &body); err != nil {
			return err
		}
		return emitFormattedJSON(cmd, body)
	},
}

func init() {
	agentCmd.AddCommand(agentStatusCmd)
	agentCmd.AddCommand(agentLoadCmd)
	systemCmd.AddCommand(systemCrewshipdCmd)
}
