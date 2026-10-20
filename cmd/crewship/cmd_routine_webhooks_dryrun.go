package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// webhookDryRunResult is `routine webhooks test`'s machine payload: what a
// sample call would hand the routine. Named so the json/yaml parity guard can
// hold it to the contract.
type webhookDryRunResult struct {
	Inputs              map[string]interface{} `json:"inputs" yaml:"inputs"`
	IgnoredTemplateKeys []string               `json:"ignored_template_keys" yaml:"ignored_template_keys"`
	Valid               bool                   `json:"valid" yaml:"valid"`
	Problem             string                 `json:"problem,omitempty" yaml:"problem,omitempty"`
}

var routineWebhooksTestCmd = &cobra.Command{
	Use:   "test <webhook_id>",
	Short: "Show what a sample call would hand the routine (starts nothing)",
	Long: `Check a webhook's mapping without running anything. Give a sample of the JSON
a sender would POST; the server builds the routine's inputs exactly as it would
for a real delivery (request data under event, raw and headers, your inputs
template layered on top), checks them against the routine's declared inputs, and
answers with the inputs and whether the routine would accept them.

No run is started, no signature is needed, and the signing secret is never
shown. Template keys that collide with event, raw or headers are dropped on a
real call too, and are listed here.

Examples:
  crewship routine webhooks test wh_123 --sample '{"action":"opened"}'
  crewship routine webhooks test wh_123 --sample @pull_request.json
  crewship routine webhooks test wh_123 --sample - --header X-GitHub-Event=pull_request < event.json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		spec, _ := cmd.Flags().GetString("sample")
		if spec == "" {
			return cli.WithExitCode(fmt.Errorf("--sample is required (inline JSON, @file, or - for stdin)"), cli.ExitValidation)
		}
		raw, err := pageReadPayload(spec)
		if err != nil {
			return err
		}
		if !json.Valid(raw) {
			return cli.WithExitCode(fmt.Errorf("--sample must be valid JSON"), cli.ExitValidation)
		}
		headers := map[string]string{}
		pairs, _ := cmd.Flags().GetStringArray("header")
		for _, p := range pairs {
			k, v, ok := strings.Cut(p, "=")
			if !ok || strings.TrimSpace(k) == "" {
				return cli.WithExitCode(fmt.Errorf("--header %q: want NAME=value", p), cli.ExitValidation)
			}
			headers[strings.TrimSpace(k)] = v
		}
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Post("/api/v1/workspaces/"+url.PathEscape(client.GetWorkspaceID())+
			"/pipeline-webhooks/"+url.PathEscape(args[0])+"/test",
			map[string]interface{}{"sample": json.RawMessage(raw), "headers": headers})
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out webhookDryRunResult
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			if out.Valid {
				fmt.Println("The routine would accept this call.")
			} else {
				fmt.Printf("The routine would refuse this call: %s\n", out.Problem)
			}
			if len(out.IgnoredTemplateKeys) > 0 {
				fmt.Printf("Ignored template keys (reserved for request data): %s\n", strings.Join(out.IgnoredTemplateKeys, ", "))
			}
			fmt.Println("Inputs:")
			keys := make([]string, 0, len(out.Inputs))
			for k := range out.Inputs {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				b, _ := json.Marshal(out.Inputs[k])
				fmt.Printf("  %s: %s\n", k, sanitizeTerminal(string(b)))
			}
		})
	},
}

func init() {
	routineWebhooksTestCmd.Flags().String("sample", "", "the JSON a sender would POST: inline, @file, or - for stdin (REQUIRED)")
	routineWebhooksTestCmd.Flags().StringArray("header", nil, "a request header the mapping reads, NAME=value (repeatable)")
	routineWebhooksCmd.AddCommand(routineWebhooksTestCmd)
}
