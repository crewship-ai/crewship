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
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/slides"
)

const selectedAgentOrigin = "http://127.0.0.1:9119"
const selectedAgentResponseBytes = 512 << 10

var selectedAgentSegment = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func newSlideAgentCommand() *cobra.Command { return newSlideAgentCommandWithTransport(nil) }

// The transport seam is trusted Go-only qualification; no flag, profile or
// environment variable can override the production origin or HTTP transport.
func newSlideAgentCommandWithTransport(transport http.RoundTripper) *cobra.Command {
	root := &cobra.Command{Use: "agent", Short: "Protected selected-text Slides adapter using the original run token", PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
		// Override the root profile/config loader entirely for this capability.
		for _, name := range []string{"server", "profile", "workspace", "server-allow-mismatch"} {
			if cmd.Flags().Changed(name) || cmd.InheritedFlags().Changed(name) {
				return errors.New("protected Slides adapter does not accept server/profile/workspace overrides")
			}
		}
		return nil
	}}
	for _, operation := range []string{"read", "replace"} {
		command := &cobra.Command{Use: operation + " <page-slug> <request-id>", Short: "Protected selected Slides " + operation, Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
			if !selectedAgentSegment.MatchString(args[0]) || !selectedAgentSegment.MatchString(args[1]) {
				return errors.New("strict Page slug and request identity required")
			}
			token := os.Getenv("CREWSHIP_AGENT_TOKEN")
			if !strings.HasPrefix(token, "agtv2.") || len(token) > 2048 || strings.ContainsAny(token, " \t\r\n") {
				return errors.New("original CREWSHIP_AGENT_TOKEN run capability required; no human profile fallback")
			}
			path := "/api/v1/pages/" + url.PathEscape(args[0]) + "/slides/requests/" + url.PathEscape(args[1])
			method := http.MethodGet
			var raw []byte
			if operation == "replace" {
				filename, _ := cmd.Flags().GetString("file")
				if filename == "" {
					return errors.New("--file is required; use - for stdin")
				}
				reader := cmd.InOrStdin()
				if filename != "-" {
					file, err := os.Open(filename)
					if err != nil {
						return errors.New("selected replacement file unavailable")
					}
					defer file.Close()
					reader = file
				}
				var err error
				raw, err = io.ReadAll(io.LimitReader(reader, slides.MaxSelectedReplacementBytes+1))
				if err != nil {
					return errors.New("selected replacement JSON unavailable")
				}
				input, err := slides.DecodeSelectedReplacement(raw)
				if err != nil {
					return err
				}
				if input.RequestID != args[1] {
					return errors.New("replacement request identity differs from original path")
				}
				method, path = http.MethodPost, path+"/replace"
			}
			req, err := http.NewRequestWithContext(cmd.Context(), method, selectedAgentOrigin+path, bytes.NewReader(raw))
			if err != nil {
				return errors.New("invalid protected Slides request")
			}
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			actualTransport := transport
			if actualTransport == nil {
				// Explicit Proxy:nil prevents HTTP_PROXY/HTTPS_PROXY from receiving
				// the bearer. This client never resolves a profile or server URL.
				owned := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 10 * time.Second}
				defer owned.CloseIdleConnections()
				actualTransport = owned
			}
			client := &http.Client{Transport: actualTransport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("protected Slides redirects are forbidden")
			}}
			response, err := client.Do(req)
			if err != nil {
				return errors.New("protected Slides sidecar request failed; preserve original request on retry")
			}
			defer response.Body.Close()
			data, err := io.ReadAll(io.LimitReader(response.Body, selectedAgentResponseBytes+1))
			if err != nil || len(data) > selectedAgentResponseBytes || !json.Valid(data) {
				return errors.New("bounded selected Slides JSON response required")
			}
			data = bytes.ReplaceAll(data, []byte(token), []byte("[REDACTED]"))
			if response.StatusCode < 200 || response.StatusCode >= 300 {
				if len(data) > 1024 {
					data = []byte(`{"error":"bounded selected Slides request failed"}`)
				}
				return fmt.Errorf("selected Slides HTTP%d: %s", response.StatusCode, data)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
			return err
		}}
		if operation == "replace" {
			command.Flags().String("file", "", "Exact replacement-only JSON; - reads stdin. Never refresh original admission")
		}
		root.AddCommand(command)
	}
	return root
}
