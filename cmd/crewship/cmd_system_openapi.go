package main

// `crewship system openapi` — fetch the server's generated OpenAPI spec.
//
// The spec has been served at GET /openapi.json since #1325, but only over
// HTTP. The CLI is the supported way to drive Crewship from an agent, and an
// agent that wants the API contract had to fall back to curl — which is exactly
// the "hand-roll HTTP because the CLI is missing a command" shortcut the ops
// contract rules out. This is that command.
//
// Deliberately raw passthrough: the spec is the artifact, so it goes to stdout
// byte-for-byte (pipe it into jq, a codegen tool, or a file). No table view.

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

var systemOpenAPICmd = &cobra.Command{
	Use:     "openapi",
	Aliases: []string{"spec"},
	Short:   "Print the server's generated OpenAPI spec (JSON) to stdout",
	Long: `Fetches GET /openapi.json from the target server and writes it to
stdout unchanged.

The spec is generated from the server's actual route registrations, so it
always matches the instance you are pointed at — paths, methods and path
parameters are exact. Bodies are derived from the handlers, not authored:
most operations carry a named schema, and where the generator could not
derive one it emits an unconstrained object.

The same document is browsable in a browser at <server>/openapi — the
instance renders it itself, so it needs no network access.

Examples:
  crewship system openapi > openapi.json
  crewship system openapi | jq '.paths | keys | length'
  crewship system openapi --out ./spec/openapi.json
`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		client := newAPIClient()
		// The spec is workspace-agnostic — it describes the whole instance and
		// is served unauthenticated at the mux root, outside /api/v1. The shared
		// client resolves the configured workspace slug on EVERY request and
		// hard-fails a miss (client.go resolveWorkspaceID), so a stale stored
		// workspace would break a command that never needed one:
		//
		//   $ crewship system openapi
		//   workspace not found: demo-cmruhne4 (check --workspace …)
		//
		// Clearing it skips resolution entirely — resolveWorkspaceID
		// short-circuits on an empty WorkspaceID — and drops the pointless
		// ?workspace_id= the injector would otherwise append.
		client.WorkspaceID = ""
		resp, err := client.Get("/openapi.json")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		// Guard the "SPA catch-all answered instead of the API" failure mode
		// that #1325 fixed: an index.html body is a 200 that looks fine but
		// carries no schema, and silently writing it to the operator's
		// openapi.json is worse than failing.
		if ct := resp.Header.Get("Content-Type"); ct != "" && !isJSONContentType(ct) {
			return fmt.Errorf("server returned %q instead of JSON for /openapi.json — "+
				"the spec route may not be registered on this build", ct)
		}

		out := os.Stdout
		if path, _ := cmd.Flags().GetString("out"); path != "" {
			f, err := os.Create(path)
			if err != nil {
				return fmt.Errorf("create %s: %w", path, err)
			}
			defer f.Close()
			if _, err := io.Copy(f, resp.Body); err != nil {
				return fmt.Errorf("write %s: %w", path, err)
			}
			cli.PrintSuccess(fmt.Sprintf("OpenAPI spec written to %s", path))
			return nil
		}
		if _, err := io.Copy(out, resp.Body); err != nil {
			return fmt.Errorf("write spec: %w", err)
		}
		return nil
	},
}

// isJSONContentType reports whether a Content-Type header names JSON, ignoring
// any ";charset=..." parameter.
func isJSONContentType(ct string) bool {
	base := strings.TrimSpace(strings.SplitN(ct, ";", 2)[0])
	return base == "application/json" || base == "application/openapi+json"
}

func init() {
	systemOpenAPICmd.Flags().String("out", "", "Write the spec to this file instead of stdout")
	systemCmd.AddCommand(systemOpenAPICmd)
}
