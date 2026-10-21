package main

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

func newRoutineOriginBindingCommand() *cobra.Command {
	group := &cobra.Command{Use: "origin-binding", Short: "Inspect or clear a routine's chat authority binding (OWNER/ADMIN operator only)"}
	get := &cobra.Command{Use: "get <slug>", Short: "Inspect the current head and independent schedule bindings", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateEntityCLIIdentifier(args[0]); err != nil {
			return err
		}
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		var out map[string]any
		path := fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/origin-binding", url.PathEscape(client.GetWorkspaceID()), url.PathEscape(args[0]))
		if err = getJSON(client, path, &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).Machine(out)
	}}
	var expected, reason, draft string
	var schedules []string
	clear := &cobra.Command{Use: "clear <slug>", Short: "Audited clear of the exact reviewed head and selected schedule bindings", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateEntityCLIIdentifier(args[0]); err != nil {
			return err
		}
		if !cmd.Flags().Changed("expected-origin") || strings.TrimSpace(reason) == "" {
			return fmt.Errorf("--expected-origin and --reason are required; use null for an unbound head")
		}
		var expectedValue any = expected
		if expected == "null" {
			expectedValue = nil
		} else if expected == "" {
			return fmt.Errorf("expected origin must be an ID or null")
		}
		selected := []map[string]any{}
		for _, value := range schedules {
			id, binding, ok := strings.Cut(value, "=")
			if !ok || id == "" || binding == "" {
				return fmt.Errorf("--schedule expects ID=ORIGIN_ID or ID=null")
			}
			var origin any = binding
			if binding == "null" {
				origin = nil
			}
			selected = append(selected, map[string]any{"id": id, "expected_origin_id": origin})
		}
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		var out map[string]any
		path := fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/origin-binding", url.PathEscape(client.GetWorkspaceID()), url.PathEscape(args[0]))
		body := map[string]any{"action": "clear", "reason": reason, "expected_origin_id": expectedValue, "schedules": selected}
		if draft != "" {
			parts := strings.SplitN(draft, "=", 3)
			if len(parts) != 3 || parts[0] == "" || parts[2] == "" {
				return fmt.Errorf("--draft expects ID=REVISION=ORIGIN_ID or ID=REVISION=null")
			}
			revision, e := strconv.Atoi(parts[1])
			if e != nil || revision < 1 {
				return fmt.Errorf("draft revision must be positive")
			}
			var origin any = parts[2]
			if parts[2] == "null" {
				origin = nil
			}
			body["draft"] = map[string]any{"id": parts[0], "expected_revision": revision, "expected_origin_id": origin}
		}
		if err = postJSON(client, path, body, &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).Machine(out)
	}}
	clear.Flags().StringVar(&expected, "expected-origin", "", "Exact binding from origin-binding get; null if unbound")
	clear.Flags().StringVar(&draft, "draft", "", "Reviewed bound draft ID=REVISION=ORIGIN_ID (or null)")
	clear.Flags().StringVar(&reason, "reason", "", "Operator reason recorded atomically in audit")
	clear.Flags().StringArrayVar(&schedules, "schedule", nil, "Selected independent schedule ID=ORIGIN_ID or ID=null (repeatable)")
	group.AddCommand(get, clear)
	return group
}
func init() { pipelineCmd.AddCommand(newRoutineOriginBindingCommand()) }
