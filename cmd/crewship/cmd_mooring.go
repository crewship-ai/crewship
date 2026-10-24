package main

// GET /api/v1/mooring/apps mooring apps
// POST /api/v1/mooring/documents mooring create
// GET /api/v1/pages/{slug}/mooring mooring read
// POST /api/v1/pages/{slug}/mooring/commands mooring command
// POST /api/v1/pages/{slug}/mooring/mandates mooring admit
// DELETE /api/v1/pages/{slug}/mooring/mandates/{mandate} mooring revoke
// POST /api/v1/internal/mooring mooring agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/crewship-ai/crewship/internal/appengine"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

func newMooringCommand() *cobra.Command {
	root := &cobra.Command{Use: "mooring", Short: "Manifest-driven experimental application documents"}
	for _, op := range []string{"apps", "create", "read", "command", "admit", "revoke"} {
		operation := op
		cmd := &cobra.Command{Use: op, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			client, e := humanWorkbookClient()
			if e != nil {
				return e
			}

			var response *http.Response
			var raw []byte
			if operation == "create" || operation == "command" || operation == "admit" {
				raw, e = workbookInput(cmd)
				if e != nil {
					return e
				}
			}
			switch operation {
			case "apps":
				response, e = client.Get("/api/v1/mooring/apps")
			case "read":
				response, e = client.Get("/api/v1/pages/" + pagePathEscape(args[0]) + "/mooring")
			case "create":
				response, e = client.Post("/api/v1/mooring/documents", json.RawMessage(raw))
			case "command":
				response, e = client.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/mooring/commands", json.RawMessage(raw))
			case "admit":
				response, e = client.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/mooring/mandates", json.RawMessage(raw))
			case "revoke":
				response, e = client.Delete("/api/v1/pages/" + pagePathEscape(args[0]) + "/mooring/mandates/" + url.PathEscape(args[1]))
			}

			if e != nil {
				return e
			}
			return printPageArtifactResponse(cmd, response)
		}}
		if op == "apps" || op == "create" {
			cmd.Args = cobra.NoArgs
		} else {
			cmd.Use += " <page-slug|@ID>"
		}
		if op == "revoke" {
			cmd.Args = cobra.ExactArgs(2)
			cmd.Use += " <mandate>"
		}
		if op == "create" || op == "command" || op == "admit" {
			cmd.Flags().String("file", "", "Exact JSON packet; - reads stdin")
		}
		root.AddCommand(cmd)
	}
	agent := &cobra.Command{Use: "agent", Args: cobra.NoArgs, Short: "Run an explicitly admitted generic application command", RunE: func(cmd *cobra.Command, _ []string) error {
		token := os.Getenv("CREWSHIP_AGENT_TOKEN")
		if token == "" {
			return errors.New("live agent token required")
		}
		raw, e := workbookInput(cmd)
		if e != nil {
			return e
		}
		base := os.Getenv("CREWSHIP_SIDECAR_URL")
		if base == "" {
			base = "http://127.0.0.1:9119"
		}
		u, e := url.Parse(base)
		if e != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.RawQuery != "" || u.Path != "" || u.User != nil || u.Fragment != "" {
			return errors.New("loopback sidecar URL required")
		}
		req, e := http.NewRequestWithContext(cmd.Context(), "POST", base+"/api/v1/mooring", bytes.NewReader(raw))
		if e != nil {
			return e
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		transport := &http.Transport{Proxy: nil}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, Timeout: 35 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }}
		response, e := client.Do(req)
		if e != nil {
			return e
		}
		defer response.Body.Close()
		body, e := io.ReadAll(io.LimitReader(response.Body, appengine.MaxSnapshotBytes+16385))
		if e != nil || len(body) > appengine.MaxSnapshotBytes+16384 || !json.Valid(body) {
			return errors.New("invalid Mooring response")
		}
		if response.StatusCode != 200 {
			return fmt.Errorf("Mooring HTTP%d; retain exact packet", response.StatusCode)
		}
		formatter := cli.NewFormatter(cli.ResolveFormat(flagFormat, nil))
		formatter.Writer = cmd.OutOrStdout()
		return formatter.Machine(cli.RawJSON(bytes.ReplaceAll(body, []byte(token), []byte("[REDACTED]"))))
	}}
	agent.Flags().String("file", "", "Retained agent packet; - reads stdin")
	root.AddCommand(agent)
	return root
}
func init() { rootCmd.AddCommand(newMooringCommand()) }
