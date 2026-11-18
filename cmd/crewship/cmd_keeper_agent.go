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
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/scrubber"
)

const keeperAgentInputLimit = 4096 // matches the sidecar's intent/command bounds

// These commands deliberately use the original run capability and local
// sidecar, never a human profile, host API or caller-supplied agent identity.
func newKeeperAgentCommand(operation string) *cobra.Command {
	arg := "<credential-name>"
	if operation == "request-status" {
		arg = "<request-id>"
	}
	cmd := &cobra.Command{
		Use:   operation + " " + arg,
		Short: "Use Keeper from the current agent run (JSON output)",
		Long:  "Use the original CREWSHIP_AGENT_TOKEN through the local sidecar. No human profile fallback. ALLOW is authorization, not authentication success. Execute reads one command from stdin; never put credential values in it. DENY exits 4, ESCALATE/PENDING exits 5; an allowed execution propagates its command exit code. A lost response has an unknown outcome: inspect before retrying.",
		Args:  cobra.ExactArgs(1),
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			for _, name := range []string{"config", "server", "profile", "workspace", "server-allow-mismatch"} {
				if cmd.Flags().Changed(name) || cmd.InheritedFlags().Changed(name) {
					return errors.New("agent Keeper commands do not accept human config/server/profile/workspace overrides")
				}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error { return keeperAgentRequest(cmd, operation, args[0]) },
	}
	if operation != "request-status" {
		cmd.Flags().Bool("id", false, "Interpret the argument as a credential ID instead of a name")
		cmd.Flags().String("intent", "", "Purpose of the credential access (required; no credential values)")
		cmd.Flags().String("approval-request-id", "", "Previously approved escalation for this exact operation")
		cmd.Flags().String("task-id", "", "Associated task ID")
	}
	if operation == "execute" {
		cmd.Flags().Bool("command-stdin", false, "Read the command from stdin (required; maximum 4096 bytes)")
	}
	return cmd
}

type keeperAgentReply struct {
	RequestID          string  `json:"request_id"`
	Decision           string  `json:"decision"`
	Reason             string  `json:"reason,omitempty"`
	RiskScore          int     `json:"risk_score"`
	Output             string  `json:"output,omitempty"`
	ExitCode           *int    `json:"exit_code,omitempty"`
	RequestType        string  `json:"request_type,omitempty"`
	ApprovalConsumedAt *string `json:"approval_consumed_at,omitempty"`
}

func keeperAgentRequest(cmd *cobra.Command, operation, argument string) error {
	token := os.Getenv("CREWSHIP_AGENT_TOKEN")
	if !strings.HasPrefix(token, "agtv2.") || len(token) <= len("agtv2.") || len(token) > 2048 || strings.ContainsAny(token, " \r\n\t") {
		return cli.WithExitCode(errors.New("original live run CREWSHIP_AGENT_TOKEN required; no human profile fallback"), cli.ExitAuth)
	}
	origin, err := docDraftSidecarOrigin()
	if err != nil {
		return cli.WithExitCode(err, cli.ExitValidation)
	}
	if strings.TrimSpace(argument) == "" || len(argument) > 256 || !utf8.ValidString(argument) || strings.ContainsAny(argument, "\x00\r\n") {
		return cli.WithExitCode(errors.New("bounded credential name or request ID required"), cli.ExitValidation)
	}
	method, path := http.MethodPost, "/keeper/"+operation
	var body io.Reader
	if operation == "request-status" {
		if err := validateEntityCLIIdentifier(argument); err != nil {
			return cli.WithExitCode(errors.New("valid Keeper request ID required"), cli.ExitValidation)
		}
		method, path = http.MethodGet, "/keeper/request/"+url.PathEscape(argument)
	} else {
		intent, _ := cmd.Flags().GetString("intent")
		if strings.TrimSpace(intent) == "" || len(intent) > keeperAgentInputLimit || !utf8.ValidString(intent) || strings.ContainsRune(intent, 0) {
			return cli.WithExitCode(errors.New("--intent is required (maximum 4096 bytes, no NUL)"), cli.ExitValidation)
		}
		payload := map[string]string{"credential_name": argument, "intent": intent}
		if byID, _ := cmd.Flags().GetBool("id"); byID {
			delete(payload, "credential_name")
			payload["credential_id"] = argument
		}
		for _, flag := range []string{"approval-request-id", "task-id"} {
			value, _ := cmd.Flags().GetString(flag)
			if len(value) > 256 || strings.ContainsAny(value, "\x00\r\n") {
				return cli.WithExitCode(errors.New("invalid Keeper reference"), cli.ExitValidation)
			}
			if value != "" {
				payload[strings.ReplaceAll(flag, "-", "_")] = value
			}
		}
		if operation == "execute" {
			stdin, _ := cmd.Flags().GetBool("command-stdin")
			if !stdin {
				return cli.WithExitCode(errors.New("--command-stdin is required"), cli.ExitValidation)
			}
			raw, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), keeperAgentInputLimit+1))
			if err != nil || len(raw) > keeperAgentInputLimit || !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 || strings.TrimSpace(string(raw)) == "" {
				return cli.WithExitCode(errors.New("command stdin must contain at most 4096 bytes of text without NUL"), cli.ExitValidation)
			}
			payload["command"] = strings.TrimSpace(string(raw))
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(cmd.Context(), method, origin+path, body)
	if err != nil {
		return errors.New("cannot construct Keeper request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 5 * time.Minute}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("Keeper redirects are forbidden")
	}}
	resp, err := client.Do(req)
	if err != nil {
		// A POST may already have executed. Do not classify this as a
		// pre-response connection failure that automation may safely retry.
		return cli.WithExitCode(errors.New("Keeper outcome unavailable; inspect request state before retrying"), cli.ExitGeneric)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Never replay a server error body that might contain credential data.
		// In particular a bridge 502 can follow an already executed command.
		detail := "Keeper sidecar did not confirm the request"
		code := (&cli.APIError{Status: resp.StatusCode}).ExitCode()
		if operation == "execute" || resp.StatusCode >= 500 {
			detail = "Keeper outcome unavailable; inspect request state before retrying"
		}
		if resp.StatusCode >= 500 {
			code = cli.ExitGeneric
		}
		return cli.WithExitCode(fmt.Errorf("%s (HTTP %d)", detail, resp.StatusCode), code)
	}
	const responseLimit = 8 << 20 // accommodates JSON escaping of bounded command output
	raw, err := io.ReadAll(io.LimitReader(resp.Body, responseLimit+1))
	if err != nil || len(raw) > responseLimit {
		return keeperAgentResponseFailure(operation, "bounded Keeper response unavailable; inspect before retrying")
	}
	var reply struct {
		keeperAgentReply
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &reply) != nil {
		return keeperAgentResponseFailure(operation, "invalid Keeper response")
	}
	if operation == "request-status" {
		reply.RequestID = reply.ID
		if reply.Decision == "" {
			reply.Decision = "PENDING"
		}
	}
	if reply.RequestID == "" || (operation == "request-status" && reply.RequestID != argument) {
		return keeperAgentResponseFailure(operation, "Keeper response lacks the expected request identity")
	}
	switch reply.Decision {
	case "ALLOW", "DENY", "ESCALATE", "PENDING":
	default:
		return keeperAgentResponseFailure(operation, "unknown Keeper decision")
	}
	if operation == "execute" && reply.Decision == "ALLOW" && reply.ExitCode == nil {
		// The existing ExecuteResult API omits exit_code when it is zero.
		zero := 0
		reply.ExitCode = &zero
	}
	safe := scrubber.New()
	reply.Reason = safe.Scrub(strings.ReplaceAll(reply.Reason, token, "[REDACTED]"))
	reply.Output = safe.Scrub(strings.ReplaceAll(reply.Output, token, "[REDACTED]"))
	if err := json.NewEncoder(cmd.OutOrStdout()).Encode(reply.keeperAgentReply); err != nil {
		return err
	}
	if reply.Decision == "DENY" {
		return cli.WithExitCode(errors.New("Keeper denied access"), cli.ExitAuth)
	}
	if reply.Decision != "ALLOW" {
		return cli.WithExitCode(errors.New("Keeper decision requires resolution; no command success confirmed"), cli.ExitConflict)
	}
	if operation == "execute" && reply.ExitCode != nil && *reply.ExitCode != 0 {
		code := *reply.ExitCode
		if code < 1 || code > 255 {
			code = cli.ExitGeneric
		}
		return cli.WithExitCode(fmt.Errorf("Keeper command exited with code %d", *reply.ExitCode), code)
	}
	return nil
}

func keeperAgentResponseFailure(operation, detail string) error {
	if operation == "execute" {
		return cli.WithExitCode(errors.New("Keeper outcome unavailable; inspect request state before retrying"), cli.ExitGeneric)
	}
	return cli.WithExitCode(errors.New(detail), cli.ExitServer)
}
