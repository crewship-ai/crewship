package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/spf13/cobra"
)

// Artifacts use the same authenticated Page transport as the editor. A conflict
// is returned unchanged; the CLI never rereads and silently overwrites a head.
func newPageArtifactCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "artifact", Short: "Create, version and discuss a multi-screen Page artifact"}
	for _, action := range []string{"create", "publish"} {
		action := action
		child := &cobra.Command{Use: action + " <slug>", Args: cobra.ExactArgs(1), Short: "Save a complete atomic file manifest", RunE: func(cmd *cobra.Command, args []string) error {
			body, err := readArtifactCLIManifest(cmd)
			if err != nil {
				return err
			}
			endpoint := "/api/v1/pages/" + pagePathEscape(args[0]) + "/artifact/versions"
			if action == "create" {
				name, _ := cmd.Flags().GetString("name")
				if name == "" {
					return fmt.Errorf("--name is required")
				}
				body["slug"], body["name"] = args[0], name
				endpoint = "/api/v1/pages/artifacts"
			} else {
				base, _ := cmd.Flags().GetInt64("base-version")
				if base < 1 || !cmd.Flags().Changed("base-version") {
					return fmt.Errorf("--base-version must be the exact positive latest version; use create for a new project")
				}
				body["base_version"] = base
			}
			return artifactCLIRequest(cmd, "POST", endpoint, body)
		}}
		child.Flags().String("file", "", "JSON complete manifest {entrypoint,files,message}; - reads stdin")
		if action == "create" {
			child.Flags().String("name", "", "Artifact title")
		} else {
			child.Flags().Int64("base-version", -1, "Exact latest version read before editing")
		}
		cmd.AddCommand(child)
	}
	for _, action := range []string{"get", "versions", "comments"} {
		action := action
		child := &cobra.Command{Use: action + " <slug>", Args: cobra.ExactArgs(1), Short: "Read artifact content, retained versions or anchored comments", RunE: func(cmd *cobra.Command, args []string) error {
			endpoint := "/api/v1/pages/" + pagePathEscape(args[0]) + "/artifact"
			if action == "comments" {
				endpoint += "/comments"
			}
			if action != "versions" {
				version, _ := cmd.Flags().GetInt64("version")
				if version < 0 {
					return fmt.Errorf("version must be nonnegative")
				}
				if version > 0 {
					endpoint += "?version=" + strconv.FormatInt(version, 10)
				}
			}
			return artifactCLIRequest(cmd, "GET", endpoint, nil)
		}}
		if action != "versions" {
			child.Flags().Int64("version", 0, "Exact retained version; 0 reads latest")
		}
		cmd.AddCommand(child)
	}
	for _, action := range []string{"restore", "share"} {
		action := action
		child := &cobra.Command{Use: action + " <slug>", Args: cobra.ExactArgs(1), Short: "Restore a retained version as new, or pin the shared version", RunE: func(cmd *cobra.Command, args []string) error {
			base, _ := cmd.Flags().GetInt64("base-version")
			version, _ := cmd.Flags().GetInt64("version")
			if base < 1 || version < 1 {
				return fmt.Errorf("--base-version and --version are required positive versions")
			}
			return artifactCLIRequest(cmd, "POST", "/api/v1/pages/"+pagePathEscape(args[0])+"/artifact/"+action, map[string]any{"base_version": base, "version": version})
		}}
		child.Flags().Int64("base-version", 0, "Exact latest version read before the operation")
		child.Flags().Int64("version", 0, "Retained version to restore or share")
		cmd.AddCommand(child)
	}
	reply := &cobra.Command{Use: "reply <slug> <comment-id>", Args: cobra.ExactArgs(2), Short: "Reply to a version-bound artifact comment", RunE: func(cmd *cobra.Command, args []string) error {
		text, _ := cmd.Flags().GetString("text")
		version, _ := cmd.Flags().GetInt64("version")
		if text == "" || version < 1 {
			return fmt.Errorf("--text and positive --version are required")
		}
		clarification, _ := cmd.Flags().GetBool("needs-clarification")
		return artifactCLIRequest(cmd, "POST", "/api/v1/pages/"+pagePathEscape(args[0])+"/artifact/comments/"+pagePathEscape(args[1])+"/reply", map[string]any{"text": text, "version": version, "needs_clarification": clarification})
	}}
	reply.Flags().Bool("needs-clarification", false, "Ask for a fresh selection without claiming an edit")
	reply.Flags().String("text", "", "Describe the change or ask for a fresh selection")
	reply.Flags().Int64("version", 0, "Version addressed by this reply")
	availability := &cobra.Command{Use: "availability", Args: cobra.NoArgs, Short: "Read whether this workspace enables artifact projects", RunE: func(cmd *cobra.Command, _ []string) error {
		return artifactCLIRequest(cmd, "GET", "/api/v1/pages/artifacts/availability", nil)
	}}
	consent := &cobra.Command{Use: "consent <run-id>", Args: cobra.ExactArgs(1), Short: "Authorize one exact private chat run as its human owner", RunE: func(cmd *cobra.Command, args []string) error {
		body := map[string]any{"run_id": args[0]}
		page, _ := cmd.Flags().GetString("page-id")
		if page != "" {
			body["page_id"] = page
		}
		return artifactCLIRequest(cmd, "POST", "/api/v1/pages/artifacts/consent", body)
	}}
	consent.Flags().String("page-id", "", "Optional immutable ID of one existing artifact project; omit to permit one new project")
	cmd.AddCommand(reply, availability, consent)
	return cmd
}

func readArtifactCLIManifest(cmd *cobra.Command) (map[string]any, error) {
	file, _ := cmd.Flags().GetString("file")
	if file == "" {
		return nil, fmt.Errorf("--file is required")
	}
	var reader io.Reader = cmd.InOrStdin()
	if file != "-" {
		f, err := os.Open(file)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		reader = f
	}
	raw, err := io.ReadAll(io.LimitReader(reader, (9<<20)+1))
	if err != nil || len(raw) > 9<<20 {
		return nil, fmt.Errorf("cannot read artifact manifest within 9 MiB")
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil || body == nil {
		return nil, fmt.Errorf("manifest must be a JSON object")
	}
	for key := range body {
		if key != "entrypoint" && key != "files" && key != "message" {
			return nil, fmt.Errorf("unknown manifest field %q", key)
		}
	}
	return body, nil
}

func init() { pageCmd.AddCommand(newPageArtifactCommand()) }
