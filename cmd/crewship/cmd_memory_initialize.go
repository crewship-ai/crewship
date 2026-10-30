package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// memoryInitializeMaxDocs is POST /api/v1/memory/initialize's batch cap
// (internal/api/memory_initialize.go). Checked here too so a mistyped batch
// fails before anything leaves the machine.
const memoryInitializeMaxDocs = 8

// memoryInitializeDoc is one document on the initialize wire.
type memoryInitializeDoc struct {
	Path string `json:"path" yaml:"path"`
	Body string `json:"body" yaml:"body"`
}

// parseMemoryInitializeDoc reads one --doc value: PATH=@FILE reads the body
// from a file, PATH=TEXT takes TEXT literally. The path is the memory file's
// name inside the scope (CREW.md, AGENT.md, daily/2026-10-29.md …); the server
// decides which names a scope accepts.
func parseMemoryInitializeDoc(value string) (memoryInitializeDoc, error) {
	path, body, ok := strings.Cut(value, "=")
	path = strings.TrimSpace(path)
	if !ok || path == "" {
		return memoryInitializeDoc{}, fmt.Errorf("--doc %q: want PATH=@FILE or PATH=TEXT", value)
	}
	if file, isFile := strings.CutPrefix(body, "@"); isFile {
		raw, err := os.ReadFile(file)
		if err != nil {
			return memoryInitializeDoc{}, fmt.Errorf("--doc %s: %w", path, err)
		}
		body = string(raw)
	}
	if strings.TrimSpace(body) == "" {
		return memoryInitializeDoc{}, fmt.Errorf("--doc %s: body is empty", path)
	}
	return memoryInitializeDoc{Path: path, Body: body}, nil
}

var memoryInitializeCmd = &cobra.Command{
	Use:   "initialize",
	Short: "Write first-time memory files for a crew or agent, never replacing existing ones",
	Long: `Provision a crew's or an agent's memory files before it has any: the
administrative first write. A file that already exists is left exactly as it
is and counted as existing, so running the same command twice changes nothing.

Backed by POST /api/v1/memory/initialize. Needs the OWNER or ADMIN role.

Each --doc is PATH=@FILE (body read from FILE) or PATH=TEXT. Between 1 and 8
documents per call. Accepted paths:

  crew scope (no --agent)   CREW.md (4 KiB), learned.md (8 KiB)
  agent scope (--agent)     AGENT.md (4 KiB), PERSONA.md (1.5 KiB), pins.md (8 KiB),
                            daily/YYYY-MM-DD.md (32 KiB)

The whole batch is validated — names, sizes, the memory content policy and the
credential scrubber — before any file is written; one rejected document
rejects the call.

Examples:
  crewship memory initialize --crew engineering --doc CREW.md=@./crew.md
  crewship memory initialize --crew engineering --agent alex \
    --doc AGENT.md=@./alex.md --doc pins.md=@./pins.md
  crewship memory initialize --crew engineering --doc learned.md=@learned.md --format json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		crewRef, _ := cmd.Flags().GetString("crew")
		if strings.TrimSpace(crewRef) == "" {
			return fmt.Errorf("--crew is required")
		}
		values, _ := cmd.Flags().GetStringArray("doc")
		if len(values) == 0 || len(values) > memoryInitializeMaxDocs {
			return fmt.Errorf("pass between 1 and %d --doc PATH=@FILE values (got %d)", memoryInitializeMaxDocs, len(values))
		}
		docs := make([]memoryInitializeDoc, 0, len(values))
		seen := map[string]bool{}
		for _, v := range values {
			doc, err := parseMemoryInitializeDoc(v)
			if err != nil {
				return err
			}
			if seen[doc.Path] {
				return fmt.Errorf("--doc %s given twice", doc.Path)
			}
			seen[doc.Path] = true
			docs = append(docs, doc)
		}
		agentSlug, _ := cmd.Flags().GetString("agent")

		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		crewID, err := resolveCrewID(client, crewRef)
		if err != nil {
			return err
		}
		body := map[string]any{"crew_id": crewID, "documents": docs}
		if agentSlug != "" {
			body["agent_slug"] = agentSlug
		}
		resp, err := client.Post("/api/v1/memory/initialize", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var result struct {
			Written  int `json:"written" yaml:"written"`
			Existing int `json:"existing" yaml:"existing"`
		}
		if err := cli.ReadJSON(resp, &result); err != nil {
			return err
		}
		if result.Written < 0 || result.Existing < 0 || result.Written+result.Existing != len(docs) {
			return fmt.Errorf("server acknowledged %d written + %d existing for %d documents", result.Written, result.Existing, len(docs))
		}
		return resolvedFormatter(cmd).AutoHuman(result, func() {
			scope := "crew " + crewRef
			if agentSlug != "" {
				scope = "agent " + agentSlug + " (crew " + crewRef + ")"
			}
			cli.PrintSuccess(fmt.Sprintf("Initialized memory for %s: %d written, %d already existed and were kept.", scope, result.Written, result.Existing))
		})
	},
}

func init() {
	memoryInitializeCmd.Flags().String("crew", "", "crew slug or id (required)")
	memoryInitializeCmd.Flags().String("agent", "", "agent slug; omit for the crew-shared tier")
	memoryInitializeCmd.Flags().StringArray("doc", nil, "memory document as PATH=@FILE or PATH=TEXT (1-8, repeatable)")
	memoryCmd.AddCommand(memoryInitializeCmd)
}
