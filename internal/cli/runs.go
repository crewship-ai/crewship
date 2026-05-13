package cli

import (
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

// RunDetail mirrors the per-run shape returned by GET /api/v1/runs/{id}.
//
// Field set tracks the legacy runResponse + the journal.RunAggregated read
// model: callers (wait, resume, diff, recap, tui) all share this single
// view, so adding a column means one type change and not three.
type RunDetail struct {
	ID           string          `json:"id"`
	AgentID      string          `json:"agent_id"`
	ChatID       *string         `json:"chat_id"`
	WorkspaceID  string          `json:"workspace_id"`
	TriggeredBy  *string         `json:"triggered_by"`
	TriggerType  string          `json:"trigger_type"`
	Status       string          `json:"status"`
	StartedAt    *string         `json:"started_at"`
	FinishedAt   *string         `json:"finished_at"`
	ErrorMessage *string         `json:"error_message"`
	ExitCode     *int            `json:"exit_code"`
	Metadata     json.RawMessage `json:"metadata"`
	CreatedAt    string          `json:"created_at"`
	AgentName    *string         `json:"agent_name,omitempty"`
	AgentSlug    *string         `json:"agent_slug,omitempty"`
	CrewName     *string         `json:"crew_name,omitempty"`
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
