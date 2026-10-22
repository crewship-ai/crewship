package main

// POST /api/v1/workbooks workbook create
// GET /api/v1/pages/{slug}/workbook workbook read
// POST /api/v1/pages/{slug}/workbook/operations workbook import
// POST /api/v1/pages/{slug}/workbook/edits workbook edit
// GET /api/v1/pages/{slug}/workbook/operations/{mutation} workbook status
// POST /api/v1/pages/{slug}/workbook/mandates workbook admit
// DELETE /api/v1/pages/{slug}/workbook/mandates/{mandate} workbook revoke
// POST /api/v1/pages/{slug}/workbook/sessions workbook session open
// DELETE /api/v1/pages/{slug}/workbook/sessions/{session} workbook session close
// GET /api/v1/pages/{slug}/workbook/commits workbook commits
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
	"strconv"
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
	for _, op := range []string{"read", "import", "edit", "status", "admit", "revoke"} {
		operation := op
		cmd := &cobra.Command{Use: operation + " <page-slug|@ID>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			if os.Getenv("CREWSHIP_AGENT_TOKEN") != "" {
				return errors.New("human workbook operations require a human credential; use workbook agent for an admitted run")
			}
			basePath := "/api/v1/pages/" + pagePathEscape(args[0]) + "/workbook"
			var body []byte
			var err error
			if operation == "import" || operation == "admit" || operation == "edit" {
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
				if operation == "edit" {
					var edit appengine.Edit
					if appengine.DecodeStrict(body, &edit) != nil {
						return errors.New("workbook edit packet required")
					}
					if _, err = edit.Request(); err != nil {
						return fmt.Errorf("workbook edit needs session_id, mutation_id \"<session_id>:<seq>\" and 1-%d cell.set ops on one sheet", appengine.MaxEditCells)
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
			case "edit":
				response, err = client.Post(basePath+"/edits", json.RawMessage(body))
			}
			if err != nil {
				return err
			}
			if operation == "edit" && response.StatusCode == http.StatusConflict {
				return printWorkbookConflict(cmd, response)
			}
			return printPageArtifactResponse(cmd, response)
		}}
		if op == "status" || op == "revoke" {
			cmd.Use += " <operation-id>"
			cmd.Args = cobra.ExactArgs(2)
		}
		if op == "import" || op == "admit" || op == "edit" {
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
	agent.Flags().String("file", "", "Retained agent read/apply/status/commits JSON packet; - reads stdin")
	root.AddCommand(agent)
	root.AddCommand(newWorkbookSessionCommand(), newWorkbookCommitsCommand())
	return root
}

// humanWorkbookClient refuses agent credentials on human workbook commands.
func humanWorkbookClient() (*cli.Client, error) {
	if os.Getenv("CREWSHIP_AGENT_TOKEN") != "" {
		return nil, errors.New("human workbook operations require a human credential; use workbook agent for an admitted run")
	}
	return pageClient()
}

func newWorkbookSessionCommand() *cobra.Command {
	session := &cobra.Command{Use: "session", Short: "Open, renew or close a live workbook editor session", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	open := &cobra.Command{Use: "open <page-slug|@ID>", Short: "Open an editor session (or renew one with --renew)", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		renew, _ := cmd.Flags().GetString("renew")
		body := map[string]string{}
		if renew != "" {
			if !appengine.ValidSessionID(renew) {
				return cli.WithExitCode(errors.New("--renew needs a session ID of 1-128 characters from A-Z, a-z, 0-9, _ and -"), cli.ExitValidation)
			}
			body["session_id"] = renew
		}
		client, err := humanWorkbookClient()
		if err != nil {
			return err
		}
		response, err := client.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/workbook/sessions", body)
		if err != nil {
			return err
		}
		return printPageArtifactResponse(cmd, response)
	}}
	open.Flags().String("renew", "", "Renew this live session of yours (heartbeat) instead of opening a new one")
	closer := &cobra.Command{Use: "close <page-slug|@ID> <session-id>", Short: "Close one of your editor sessions; repeating it is safe", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		client, err := humanWorkbookClient()
		if err != nil {
			return err
		}
		response, err := client.Delete("/api/v1/pages/" + pagePathEscape(args[0]) + "/workbook/sessions/" + url.PathEscape(args[1]))
		if err != nil {
			return err
		}
		return printPageArtifactResponse(cmd, response)
	}}
	session.AddCommand(open, closer)
	return session
}

func newWorkbookCommitsCommand() *cobra.Command {
	commits := &cobra.Command{Use: "commits <page-slug|@ID>", Short: "List confirmed workbook revisions after --since", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		since, _ := cmd.Flags().GetInt64("since")
		limit, _ := cmd.Flags().GetInt("limit")
		if since < 0 || limit < 0 || limit > appengine.MaxFeedLimit {
			return cli.WithExitCode(fmt.Errorf("--since must be at least 0 and --limit between 1 and %d", appengine.MaxFeedLimit), cli.ExitValidation)
		}
		client, err := humanWorkbookClient()
		if err != nil {
			return err
		}
		query := url.Values{"since": {strconv.FormatInt(since, 10)}}
		if limit > 0 {
			query.Set("limit", strconv.Itoa(limit))
		}
		response, err := client.Get("/api/v1/pages/" + pagePathEscape(args[0]) + "/workbook/commits?" + query.Encode())
		if err != nil {
			return err
		}
		if response.StatusCode == http.StatusGone {
			return printWorkbookRefusal(cmd, response, "workbook commits")
		}
		return printPageArtifactResponse(cmd, response)
	}}
	commits.Flags().Int64("since", 0, "Return revisions after this one (required)")
	commits.Flags().Int("limit", 0, fmt.Sprintf("Revisions per page, 1-%d (server default %d)", appengine.MaxFeedLimit, appengine.DefaultFeedLimit))
	_ = commits.MarkFlagRequired("since")
	return commits
}

// printWorkbookConflict prints a rejected edit's body, which carries the head
// revision and the server's values for conflicting cells, then exits 5.
func printWorkbookConflict(cmd *cobra.Command, response *http.Response) error {
	return printWorkbookRefusal(cmd, response, "workbook edit")
}

// printWorkbookRefusal prints a refusal body the caller acts on (a conflict,
// or a feed range that requires a reread), then exits 5.
func printWorkbookRefusal(cmd *cobra.Command, response *http.Response, what string) error {
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	var body struct {
		Error string `json:"error" yaml:"error"`
	}
	if err != nil || json.Unmarshal(raw, &body) != nil || body.Error == "" {
		return cli.WithExitCode(fmt.Errorf("%s conflict", what), cli.ExitConflict)
	}
	formatter := cli.NewFormatter(cli.ResolveFormat(flagFormat, nil))
	formatter.Writer = cmd.OutOrStdout()
	if err = formatter.Machine(cli.RawJSON(raw)); err != nil {
		return err
	}
	return cli.WithExitCode(fmt.Errorf("%s rejected: %s", what, body.Error), cli.ExitConflict)
}
func init() { rootCmd.AddCommand(newWorkbookCommand()) }
