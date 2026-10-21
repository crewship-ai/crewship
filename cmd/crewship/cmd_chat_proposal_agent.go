package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/chatentities"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// Agent commands use the existing Crewship executable and verified sidecar
// token, never a human CLI profile or a model-supplied chat/run identity.
func newAgentProposalCommand(edit bool) *cobra.Command {
	use := "propose"
	n := 0
	if edit {
		use = "revise <proposal-id>"
		n = 1
	}
	cmd := &cobra.Command{Use: use, Short: "Publish inert issue intent from a verified agent run", Args: cobra.ExactArgs(n), RunE: func(cmd *cobra.Command, args []string) error {
		endpoint, _ := cmd.Flags().GetString("sidecar")
		u, err := url.Parse(endpoint)
		if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
			return fmt.Errorf("--sidecar must be an HTTP loopback origin")
		}
		ip := net.ParseIP(u.Hostname())
		if u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return fmt.Errorf("--sidecar must use loopback")
		}
		token := os.Getenv("CREWSHIP_AGENT_TOKEN")
		if token == "" {
			return fmt.Errorf("CREWSHIP_AGENT_TOKEN is required; use human review commands for confirmation")
		}
		key, _ := cmd.Flags().GetString("idempotency-key")
		if strings.TrimSpace(key) == "" || len(key) > 200 {
			return fmt.Errorf("--idempotency-key is required (maximum 200 bytes)")
		}
		file, _ := cmd.Flags().GetString("file")
		if file == "" {
			return fmt.Errorf("--file is required")
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		var intent chatentities.ProposalCreate
		if err = json.Unmarshal(raw, &intent); err != nil {
			return err
		}
		body := map[string]any{"title": intent.Title, "items": intent.Items, "operation_key": key}
		path := "/chat/proposal/create"
		if edit {
			if err = validateEntityCLIIdentifier(args[0]); err != nil {
				return err
			}
			version, _ := cmd.Flags().GetInt64("version")
			if version < 1 || version > 1<<53-1 {
				return fmt.Errorf("--version must identify the reviewed draft")
			}
			body["expected_version"] = version
			path = "/chat/proposal/" + url.PathEscape(args[0]) + "/edit"
		}
		raw, err = json.Marshal(body)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(cmd.Context(), "POST", endpoint+path, bytes.NewReader(raw))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		var result map[string]any
		if err = cli.ReadJSON(resp, &result); err != nil {
			return err
		}
		return resolvedFormatter(cmd).Machine(result)
	}}
	cmd.Flags().String("sidecar", "http://127.0.0.1:9119", "Local sidecar HTTP origin (loopback only)")
	cmd.Flags().String("file", "", "JSON title and complete proposal items")
	cmd.Flags().String("idempotency-key", "", "Stable agent operation key; reuse for retries")
	if edit {
		cmd.Flags().Int64("version", 0, "Expected proposal version")
	}
	return cmd
}
func init() {
	for _, cmd := range chatCmd.Commands() {
		if cmd.Name() == "proposal" {
			cmd.AddCommand(newAgentProposalCommand(false), newAgentProposalCommand(true))
			return
		}
	}
}
