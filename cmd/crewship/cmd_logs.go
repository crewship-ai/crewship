package main

import (
	"fmt"
	"os"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// defaultLogLines is the line count both log commands default to, and the
// value a non-positive --lines/--tail falls back to. It matches
// parsePagination's default for the route (internal/api/proxy.go).
const defaultLogLines = 100

var logsCmd = &cobra.Command{
	Use:   "logs <agent-slug>",
	Short: "View agent logs",
	Long: `View logs for an agent. Use --follow for live streaming.

Examples:
  crewship logs viktor
  crewship logs viktor --follow
  crewship logs viktor --lines 50`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeAgentSlug,
	RunE: func(cmd *cobra.Command, args []string) error {
		lines, _ := cmd.Flags().GetInt("lines")
		follow, _ := cmd.Flags().GetBool("follow")
		return runAgentLogs(cmd, args[0], lines, follow)
	},
}

// logEntry is one line of an agent's log, in the shape the API returns it.
//
// The handler (proxy.AgentLogs, internal/api/proxy.go) unwraps the sidecar's
// `logs` value and writes it as a bare JSON ARRAY — or an empty array when the
// agent has no crew or crewshipd is unreachable. It has never written an
// object, which is what `agent logs` believed until #2086.
type logEntry struct {
	Timestamp string `json:"ts" yaml:"ts"`
	Level     string `json:"level" yaml:"level"`
	Agent     string `json:"agent" yaml:"agent"`
	Event     string `json:"event" yaml:"event"`
	Content   string `json:"content" yaml:"content"`
}

// runAgentLogs is the ONE implementation behind both `crewship logs <agent>`
// and `crewship agent logs <agent>`.
//
// They used to be two independent readers of the same route and only this one
// was right (#2086). The other decoded the response into a map[string]any —
// so it failed on EVERY agent with "cannot unmarshal array into Go value of
// type map[string]interface {}" — sent its line count as `tail`, which
// parsePagination has never read (the parameter is `limit`), and printed the
// container's stdout to the terminal unsanitised. Three fixes for a duplicate
// nobody needed; the duplicate now delegates here instead.
//
// cmd is threaded through for resolvedFormatter: the format is a flag on the
// command that was actually invoked, and `agent logs` is a different command
// object from `logs`.
func runAgentLogs(cmd *cobra.Command, agentRef string, lines int, follow bool) error {
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}

	client := newAPIClient()

	// Resolve the agent to its id AND its crew. The crew is not optional: a
	// crewless agent has no container to read, and the handler answers that
	// case with an empty array, which is indistinguishable from "running,
	// nothing logged yet".
	//
	// getByRef is the shared slug-or-CUID reader every sibling command uses,
	// and using it is what makes this lookup correct rather than merely
	// similar (#2106). A CUID costs ONE request to /api/v1/agents/{id} — the
	// only agent lookup with no page ceiling — and a slug goes through
	// resolveAgentID, which owns the "Did you mean" and "Available:" hints.
	// The hand-rolled LIST scan that stood here did neither: it sent no
	// `limit`, so it saw the route's default first 100 rows and reported
	// "agent not found" for anything past them, and a typo outside
	// fuzzy.Nearest's threshold got a bare "agent not found" where every
	// other command lists what does exist.
	resp, agentID, err := getByRef(client, "/api/v1/agents/", agentRef, resolveAgentID)
	if err != nil {
		return err
	}
	var agent struct {
		CrewID *string `json:"crew_id"`
	}
	if err := cli.ReadJSON(resp, &agent); err != nil {
		return err
	}
	crewID := ""
	if agent.CrewID != nil {
		crewID = *agent.CrewID
	}
	if crewID == "" {
		return fmt.Errorf("agent has no crew (logs require a crew)")
	}

	if lines <= 0 {
		lines = defaultLogLines
	}

	// Fetch logs via proxy endpoint
	path := fmt.Sprintf("/api/v1/agents/%s/logs?crew_id=%s&limit=%d", agentID, crewID, lines)
	logResp, err := client.Get(path)
	if err != nil {
		return err
	}
	if err := cli.CheckError(logResp); err != nil {
		return err
	}

	var logEntries []logEntry
	if err := cli.ReadJSON(logResp, &logEntries); err != nil {
		return err
	}
	if logEntries == nil {
		logEntries = []logEntry{}
	}

	f := resolvedFormatter(cmd)

	// --follow turns the whole command into a stream, and a stream cannot
	// be a JSON array: the closing bracket would arrive when the follow
	// ends, which is never. So under --follow the BACKLOG is emitted in the
	// same NDJSON shape the live tail uses, one object per line — otherwise
	// stdout would be an array followed by loose objects, which is the very
	// defect this change exists to remove, in a new costume.
	//
	// Without --follow the result is a finite document and the array is
	// correct.
	machine := f.Format == "json" || f.Format == "yaml" || f.Format == "ndjson"
	if follow && machine {
		for _, l := range logEntries {
			if err := f.WriteStreamRow(l); err != nil {
				return err
			}
		}
		return logsFollow(f, client, agentID, agentRef)
	}

	// The machine formats carry the log entries as the server sent them:
	// the human rendering truncates content at 200 characters and strips
	// terminal escapes, both of which are protections for a terminal and
	// data loss for a parser. sanitizeTerminal exists because agent stdout
	// is untrusted and a rogue entry could smuggle ANSI/OSC sequences into
	// the operator's scrollback — a JSON string has no such hazard, and a
	// consumer that renders one to a terminal has to sanitise anyway.
	if err := f.AutoHuman(logEntries, func() {
		for _, l := range logEntries {
			ts := l.Timestamp
			if t, err := time.Parse(time.RFC3339Nano, l.Timestamp); err == nil {
				ts = t.Format("2006-01-02 15:04:05")
			}
			eventColor := ""
			switch l.Event {
			case "output":
				eventColor = cli.White
			case "error":
				eventColor = cli.Red
			default:
				eventColor = cli.Gray
			}
			fmt.Printf("%s%s%s %s[%s]%s %s\n",
				cli.Dim, ts, cli.Reset,
				eventColor, sanitizeTerminal(l.Event), cli.Reset,
				truncate(sanitizeTerminal(l.Content), 200))
		}
	}); err != nil {
		return err
	}

	if follow {
		return logsFollow(f, client, agentID, agentRef)
	}

	return nil
}

func logsFollow(f *cli.Formatter, client *cli.Client, agentID, agentSlug string) error {
	wsToken, err := cli.WSTokenFromServer(client)
	if err != nil {
		return fmt.Errorf("get WS token for follow: %w", err)
	}

	server := streamServerURL()
	ws, err := cli.NewWSClient(server, wsToken)
	if err != nil {
		return err
	}
	defer ws.Close()

	channel := "agent:" + agentID
	if err := ws.Subscribe(channel); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}

	fmt.Fprintf(os.Stderr, "%s[following logs for %s, Ctrl+C to stop]%s\n", cli.Dim, agentSlug, cli.Reset)

	for {
		msg, err := ws.ReadMessage()
		if err != nil {
			return nil
		}

		event, _ := cli.ParseChatEvent(msg)
		if event == nil {
			continue
		}

		now := time.Now()
		// A follow is a stream, so the machine rendering is one JSON object
		// per line rather than a document — a `-f json` array could only be
		// closed when the stream ends, which for `--follow` is never. NDJSON
		// is the shape every streaming consumer already expects, and it is
		// what `-f json` gets here for that reason.
		switch f.Format {
		case "json", "ndjson", "yaml":
			if err := f.WriteStreamRow(logEntry{
				Timestamp: now.Format(time.RFC3339Nano),
				Event:     event.Type,
				Content:   event.Content,
				Agent:     agentSlug,
			}); err != nil {
				return err
			}
			continue
		}
		// event.Type is a constrained enum from the journal but we
		// sanitise anyway so an unknown type from a future agent can't
		// smuggle ANSI escapes into the user's terminal. event.Content
		// is fully untrusted (agent stdout) and definitely needs it.
		fmt.Printf("%s%s%s [%s] %s\n",
			cli.Dim, now.Format("2006-01-02 15:04:05"), cli.Reset,
			sanitizeTerminal(event.Type),
			truncate(sanitizeTerminal(event.Content), 200))
	}
}

func init() {
	logsCmd.Flags().IntP("lines", "n", defaultLogLines, "Number of log lines")
	logsCmd.Flags().BoolP("follow", "F", false, "Stream logs in real-time")
}
