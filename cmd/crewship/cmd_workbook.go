package main

// POST /api/v1/workbooks workbook create
// GET /api/v1/pages/{slug}/workbook workbook read
// POST /api/v1/pages/{slug}/workbook/operations workbook import
// GET /api/v1/pages/{slug}/workbook/operations/{mutation} workbook status
// POST /api/v1/pages/{slug}/workbook/mandates workbook admit
// DELETE /api/v1/pages/{slug}/workbook/mandates/{mandate} workbook revoke
// POST /api/v1/internal/gridcraft workbook agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/appengine"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

func workbookInput(cmd *cobra.Command) ([]byte, error) {
	path, _ := cmd.Flags().GetString("file")
	if path == "" {
		return nil, errors.New("--file is required; retain the original packet on retry")
	}
	input := cmd.InOrStdin()
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		input = f
	}
	raw, err := io.ReadAll(io.LimitReader(input, appengine.MaxSnapshotBytes+16385))
	if err != nil || len(raw) > appengine.MaxSnapshotBytes+16384 || !json.Valid(raw) {
		return nil, errors.New("bounded workbook JSON packet required")
	}
	return raw, nil
}
func newWorkbookCommand() *cobra.Command { return newWorkbookCommandWithTransport(nil) }
func newWorkbookCommandWithTransport(transport http.RoundTripper) *cobra.Command {
	root := &cobra.Command{Use: "workbook", Short: "Experimental offline workbook import and targeted agent operations", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	for _, op := range []string{"read", "import", "status", "admit", "revoke"} {
		operation := op
		cmd := &cobra.Command{Use: operation + " <page-slug|@ID>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			if os.Getenv("CREWSHIP_AGENT_TOKEN") != "" {
				return errors.New("human workbook operations require a human credential; use workbook agent for an admitted run")
			}
			basePath := "/api/v1/pages/" + pagePathEscape(args[0]) + "/workbook"
			var body []byte
			var err error
			if operation == "import" || operation == "admit" {
				body, err = workbookInput(cmd)
				if err != nil {
					return err
				}
				if operation == "import" {
					var req appengine.Request
					if appengine.DecodeStrict(body, &req) != nil || req.Command.Kind != "import" {
						return errors.New("native workbook import packet required")
					}
				}
			}
			client, err := pageClient()
			if err != nil {
				return err
			}
			var response *http.Response
			switch operation {
			case "read":
				response, err = client.Get(basePath)
			case "status":
				response, err = client.Get(basePath + "/operations/" + url.PathEscape(args[1]))
			case "revoke":
				response, err = client.Delete(basePath + "/mandates/" + url.PathEscape(args[1]))
			case "import":
				response, err = client.Post(basePath+"/operations", json.RawMessage(body))
			case "admit":
				response, err = client.Post(basePath+"/mandates", json.RawMessage(body))
			}
			if err != nil {
				return err
			}
			return printPageArtifactResponse(cmd, response)
		}}
		if op == "status" || op == "revoke" {
			cmd.Use += " <operation-id>"
			cmd.Args = cobra.ExactArgs(2)
		}
		if op == "import" || op == "admit" {
			cmd.Flags().String("file", "", "Exact JSON request; - reads stdin. Preserve IDs and preconditions on retry")
		}
		root.AddCommand(cmd)
	}
	create := &cobra.Command{Use: "create <page-slug>", Short: "Create the empty personal private Page a workbook is imported into", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if os.Getenv("CREWSHIP_AGENT_TOKEN") != "" {
			return errors.New("human workbook operations require a human credential; use workbook agent for an admitted run")
		}
		name, _ := cmd.Flags().GetString("name")
		client, err := pageClient()
		if err != nil {
			return err
		}
		response, err := client.Post("/api/v1/workbooks", map[string]string{"slug": args[0], "name": name})
		if err != nil {
			return err
		}
		return printPageArtifactResponse(cmd, response)
	}}
	create.Flags().String("name", "", "Page name; repeat the same slug and name to retry safely")
	_ = create.MarkFlagRequired("name")
	root.AddCommand(create)
	agent := &cobra.Command{Use: "agent", Short: "Send a retained read/apply/status packet through the protected run sidecar", Args: cobra.NoArgs, PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
		for _, key := range []string{"config", "server", "profile", "workspace", "server-allow-mismatch"} {
			if cmd.Flags().Changed(key) || cmd.InheritedFlags().Changed(key) {
				return errors.New("protected workbook requests do not accept connection or profile overrides")
			}
		}
		return nil
	}, RunE: func(cmd *cobra.Command, _ []string) error {
		token := os.Getenv("CREWSHIP_AGENT_TOKEN")
		if !strings.HasPrefix(token, "agtv2.") || len(token) > 2048 || strings.ContainsAny(token, " \t\r\n") {
			return errors.New("original live run capability required; no human profile fallback")
		}
		raw, err := workbookInput(cmd)
		if err != nil {
			return err
		}
		request, err := http.NewRequestWithContext(cmd.Context(), http.MethodPost, selectedAgentOrigin+"/api/v1/gridcraft", bytes.NewReader(raw))
		if err != nil {
			return err
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		actual := transport
		if actual == nil {
			owned := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 10 * time.Second}
			defer owned.CloseIdleConnections()
			actual = owned
		}
		client := &http.Client{Transport: actual, Timeout: 35 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("workbook redirects are forbidden") }}
		response, err := client.Do(request)
		if err != nil {
			return errors.New("workbook outcome unknown; retain the exact packet")
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, appengine.MaxSnapshotBytes+16385))
		if err != nil || len(body) > appengine.MaxSnapshotBytes+16384 || !json.Valid(body) {
			return errors.New("bounded workbook response required")
		}
		body = bytes.ReplaceAll(body, []byte(token), []byte("[REDACTED]"))
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return fmt.Errorf("workbook HTTP%d denied or unavailable; retain the exact packet", response.StatusCode)
		}
		formatter := cli.NewFormatter(cli.ResolveFormat(flagFormat, nil))
		formatter.Writer = cmd.OutOrStdout()
		return formatter.Machine(cli.RawJSON(body))
	}}
	agent.Flags().String("file", "", "Retained agent read/apply/status JSON packet; - reads stdin")
	root.AddCommand(agent)
	return root
}
func init() { rootCmd.AddCommand(newWorkbookCommand()) }
