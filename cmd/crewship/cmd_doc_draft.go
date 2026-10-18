package main

// GET /api/v1/internal/documents/draft doc draft view
// POST /api/v1/internal/documents/draft doc draft apply, doc draft checkpoint

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/sidecar"
)

func newDocDraftCommand() *cobra.Command { return newDocDraftCommandWithTransport(nil) }

// Transport injection is available to Go tests only, never command flags.
func newDocDraftCommandWithTransport(transport http.RoundTripper) *cobra.Command {
	root := &cobra.Command{Use: "draft", Short: "Checkpoint this live run's explicitly admitted new Document draft", Long: "Use view, then prepare once and retain the immutable packet. prepare is unsaved. apply commits text then pins it; a failed pin does not undo committed text. Retry the exact packet, never regenerate its preconditions. No existing Page or human-profile fallback.", PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
		for _, key := range []string{"config", "server", "profile", "workspace", "server-allow-mismatch"} {
			if cmd.Flags().Changed(key) || cmd.InheritedFlags().Changed(key) {
				return errors.New("protected Docs drafting does not accept config/server/profile/workspace overrides")
			}
		}
		return nil
	}}
	for _, op := range []string{"view", "prepare", "apply", "checkpoint"} {
		op := op
		command := &cobra.Command{Use: op, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			token := os.Getenv("CREWSHIP_AGENT_TOKEN")
			if !strings.HasPrefix(token, "agtv2.") || len(token) > 2048 || strings.ContainsAny(token, " \t\r\n") {
				return errors.New("original CREWSHIP_AGENT_TOKEN run capability required; no human profile fallback")
			}
			exchange := func(method string, body []byte) (int, []byte, error) {
				req, err := http.NewRequestWithContext(cmd.Context(), method, selectedAgentOrigin+"/api/v1/documents/draft", bytes.NewReader(body))
				if err != nil {
					return 0, nil, errors.New("invalid protected draft request")
				}
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("Content-Type", "application/json")
				actual := transport
				if actual == nil {
					owned := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 10 * time.Second}
					defer owned.CloseIdleConnections()
					actual = owned
				}
				client := &http.Client{Transport: actual, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
					return errors.New("protected Docs redirects are forbidden")
				}}
				response, err := client.Do(req)
				if err != nil {
					return 0, nil, errors.New("draft outcome unknown; preserve the exact packet for retry")
				}
				defer response.Body.Close()
				raw, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
				if err != nil || len(raw) > 64<<10 || !json.Valid(raw) {
					return 0, nil, errors.New("bounded draft JSON response required; retain the exact packet")
				}
				raw = bytes.ReplaceAll(raw, []byte(token), []byte("[REDACTED]"))
				return response.StatusCode, raw, nil
			}
			output := json.NewEncoder(cmd.OutOrStdout())
			if op == "view" {
				status, raw, err := exchange(http.MethodGet, nil)
				if err != nil {
					return err
				}
				if status < 200 || status >= 300 {
					return fmt.Errorf("draft view HTTP%d denied or unavailable", status)
				}
				var value json.RawMessage = raw
				return output.Encode(value)
			}
			file, _ := cmd.Flags().GetString("file")
			if file == "" {
				return errors.New("--file is required; retain the original packet on retry")
			}
			reader := cmd.InOrStdin()
			if file != "-" {
				f, err := os.Open(file)
				if err != nil {
					return errors.New("draft packet file unavailable")
				}
				defer f.Close()
				reader = f
			}
			raw, err := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
			if err != nil || len(raw) > 1<<20 {
				return errors.New("draft input exceeds 1 MiB")
			}
			if op == "prepare" {
				p, err := sidecar.PrepareDocumentDraft(raw)
				if err != nil {
					return err
				}
				return output.Encode(map[string]any{"status": "prepared_unsaved", "packet": p})
			}
			packet, err := sidecar.ParseDocumentDraftPacket(raw)
			if err != nil {
				return err
			}
			out, applyErr := sidecar.ApplyDocumentDraft(packet, op == "checkpoint", exchange)
			if err := output.Encode(out); err != nil {
				return err
			}
			return applyErr
		}}
		if op != "view" {
			command.Flags().String("file", "", "Original JSON input or immutable packet file; - reads stdin")
		}
		root.AddCommand(command)
	}
	return root
}
