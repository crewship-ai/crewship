package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// HireStatus is the minimal agent shape `hire --wait` needs to detect
// when a guided-autonomy hire (staged PENDING_REVIEW, see
// POST /api/v1/agents/hire) has been resolved. Sourced from the same
// GET /api/v1/agents/{agentId} route `crewship agent get` uses.
type HireStatus struct {
	ID        string  `json:"id"`
	Status    string  `json:"status"`
	ExpiredAt *string `json:"expired_at"`
}

// IsResolved reports whether a staged hire has reached a terminal
// outcome: approved (status flipped away from PENDING_REVIEW by
// POST /agents/{id}/approve-hire) or ghosted (its TTL elapsed before
// anyone approved it — expired_at gets set even while still
// PENDING_REVIEW). There is no explicit "denied" status today — a
// rejected hire is left to expire, which IsResolved also treats as
// terminal so `--wait` doesn't hang forever.
func (h *HireStatus) IsResolved() bool {
	if strings.ToUpper(h.Status) != "PENDING_REVIEW" {
		return true
	}
	return h.ExpiredAt != nil
}

// GetHireStatus fetches the minimal status view of an agent by id.
func (c *Client) GetHireStatus(ctx context.Context, agentID string) (*HireStatus, error) {
	if strings.TrimSpace(agentID) == "" {
		return nil, errors.New("agent id required")
	}
	resp, err := c.WithContext(ctx).Get("/api/v1/agents/" + url.PathEscape(agentID))
	if err != nil {
		return nil, fmt.Errorf("get agent %q: %w", agentID, err)
	}
	if err := CheckError(resp); err != nil {
		return nil, fmt.Errorf("get agent %q: %w", agentID, err)
	}
	var status HireStatus
	if err := ReadJSON(resp, &status); err != nil {
		return nil, fmt.Errorf("decode agent %q: %w", agentID, err)
	}
	return &status, nil
}

// PollHireApproval polls GetHireStatus at `interval` until the staged
// hire resolves (approved or ghosted), ctx is cancelled, or the
// deadline (if set in ctx) is reached. Mirrors PollRun/PollMission's
// cadence/onTick contract.
func (c *Client) PollHireApproval(ctx context.Context, agentID string, interval time.Duration, onTick func(*HireStatus)) (*HireStatus, error) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		status, err := c.GetHireStatus(ctx, agentID)
		if err != nil {
			return nil, fmt.Errorf("poll hire %q: %w", agentID, err)
		}
		if status.IsResolved() {
			return status, nil
		}
		if onTick != nil {
			onTick(status)
		}
		select {
		case <-ctx.Done():
			return status, fmt.Errorf("poll hire %q: %w", agentID, ctx.Err())
		case <-t.C:
			continue
		}
	}
}
