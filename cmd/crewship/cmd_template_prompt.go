package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// Prompt templates use JSON values in template_json; workflow templates use
// a JSON-encoded string with a different validation contract.
func newPromptTemplateCommand() *cobra.Command {
	root := &cobra.Command{Use: "prompt", Short: "Manage workspace prompt templates"}
	for _, action := range []string{"list", "get", "create", "update", "delete"} {
		action := action
		use, count := action, 0
		if action == "get" || action == "update" || action == "delete" {
			use += " <template-id>"
			count = 1
		}
		cmd := &cobra.Command{Use: use, Args: cobra.ExactArgs(count), Short: action + " a workspace prompt template", RunE: func(cmd *cobra.Command, args []string) error {
			id := ""
			if len(args) > 0 {
				var err error
				id, err = guestCLIPathSegment(args[0])
				if err != nil {
					return err
				}
			}
			var body json.RawMessage
			if action == "create" || action == "update" {
				file, _ := cmd.Flags().GetString("file")
				if strings.TrimSpace(file) == "" {
					return fmt.Errorf("--file is required; use - for stdin")
				}
				var r io.Reader = cmd.InOrStdin()
				if file != "-" {
					f, err := os.Open(file)
					if err != nil {
						return err
					}
					defer f.Close()
					r = f
				}
				data, err := io.ReadAll(io.LimitReader(r, (1<<20)+1))
				if err != nil {
					return err
				}
				if len(data) > 1<<20 {
					return fmt.Errorf("template request exceeds 1 MiB")
				}
				var object map[string]json.RawMessage
				if json.Unmarshal(data, &object) != nil || object == nil {
					return fmt.Errorf("template request must be a JSON object")
				}
				body = json.RawMessage(data)
			}
			if action == "delete" {
				yes, _ := cmd.Flags().GetBool("yes")
				if !yes {
					return fmt.Errorf("--yes is required to delete a template")
				}
			}
			client, err := requireAuthAndWorkspace()
			if err != nil {
				return err
			}
			var response *http.Response
			switch action {
			case "list":
				response, err = client.Get("/api/v1/templates")
			case "get":
				response, err = client.Get("/api/v1/templates/" + id)
			case "create":
				response, err = client.Post("/api/v1/templates", body)
			case "update":
				response, err = client.Patch("/api/v1/templates/"+id, body)
			case "delete":
				response, err = client.Delete("/api/v1/templates/" + id)
			}
			if err != nil {
				return err
			}
			return printPromptTemplateResponse(cmd, response)
		}}
		if action == "create" || action == "update" {
			cmd.Flags().String("file", "", "JSON request object; - reads stdin")
		}
		if action == "delete" {
			cmd.Flags().Bool("yes", false, "Confirm deletion")
		}
		root.AddCommand(cmd)
	}
	return root
}

func printPromptTemplateResponse(cmd *cobra.Command, response *http.Response) error {
	defer response.Body.Close()
	if err := cli.CheckError(response); err != nil {
		return err
	}
	if response.StatusCode == http.StatusNoContent {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, (10<<20)+1))
	if err != nil {
		return err
	}
	if len(raw) > 10<<20 {
		return fmt.Errorf("template response exceeds 10 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var body any
	if err = decoder.Decode(&body); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return fmt.Errorf("invalid template response")
	}
	var writeErr error
	err = resolvedFormatter(cmd).AutoHuman(body, func() {
		encoded, _ := json.Marshal(body)
		_, writeErr = fmt.Fprintln(cmd.OutOrStdout(), string(encoded))
	})
	if err != nil {
		return err
	}
	return writeErr
}

func init() { templateCmd.AddCommand(newPromptTemplateCommand()) }
