package main

// POST /api/v1/internal/documents/create doc create

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
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/documents"
	"github.com/spf13/cobra"
)

const selectedAgentOrigin = "http://127.0.0.1:9119"

func newDocCreateCommand() *cobra.Command { return newDocCreateCommandWithTransport(nil) }

// The optional transport is a Go-only test seam, never a user-controlled origin.
func newDocCreateCommandWithTransport(transport http.RoundTripper) *cobra.Command {
	command := &cobra.Command{Use: "create --file <authoring.json>", Short: "Create one private Docs for this chat run's explicit human request", Args: cobra.NoArgs, PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
		for _, name := range []string{"config", "server", "profile", "workspace", "server-allow-mismatch"} {
			if cmd.Flags().Changed(name) || cmd.InheritedFlags().Changed(name) {
				return errors.New("protected Docs creation does not accept config/server/profile/workspace overrides")
			}
		}
		return nil
	}, RunE: func(cmd *cobra.Command, _ []string) error {
		token := os.Getenv("CREWSHIP_AGENT_TOKEN")
		if !strings.HasPrefix(token, "agtv2.") || len(token) > 2048 || strings.ContainsAny(token, " \t\r\n") {
			return errors.New("original CREWSHIP_AGENT_TOKEN run capability required; no human profile fallback")
		}
		filename, _ := cmd.Flags().GetString("file")
		if filename == "" {
			return errors.New("--file is required; use - for stdin")
		}
		reader := cmd.InOrStdin()
		if filename != "-" {
			file, err := os.Open(filename)
			if err != nil {
				return errors.New("document authoring file unavailable")
			}
			defer file.Close()
			reader = file
		}
		raw, err := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
		if err != nil || len(raw) > 1<<20 || !utf8.Valid(raw) {
			return errors.New("document authoring must fit 1 MiB of valid UTF-8")
		}
		var request documents.CreationInput
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || (documents.CreationIntent{SubmissionID: request.MutationID}).Validate() != nil {
			return errors.New("one authoring object and stable mutation_id required")
		}
		if err := documents.ValidateAuthoring(request.Blocks); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(cmd.Context(), http.MethodPost, selectedAgentOrigin+"/api/v1/documents/create", bytes.NewReader(raw))
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
		client := &http.Client{Transport: actual, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("protected Docs redirects are forbidden")
		}}
		response, err := client.Do(req)
		if err != nil {
			return errors.New("protected Docs sidecar request failed; preserve the same authoring payload and mutation_id on retry")
		}
		defer response.Body.Close()
		data, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
		if err != nil || len(data) > 64<<10 || !json.Valid(data) {
			return errors.New("bounded Docs creation JSON response required")
		}
		data = bytes.ReplaceAll(data, []byte(token), []byte("[REDACTED]"))
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			if len(data) > 1024 {
				data = []byte(`{"error":"document creation denied or unavailable"}`)
			}
			return fmt.Errorf("Docs creation HTTP%d: %s", response.StatusCode, data)
		}
		var result any
		if json.Unmarshal(data, &result) != nil {
			return errors.New("invalid Docs creation JSON response")
		}
		formatter := cli.NewFormatter(cli.ResolveFormat(flagFormat, nil))
		formatter.Writer = cmd.OutOrStdout()
		return formatter.Machine(result)
	}}
	command.Flags().String("file", "", "Readable heading/paragraph/list/codeBlock JSON; - reads stdin. Keep the exact mutation_id and content on retry")
	return command
}
