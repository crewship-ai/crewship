package main

// GET /api/v1/internal/documents/draft doc draft view
// POST /api/v1/internal/documents/draft doc draft apply, doc draft checkpoint, doc draft propose

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
	"github.com/crewship-ai/crewship/internal/sidecar"
)

func newDocDraftCommand() *cobra.Command { return newDocDraftCommandWithTransport(nil) }

// Transport injection is available to Go tests only, never command flags.
func newDocDraftCommandWithTransport(transport http.RoundTripper) *cobra.Command {
	root := &cobra.Command{Use: "draft", Short: "Save guarded drafts of the document this live run was admitted to write", Example: `  crewship doc draft view
  crewship doc draft view --page <page-id>
  crewship doc draft prepare --file input.json > packet.json
  crewship doc draft apply --file packet.json
  crewship doc draft propose --file packet.json`, Long: "Use view, then prepare once and retain the immutable packet. prepare is unsaved. apply commits text then pins it; a failed pin does not undo committed text. Retry the exact packet, never regenerate its preconditions. Only this run’s admitted new or existing document; no other artifact or human-profile fallback. view --page reads, never drafts, one Document the run reaches without a consent (attached to the message that started it, or through the agent's own grant or its crew's folder); the server decides. On a schema 4, 5 or 6 document the view has mode propose: prepare builds a proposal packet and propose sends it; the document is unchanged until a human accepts it.", PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
		for _, key := range []string{"config", "server", "profile", "workspace", "server-allow-mismatch"} {
			if cmd.Flags().Changed(key) || cmd.InheritedFlags().Changed(key) {
				return errors.New("protected Docs drafting does not accept config/server/profile/workspace overrides")
			}
		}
		return nil
	}}
	shorts := map[string]string{
		"view":       "Read headings, paragraphs, block hashes, revision and epoch of the admitted document",
		"prepare":    "Build an immutable edit and checkpoint packet locally without saving",
		"apply":      "Apply a prepared packet and checkpoint the confirmed revision",
		"checkpoint": "Retry only the checkpoint of a packet whose text edit is already saved",
		"propose":    "Send a prepared proposal packet for human review; the document is unchanged",
	}
	for _, op := range []string{"view", "prepare", "apply", "checkpoint", "propose"} {
		op := op
		command := &cobra.Command{Use: op, Short: shorts[op], Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			token := os.Getenv("CREWSHIP_AGENT_TOKEN")
			if !strings.HasPrefix(token, "agtv2.") || len(token) > 2048 || strings.ContainsAny(token, " \t\r\n") {
				return errors.New("original CREWSHIP_AGENT_TOKEN run capability required; no human profile fallback")
			}
			// Only the read-only view may use another loopback sidecar;
			// prepare, apply and propose keep the fixed transport.
			origin := selectedAgentOrigin
			if op == "view" {
				var err error
				if origin, err = docDraftSidecarOrigin(); err != nil {
					return err
				}
			}
			query := ""
			if op == "view" {
				if page, _ := cmd.Flags().GetString("page"); cmd.Flags().Changed("page") {
					if page == "" {
						return errors.New("--page needs a Document page ID")
					}
					query = "?" + url.Values{"page_id": []string{page}}.Encode()
				}
			}
			exchange := func(method string, body []byte) (int, []byte, error) {
				target := origin + "/api/v1/documents/draft"
				if method == http.MethodGet {
					target += query
				}
				req, err := http.NewRequestWithContext(cmd.Context(), method, target, bytes.NewReader(body))
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
			formatter := cli.NewFormatter(cli.ResolveFormat(flagFormat, nil))
			formatter.Writer = cmd.OutOrStdout()
			output := func(value any) error {
				raw, err := json.Marshal(value)
				if err != nil {
					return err
				}
				// Keep JSON field names, nested receipts and int64 preconditions
				// intact when the shared formatter renders another format.
				return formatter.Machine(cli.RawJSON(raw))
			}
			if op == "view" {
				status, raw, err := exchange(http.MethodGet, nil)
				if err != nil {
					return err
				}
				if status < 200 || status >= 300 {
					return fmt.Errorf("draft view HTTP%d denied or unavailable", status)
				}
				var value json.RawMessage = raw
				return output(value)
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
				p, err := sidecar.PrepareDocumentDraftOrProposal(raw)
				if err != nil {
					return err
				}
				return output(map[string]any{"status": "prepared_unsaved", "packet": p})
			}
			if op == "propose" {
				packet, err := sidecar.ParseDocumentProposalPacket(raw)
				if err != nil {
					return err
				}
				out, applyErr := sidecar.ApplyDocumentProposal(packet, exchange)
				if err := output(out); err != nil {
					return err
				}
				return applyErr
			}
			packet, err := sidecar.ParseDocumentDraftPacket(raw)
			if err != nil {
				return err
			}
			out, applyErr := sidecar.ApplyDocumentDraft(packet, op == "checkpoint", exchange)
			if err := output(out); err != nil {
				return err
			}
			return applyErr
		}}
		if op != "view" {
			command.Flags().String("file", "", "Original JSON input or immutable packet file; - reads stdin")
		} else {
			command.Flags().String("page", "", "Read a Document this run reaches without a consent (page ID): attached to the message that started it, or reached through the agent's own grant or its crew's folder; the view is read-only")
		}
		root.AddCommand(command)
	}
	return root
}

// docDraftSidecarOrigin is the protected run sidecar of `doc draft view`: the
// fixed loopback origin, or CREWSHIP_SIDECAR_URL when it names another plain
// loopback http origin (as for `mooring agent`). It is transport selection, never an
// authority override: the run's own bearer still decides everything.
func docDraftSidecarOrigin() (string, error) {
	base := os.Getenv("CREWSHIP_SIDECAR_URL")
	if base == "" {
		return selectedAgentOrigin, nil
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.RawQuery != "" || u.ForceQuery || u.Path != "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return "", errors.New("loopback sidecar URL required")
	}
	return "http://" + u.Host, nil
}
