package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// routineSkeleton is the default scaffold emitted by `routine init` with
// no --from. It is a minimal but valid DSL (passes `routine validate`):
// one agent_run step over one input. JSON has no comment syntax, so the
// pointer at `routine schema` / `routine validate` lives in the
// description field. The agent_slug is a placeholder the author must
// swap for a real slug in their crew.
const routineSkeleton = `{
  "dsl_version": "1.0",
  "name": "my-routine",
  "display_name": "My Routine",
  "description": "Scaffold from 'crewship routine init'. Run 'crewship routine schema' for the full DSL reference and 'crewship routine validate <file>' to check your edits. Replace agent_slug with a real agent in your author crew.",
  "inputs": [
    {
      "name": "topic",
      "type": "string",
      "required": true,
      "description": "Example input — reference it in a step as {{ inputs.topic }}"
    }
  ],
  "steps": [
    {
      "id": "summarize",
      "type": "agent_run",
      "agent_slug": "your-agent",
      "prompt": "Summarize the following topic: {{ inputs.topic }}"
    }
  ]
}
`

var routineInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Scaffold a new routine — a minimal skeleton or a clone of an existing one",
	Long: `Writes a starting routine DSL you can edit and then 'routine validate'
+ 'routine apply'.

  crewship routine init                          # minimal valid skeleton to stdout
  crewship routine init -o new.json              # ...to a file
  crewship routine init --from summarize-text -o new.json   # clone an existing routine's definition

--from clones the live definition of an existing routine (via the same
export path 'routine export' uses) as a valid, edit-ready starting point.
Without --from you get a one-step agent_run skeleton.

The output is a routine DSL (what 'routine validate' and 'routine apply'
consume), not a full export bundle.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		from, _ := cmd.Flags().GetString("from")
		outPath, _ := cmd.Flags().GetString("output")

		var payload []byte
		if from != "" {
			def, err := fetchRoutineDefinition(from)
			if err != nil {
				return err
			}
			payload = def
		} else {
			payload = []byte(routineSkeleton)
		}

		if outPath != "" {
			if err := os.WriteFile(outPath, payload, 0o644); err != nil {
				return fmt.Errorf("write %s: %w", outPath, err)
			}
			fmt.Fprintf(os.Stderr, "Wrote routine skeleton to %s\n", outPath)
			fmt.Fprintf(os.Stderr, "Next: edit it, then 'crewship routine validate %s' and 'crewship routine apply %s'.\n", outPath, outPath)
			return nil
		}
		_, err := os.Stdout.Write(payload)
		return err
	},
}

// fetchRoutineDefinition pulls the export bundle for slug and returns the
// pretty-printed DSL definition (bundle.pipeline.definition) — the shape
// 'routine validate' / 'routine apply' consume, not the whole bundle.
func fetchRoutineDefinition(slug string) ([]byte, error) {
	if err := requireAuth(); err != nil {
		return nil, err
	}
	if err := requireWorkspace(); err != nil {
		return nil, err
	}
	client := newAPIClient()
	ws := client.GetWorkspaceID()
	resp, err := client.Get(fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/export", ws, url.PathEscape(slug)))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return nil, err
	}

	var bundle struct {
		Pipeline struct {
			Definition json.RawMessage `json:"definition"`
		} `json:"pipeline"`
	}
	if err := cli.ReadJSON(resp, &bundle); err != nil {
		return nil, err
	}
	if len(bundle.Pipeline.Definition) == 0 {
		return nil, fmt.Errorf("routine %q export has no definition to clone", slug)
	}
	// Re-indent for a human-editable file, and append a trailing newline.
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, bundle.Pipeline.Definition, "", "  "); err != nil {
		// Fall back to the raw definition if it isn't re-indentable.
		return append([]byte(bundle.Pipeline.Definition), '\n'), nil
	}
	pretty.WriteByte('\n')
	return pretty.Bytes(), nil
}

func init() {
	routineInitCmd.Flags().String("from", "", "Clone an existing routine's definition by slug")
	routineInitCmd.Flags().StringP("output", "o", "", "Write to a file instead of stdout")
	pipelineCmd.AddCommand(routineInitCmd)
}
