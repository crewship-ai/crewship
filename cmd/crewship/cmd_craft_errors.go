package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// craftHTTPError turns a non-2xx native Craft response into the CLI's
// ordinary classified error: the server's body text, the status → exit-code
// mapping (409 conflict, 422 validation, 5xx server…) and the JSON error
// envelope under --format json. The private session token is removed from the
// body before any of it is parsed or printed.
func craftHTTPError(r *http.Response, secret string) error {
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
	if secret != "" {
		raw = bytes.ReplaceAll(raw, []byte(secret), []byte("[redacted]"))
	}
	err := cli.CheckError(&http.Response{StatusCode: r.StatusCode, Body: io.NopCloser(bytes.NewReader(raw))})
	var api *cli.APIError
	if !errors.As(err, &api) {
		return err
	}
	if api.Extensions != nil {
		delete(api.Extensions, "session_token")
	}
	if state := craftProposalState(api.Extensions); state != "" {
		return &craftContextError{api: api, note: "proposal state: " + state}
	}
	return api
}

// craftProposalState reads proposal.state from a refusal body, so a 409 on
// accept tells "stale — the document moved on" apart from "already decided".
func craftProposalState(body map[string]any) string {
	proposal, _ := body["proposal"].(map[string]any)
	state, _ := proposal["state"].(string)
	return state
}

// craftContextError appends what the CLI learned about a refusal to the
// server's message without losing the APIError (status, exit code, envelope).
type craftContextError struct {
	api  *cli.APIError
	note string
}

func (e *craftContextError) Error() string { return e.api.Error() + " (" + e.note + ")" }
func (e *craftContextError) Unwrap() error { return e.api }
func (e *craftContextError) ExitCode() int { return e.api.ExitCode() }

// craftLeaseAction reports whether a refusal of this action is about the
// editor lease, so a 409 can name who holds it.
func craftLeaseAction(action string) bool {
	switch action {
	case "acquire", "acquire-explicit", "renew", "release", "request-handoff", "handoff":
		return true
	}
	return false
}

// craftWithLeaseHolder reads the current lease once after a 409 and adds its
// holder to the error, because the refusal body says only "lease changed".
// The extra read is best-effort; the original refusal is returned regardless.
func craftWithLeaseHolder(c *cli.Client, page string, err error, secret string) error {
	var api *cli.APIError
	if !errors.As(err, &api) || api.Status != http.StatusConflict {
		return err
	}
	r, e := c.Get("/api/v1/pages/" + pagePathEscape(page) + "/craft/lease")
	if e != nil {
		return err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		return err
	}
	raw, e := craftReadJSON(r.Body)
	if e != nil {
		return err
	}
	if secret != "" {
		raw = bytes.ReplaceAll(raw, []byte(secret), []byte("[redacted]"))
	}
	var lease struct {
		Mine   bool `json:"mine" yaml:"mine"`
		Holder *struct {
			DisplayName string `json:"display_name" yaml:"display_name"`
			Kind        string `json:"kind" yaml:"kind"`
			ActorID     string `json:"actor_id" yaml:"actor_id"`
			RunID       string `json:"run_id" yaml:"run_id"`
		} `json:"holder" yaml:"holder"`
		Generation *string `json:"generation" yaml:"generation"`
	}
	if json.Unmarshal(raw, &lease) != nil {
		return err
	}
	var note string
	switch {
	case lease.Holder == nil:
		note = "the lease is now free"
	case lease.Mine:
		note = "this session holds the lease"
	default:
		parts := []string{lease.Holder.Kind}
		if lease.Holder.DisplayName != "" {
			parts = append(parts, fmt.Sprintf("%q", lease.Holder.DisplayName))
		}
		if lease.Holder.RunID != "" {
			parts = append(parts, "run "+lease.Holder.RunID)
		}
		note = "lease held by " + strings.Join(parts, " ")
	}
	if lease.Generation != nil {
		note += ", generation " + *lease.Generation
	}
	if api.Extensions == nil {
		api.Extensions = map[string]any{}
	}
	var leaseDoc any
	if json.Unmarshal(raw, &leaseDoc) == nil {
		api.Extensions["lease"] = leaseDoc
	}
	return &craftContextError{api: api, note: note}
}

// craftTransportError keeps the established opaque wording (no URL, no
// header) while classifying it as a connection failure (exit 8).
func craftTransportError(what string) error {
	return cli.WithExitCode(errors.New(what), cli.ExitConnection)
}

// craftAgentResponse is craftResponse for the native agent task routes. Their
// refusal bodies can describe a private runtime or document, so the CLI
// classifies the status (exit code, envelope status) without reflecting the
// body — the established contract for these commands.
func craftAgentResponse(cmd *cobra.Command, r *http.Response, err error) error {
	if err == nil && (r.StatusCode < 200 || r.StatusCode >= 300) {
		_ = r.Body.Close()
		return &craftStatusError{status: r.StatusCode}
	}
	return craftResponse(cmd, r, err, "")
}

type craftStatusError struct{ status int }

func (e *craftStatusError) Error() string { return fmt.Sprintf("native HTTP %d", e.status) }
func (e *craftStatusError) ExitCode() int { return (&cli.APIError{Status: e.status}).ExitCode() }
