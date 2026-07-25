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
parameters are exact. Request/response bodies use a generic placeholder
schema; the spec is a route contract, not a hand-authored data model.

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
		// Not under /api/v1 — the spec is mounted at the mux root so tooling
		// can find it at the conventional well-known path.
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
