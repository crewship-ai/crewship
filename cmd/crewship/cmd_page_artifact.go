package main

// GET  /api/v1/pages/{slug}/artifact           page artifact show
// POST /api/v1/pages/{slug}/artifact/revisions page artifact publish
// POST /api/v1/pages/{slug}/artifact/restore   page artifact restore
// POST /api/v1/pages                          page artifact create
// These use the ordinary human profile client, never an internal agent route.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

const pageArtifactMaxHTMLBytes = 10 << 20

func newPageArtifactCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "artifact", Short: "Create human-owned HTML artifacts and manage immutable revisions"}
	create := &cobra.Command{Use: "create <slug>", Short: "Create an empty human-owned page ready for an HTML artifact", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := cmd.Flags().GetString("name")
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("--name is required")
		}
		client, err := pageClient()
		if err != nil {
			return err
		}
		response, err := client.Post("/api/v1/pages", map[string]any{"slug": args[0], "name": strings.TrimSpace(name), "panels": []any{}, "allow_empty": true})
		if err != nil {
			return err
		}
		return printPageArtifactResponse(cmd, response)
	}}
	create.Flags().String("name", "", "Display name for the human-owned artifact page")
	show := &cobra.Command{Use: "show <slug>", Short: "Read artifact source and revision history as a JSON document", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		revision, _ := cmd.Flags().GetInt64("revision")
		if cmd.Flags().Changed("revision") && revision < 1 {
			return fmt.Errorf("--revision must be positive")
		}
		client, err := pageClient()
		if err != nil {
			return err
		}
		endpoint := "/api/v1/pages/" + pagePathEscape(args[0]) + "/artifact"
		if revision > 0 {
			endpoint += fmt.Sprintf("?revision=%d", revision)
		}
		response, err := client.Get(endpoint)
		if err != nil {
			return err
		}
		return printPageArtifactResponse(cmd, response)
	}}
	show.Flags().Int64("revision", 0, "Read a historical immutable revision; omit for the current head")
	publish := newPageArtifactWriteCommand(false)
	restore := newPageArtifactWriteCommand(true)
	cmd.AddCommand(create, show, publish, restore)
	return cmd
}

func newPageArtifactWriteCommand(restore bool) *cobra.Command {
	verb, short := "publish", "Save an HTML file as a new immutable artifact revision"
	if restore {
		verb, short = "restore", "Restore a historical artifact as a new immutable revision"
	}
	cmd := &cobra.Command{Use: verb + " <slug>", Short: short, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		base, _ := cmd.Flags().GetInt64("base-rev")
		if !cmd.Flags().Changed("base-rev") || base < 0 {
			return fmt.Errorf("--base-rev is required and must be nonnegative; use 0 for the first revision")
		}
		body := map[string]any{"base_rev": base}
		if restore {
			revision, _ := cmd.Flags().GetInt64("revision")
			if revision < 1 {
				return fmt.Errorf("--revision is required and must be positive")
			}
			body["revision"] = revision
		} else {
			file, _ := cmd.Flags().GetString("file")
			if strings.TrimSpace(file) == "" {
				return fmt.Errorf("--file is required; use - for stdin")
			}
			var reader io.Reader = cmd.InOrStdin()
			if file != "-" {
				f, err := os.Open(file)
				if err != nil {
					return err
				}
				defer f.Close()
				reader = f
			}
			content, err := io.ReadAll(io.LimitReader(reader, pageArtifactMaxHTMLBytes+1))
			if err != nil {
				return err
			}
			if len(content) > pageArtifactMaxHTMLBytes {
				return fmt.Errorf("artifact HTML exceeds 10 MiB")
			}
			if len(content) == 0 || !utf8.Valid(content) {
				return fmt.Errorf("artifact HTML must be nonempty UTF-8")
			}
			body["html"] = string(content)
		}
		mutation, _ := cmd.Flags().GetString("mutation-id")
		if cmd.Flags().Changed("mutation-id") && strings.TrimSpace(mutation) == "" {
			return fmt.Errorf("--mutation-id must not be empty")
		}
		if mutation == "" {
			mutation = uuid.NewString()
		}
		body["mutation_id"] = mutation
		client, err := pageClient()
		if err != nil {
			return err
		}
		var response *http.Response
		if restore {
			response, err = client.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/artifact/restore", body)
		} else {
			response, err = client.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/artifact/revisions", body)
		}
		if err != nil {
			return err
		}
		return printPageArtifactResponse(cmd, response)
	}}
	cmd.Flags().Int64("base-rev", -1, "Required current head revision; 0 creates the first artifact revision")
	cmd.Flags().String("mutation-id", "", "Optional stable identity for retrying this exact mutation; generated once when omitted")
	if restore {
		cmd.Flags().Int64("revision", 0, "Required historical revision to restore")
	} else {
		cmd.Flags().String("file", "", "HTML source file; - reads stdin")
	}
	return cmd
}

// Re-encode JSON even for human output: untrusted source stays escaped text,
// including terminal control characters, rather than executing or opening HTML.
func printPageArtifactResponse(cmd *cobra.Command, response *http.Response) error {
	defer response.Body.Close()
	if err := pageCheckError(response); err != nil {
		return err
	}
	const limit = 64 << 20 // JSON escaping can expand the 10 MiB source sixfold.
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return err
	}
	if len(data) > limit {
		return fmt.Errorf("artifact response exceeds limit")
	}
	var payload any
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}
	safe, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var writeErr error
	if err := resolvedFormatter(cmd).AutoHuman(payload, func() { _, writeErr = fmt.Fprintln(cmd.OutOrStdout(), string(safe)) }); err != nil {
		return err
	}
	return writeErr
}

func init() { pageCmd.AddCommand(newPageArtifactCommand()) }
