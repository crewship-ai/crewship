package main

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

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

const docAgentIntentBytes = 256 << 10
const docAgentResponseBytes = 4 << 20

func validateDocAgentOperations(raw []byte) error {
	var operations []json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if decoder.Decode(&operations) != nil || decoder.Decode(new(any)) != io.EOF || len(operations) < 1 || len(operations) > 256 {
		return errors.New("only an array of 1..256 structural operations is accepted")
	}
	for _, rawOp := range operations {
		var op struct {
			Op       string          `json:"op"`
			ID       string          `json:"id"`
			AfterID  string          `json:"after_id"`
			BeforeID string          `json:"before_id"`
			Node     json.RawMessage `json:"node"`
		}
		decoder := json.NewDecoder(bytes.NewReader(rawOp))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&op) != nil || decoder.Decode(new(any)) != io.EOF {
			return errors.New("structural operations cannot supply identity, hashes or preconditions")
		}
		validID := func(id string) bool { return selectedAgentSegment.MatchString(id) }
		switch op.Op {
		case "insert_after":
			if !validID(op.AfterID) || op.ID != "" || op.BeforeID != "" || len(op.Node) == 0 {
				return errors.New("insert_after requires after_id and a plain node")
			}
		case "delete":
			if !validID(op.ID) || op.AfterID != "" || op.BeforeID != "" || len(op.Node) != 0 {
				return errors.New("delete requires only a selected block ID")
			}
		case "move_before":
			if !validID(op.ID) || !validID(op.BeforeID) || op.ID == op.BeforeID || op.AfterID != "" || len(op.Node) != 0 {
				return errors.New("move_before requires two selected block IDs")
			}
		default:
			return errors.New("unsupported structural operation")
		}
	}
	return nil
}

func newDocAgentCommand() *cobra.Command { return newDocAgentCommandWithTransport(nil) }

// Fixed protected transport follows the established selected Slides adapter.
// This Go-only test seam is never an origin/profile/environment override.
func newDocAgentCommandWithTransport(transport http.RoundTripper) *cobra.Command {
	root := &cobra.Command{Use: "agent", Short: "Protected Docs structural adapter using the original run capability", PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
		for _, name := range []string{"server", "profile", "workspace", "server-allow-mismatch"} {
			if cmd.Flags().Changed(name) || cmd.InheritedFlags().Changed(name) {
				return errors.New("protected Docs adapter does not accept server/profile/workspace overrides")
			}
		}
		return nil
	}}
	for _, operation := range []string{"read", "apply", "checkpoint"} {
		command := &cobra.Command{Use: operation + " <page-slug>", Short: "Protected Docs " + operation, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			if !selectedAgentSegment.MatchString(args[0]) {
				return errors.New("strict Page slug required")
			}
			token := os.Getenv("CREWSHIP_AGENT_TOKEN")
			if !strings.HasPrefix(token, "agtv2.") || len(token) > 2048 || strings.ContainsAny(token, " \t\r\n") {
				return errors.New("original CREWSHIP_AGENT_TOKEN required; no human profile fallback")
			}
			method, path := http.MethodGet, "/api/v1/pages/"+url.PathEscape(args[0])+"/document/agent-read"
			var raw []byte
			if operation == "apply" {
				filename, _ := cmd.Flags().GetString("operations-file")
				if filename == "" {
					return errors.New("--operations-file required; - reads only operations from stdin")
				}
				reader := cmd.InOrStdin()
				if filename != "-" {
					file, err := os.Open(filename)
					if err != nil {
						return errors.New("original operations file unavailable")
					}
					defer file.Close()
					reader = file
				}
				var err error
				raw, err = io.ReadAll(io.LimitReader(reader, docAgentIntentBytes+1))
				if err != nil || len(raw) > docAgentIntentBytes {
					return errors.New("bounded operations JSON required")
				}
				if err = validateDocAgentOperations(raw); err != nil {
					return err
				}
				raw, err = json.Marshal(map[string]json.RawMessage{"operations": raw})
				if err != nil {
					return errors.New("invalid operations JSON")
				}
				method = http.MethodPost
				path = "/api/v1/pages/" + url.PathEscape(args[0]) + "/document/agent-apply"
			} else if operation == "checkpoint" {
				method = http.MethodPost
				path = "/api/v1/pages/" + url.PathEscape(args[0]) + "/document/agent-checkpoint"
				raw = []byte("{}")
			}

			req, err := http.NewRequestWithContext(cmd.Context(), method, selectedAgentOrigin+path, bytes.NewReader(raw))
			if err != nil {
				return errors.New("invalid protected Docs request")
			}
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			actual := transport
			if actual == nil {
				owned := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 10 * time.Second}
				defer owned.CloseIdleConnections()
				actual = owned
			}
			client := &http.Client{Transport: actual, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("protected Docs redirects forbidden") }}
			response, err := client.Do(req)
			if err != nil {
				return errors.New("protected Docs sidecar request failed; preserve original request on retry")
			}
			defer response.Body.Close()
			data, err := io.ReadAll(io.LimitReader(response.Body, docAgentResponseBytes+1))
			if err != nil || len(data) > docAgentResponseBytes || !json.Valid(data) {
				return errors.New("bounded Docs JSON response required")
			}
			data = bytes.ReplaceAll(data, []byte(token), []byte("[REDACTED]"))
			if response.StatusCode < 200 || response.StatusCode >= 300 {
				if len(data) > 1024 {
					data = []byte(`{"error":"bounded protected Docs request failed"}`)
				}
				return fmt.Errorf("protected Docs HTTP%d: %s", response.StatusCode, data)
			}
			var payload any
			if json.Unmarshal(data, &payload) != nil {
				return errors.New("invalid protected Docs JSON response")
			}
			formatter := cli.NewFormatter(cli.ResolveFormat(flagFormat, nil))
			formatter.Writer = cmd.OutOrStdout()
			return formatter.Machine(payload)
		}}
		if operation == "apply" {
			command.Flags().String("operations-file", "", "JSON array of operations only; - reads stdin. The tool supplies original identity and preconditions")
		}
		root.AddCommand(command)
	}
	return root
}
