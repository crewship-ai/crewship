package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// RawJSON is a JSON document the CLI carries but does not own — a run's
// `metadata` is the first one — held as the bytes the server sent.
//
// It is json.RawMessage plus one thing: it knows how to render itself as YAML.
// A bare json.RawMessage IS a []byte, and yaml.v3 renders a []byte as a
// sequence of integers, one per line, so `run list --format yaml` over a
// handful of runs printed hundreds of numbers where a mapping belonged.
// cmd_preferences.go had already had to decode around the same thing by hand
// at its own call site.
//
// The knowledge lives on the type rather than inside Formatter.YAML on
// purpose. The formatter is handed structs that variously carry json tags
// (RunDetail), yaml tags (cli.SlashCommand) or neither, so re-routing it
// through JSON would have renamed the keys of every other command's YAML
// output in order to fix this one. Any other field holding an opaque document
// should take this type.
type RawJSON []byte

// MarshalJSON hands the document back byte for byte. An absent one is null,
// not an empty string: "the server sent no metadata" is what null means here.
func (r RawJSON) MarshalJSON() ([]byte, error) {
	if len(r) == 0 {
		return []byte("null"), nil
	}
	return r, nil
}

func (r *RawJSON) UnmarshalJSON(b []byte) error {
	if r == nil {
		return errors.New("cli.RawJSON: UnmarshalJSON on nil pointer")
	}
	*r = append((*r)[:0], b...)
	return nil
}

// MarshalYAML decodes the document so the YAML encoder is given its SHAPE —
// mapping, sequence, scalar — instead of the bytes it is spelled with.
//
// Numbers go through json.Number and come back as int64 wherever they are
// whole. Decoding straight into float64, which is what encoding/json does by
// default, would print a token count or a unix timestamp as 1.699999999e+09.
//
// A document that does not parse is rendered as the string it is. It came from
// a server this CLI did not ship with, or a row edited around the API, and one
// unreadable field must not fail the whole listing.
func (r RawJSON) MarshalYAML() (interface{}, error) {
	if len(bytes.TrimSpace(r)) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(r))
	dec.UseNumber()
	var doc interface{}
	if err := dec.Decode(&doc); err != nil {
		return string(r), nil
	}
	return yamlNumbers(doc), nil
}

// String is the document as it was received — what the table and human
// renderers print when they want the literal.
func (r RawJSON) String() string { return string(r) }

// yamlNumbers walks a decoded document turning json.Number into the narrowest
// Go scalar that holds it. json.Number is a string type, so handing one
// straight to the YAML encoder would quote every number in the document.
func yamlNumbers(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		for k, item := range val {
			val[k] = yamlNumbers(item)
		}
		return val
	case []interface{}:
		for i, item := range val {
			val[i] = yamlNumbers(item)
		}
		return val
	case json.Number:
		if n, err := strconv.ParseInt(val.String(), 10, 64); err == nil {
			return n
		}
		if f, err := val.Float64(); err == nil {
			return f
		}
		return val.String()
	}
	return v
}

// RunDetail mirrors the per-run shape returned by GET /api/v1/runs/{id}.
//
// Field set tracks the legacy runResponse + the journal.RunAggregated read
// model: callers (wait, resume, diff, recap, tui) all share this single
// view, so adding a column means one type change and not three.
type RunDetail struct {
	ID          string  `json:"id"`
	AgentID     string  `json:"agent_id"`
	ChatID      *string `json:"chat_id"`
	WorkspaceID string  `json:"workspace_id"`
	TriggeredBy *string `json:"triggered_by"`
	TriggerType string  `json:"trigger_type"`
	// Kind discriminates which engine produced this run: "agent" for an
	// ad-hoc agent/chat execution, "pipeline" for a routine run (#2284).
	Kind         string  `json:"kind"`
	Status       string  `json:"status"`
	StartedAt    *string `json:"started_at"`
	FinishedAt   *string `json:"finished_at"`
	ErrorMessage *string `json:"error_message"`
	ExitCode     *int    `json:"exit_code"`
	// Metadata is the server's own blob. RawJSON, not json.RawMessage, so
	// `--format yaml` renders the document rather than its bytes.
	Metadata RawJSON `json:"metadata"`
	// Model and the session-provenance fields below are absent on older runs
	// and on adapters that report no session-init, so every one of them is a
	// pointer: a renderer has to be able to skip the row rather than print an
	// empty one that reads as "recorded, and empty".
	Model           *string          `json:"model,omitempty"`
	CLIVersion      *string          `json:"cli_version,omitempty"`
	APIKeySource    *string          `json:"api_key_source,omitempty"`
	PermissionMode  *string          `json:"permission_mode,omitempty"`
	SessionID       *string          `json:"session_id,omitempty"`
	MCPServerErrors []MCPServerError `json:"mcp_server_errors,omitempty"`
	// MCPServerErrorCount is how many servers the CLI reported skipping. It can
	// exceed len(MCPServerErrors): the producer stores only the entries it could
	// project, and the list is capped. Absent (0) on runs that recorded no
	// count, so a renderer reports a shortfall only when this is the larger.
	MCPServerErrorCount int `json:"mcp_server_error_count,omitempty"`
	// MCPServerErrorsTruncated / PermissionDenialsTruncated say the list above
	// was capped, so what it names is not all of it.
	MCPServerErrorsTruncated bool `json:"mcp_server_errors_truncated,omitempty"`
	// PermissionDenials names the tools the CLI refused to let the agent use and
	// how many times each was refused. Names and counts only — the denied input
	// never reaches the run record.
	PermissionDenials          []DeniedTool `json:"permission_denials,omitempty"`
	PermissionDenialsTruncated bool         `json:"permission_denials_truncated,omitempty"`
	CreatedAt                  string       `json:"created_at"`
	AgentName                  *string      `json:"agent_name,omitempty"`
	AgentSlug                  *string      `json:"agent_slug,omitempty"`
	CrewName                   *string      `json:"crew_name,omitempty"`
}

// DeniedTool is one tool the CLI refused to let the agent use, with the number
// of refusals the producer collapsed into it. Count is 0 when the record
// predates the tally — not "denied zero times", which a run record never says.
type DeniedTool struct {
	ToolName string `json:"tool_name"`
	Count    int    `json:"count,omitempty"`
}

// UnmarshalJSON accepts a bare tool name as well as the object form, because
// this CLI talks to servers it did not ship with: `permission_denials` was an
// array of strings before the count was added, and a CLI that rejected the old
// shape would fail the whole decode — reporting NO denials for a run the CLI
// blocked, which is the exact misdiagnosis this field exists to prevent.
func (d *DeniedTool) UnmarshalJSON(b []byte) error {
	var name string
	if err := json.Unmarshal(b, &name); err == nil {
		d.ToolName, d.Count = name, 0
		return nil
	}
	// Alias breaks the recursion into this method.
	type deniedTool DeniedTool
	var v deniedTool
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*d = DeniedTool(v)
	return nil
}

// MCPServerError mirrors one entry of the run's mcp_server_errors: an MCP
// server the CLI skipped at startup, so the run finished without a capability
// it was configured for.
type MCPServerError struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Message string `json:"message"`
}

// IsTerminal reports whether the run has reached a terminal status that
// will not change further. Used by PollRun to know when to stop.
func (r *RunDetail) IsTerminal() bool {
	switch strings.ToUpper(r.Status) {
	case "COMPLETED", "FAILED", "CANCELLED", "TIMEOUT":
		return true
	}
	return false
}

// PipelineRunIDHint is the shared, issue-#1193 error message for a
// run_-shaped id fed into a command that only resolves msg_-shaped
// chat-turn run ids. Both GetRun (backing diff/resume) and fetchRun
// (backing inspect/explain, in cmd/crewship) surface this instead of a
// bare 404 — see IsPipelineRunID for why the two ID shapes are genuinely
// different data.
func PipelineRunIDHint(id string) error {
	return NotFoundf(
		"%s looks like a pipeline run id (from `routine runs`), not a chat-turn run id — "+
			"this command works with chat-turn runs (msg_..., from `crewship history`), not pipeline runs — "+
			"use `crewship routine logs %s` instead", id, id)
}

// GetRun fetches a single run by id.
//
// The server endpoint is GET /api/v1/runs/{id}. The endpoint was added
// alongside this CLI helper — older servers will return 404; callers
// should treat that as "endpoint unavailable" (wrap the error in a hint
// to upgrade the server) rather than "run not found".
func (c *Client) GetRun(ctx context.Context, id string) (*RunDetail, error) {
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("run id required")
	}
	if IsPipelineRunID(id) {
		return nil, PipelineRunIDHint(id)
	}
	resp, err := c.WithContext(ctx).Get("/api/v1/runs/" + url.PathEscape(id))
	if err != nil {
		return nil, fmt.Errorf("get run %q: %w", id, err)
	}
	if err := CheckError(resp); err != nil {
		return nil, fmt.Errorf("get run %q: %w", id, err)
	}
	var detail RunDetail
	if err := ReadJSON(resp, &detail); err != nil {
		return nil, fmt.Errorf("decode run %q: %w", id, err)
	}
	return &detail, nil
}

// PollRun polls GetRun(id) at `interval` until the run reaches a terminal
// status, ctx is cancelled, or the deadline (if set in ctx) is reached.
//
// The poller uses a fixed cadence rather than exponential backoff — agent
// runs typically complete within seconds-to-minutes, so the cost of a
// steady cadence is bounded and predictable. Callers wanting a different
// pattern can wrap GetRun themselves.
//
// A nil callback is allowed. If non-nil, it's invoked after every
// non-terminal status read so callers can render progress (e.g., a
// spinner or "still running [12s elapsed]" tick).
func (c *Client) PollRun(ctx context.Context, id string, interval time.Duration, onTick func(*RunDetail)) (*RunDetail, error) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()

	// First read happens immediately so callers see initial state without
	// waiting one full interval — important for already-completed runs.
	for {
		detail, err := c.GetRun(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("poll run %q: %w", id, err)
		}
		if detail.IsTerminal() {
			return detail, nil
		}
		if onTick != nil {
			onTick(detail)
		}
		select {
		case <-ctx.Done():
			// Wrap with the run id so a cancelled / deadlined poll
			// reads in logs as "poll run <id>: context deadline
			// exceeded" instead of an isolated context error.
			return detail, fmt.Errorf("poll run %q: %w", id, ctx.Err())
		case <-t.C:
			continue
		}
	}
}

// prURLPattern matches GitHub/GitLab/Bitbucket-style PR URLs and
// extracts (owner-or-group-path, repo, number). Sites tested:
//
//	https://github.com/foo/bar/pull/123
//	https://gitlab.com/foo/bar/-/merge_requests/123
//	https://gitlab.com/group/subgroup/repo/-/merge_requests/123
//	https://bitbucket.org/foo/bar/pull-requests/123
//
// GitLab supports nested subgroups (`group/subgroup/.../repo`). To keep
// resume-lookups working across those, the first capture is a greedy
// path component that allows additional `/` segments — we then assume
// the LAST path segment before the keyword is the repo and everything
// preceding it is the owner/group path. Per-host quirks (gitlab's
// `-/merge_requests`, bitbucket's hyphenated `pull-requests`) live in
// the alternation so adding a new host is a one-line change.
var prURLPattern = regexp.MustCompile(`(?i)^https?://[^/]+/(.+?)/([^/]+)/(?:pull|pulls|pull-requests|-/merge_requests)/(\d+)`)

// ParsePRURL extracts (owner, repo, number) from a pull-request URL.
// Returns ok=false when the URL doesn't match any supported pattern;
// callers should fall back to treating the input as a session-id.
//
// For GitLab subgroups, owner contains the full group/subgroup path
// (e.g. "group/subgroup"). That matches what GitLab actually stores
// for the project namespace, so journal searches keyed on `owner/repo`
// stay round-trippable.
func ParsePRURL(s string) (owner, repo string, number int, ok bool) {
	s = strings.TrimSpace(s)
	m := prURLPattern.FindStringSubmatch(s)
	if len(m) != 4 {
		return "", "", 0, false
	}
	n, err := strconv.Atoi(m[3])
	if err != nil {
		return "", "", 0, false
	}
	return m[1], m[2], n, true
}
