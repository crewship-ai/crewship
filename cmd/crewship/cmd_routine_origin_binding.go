package main

import (
	"fmt"
	"net/url"
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
	var expected, reason string
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
		if err = postJSON(client, path, map[string]any{"action": "clear", "reason": reason, "expected_origin_id": expectedValue, "schedules": selected}, &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).Machine(out)
	}}
	clear.Flags().StringVar(&expected, "expected-origin", "", "Exact binding from origin-binding get; null if unbound")
	clear.Flags().StringVar(&reason, "reason", "", "Operator reason recorded atomically in audit")
	clear.Flags().StringArrayVar(&schedules, "schedule", nil, "Selected independent schedule ID=ORIGIN_ID or ID=null (repeatable)")
	group.AddCommand(get, clear)
	return group
}
func init() { pipelineCmd.AddCommand(newRoutineOriginBindingCommand()) }
