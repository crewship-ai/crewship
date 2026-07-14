package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// MissionDetail mirrors the per-mission shape returned by
// GET /api/v1/crews/{crewId}/missions/{missionId}. Only the fields
// `wait`/PollMission need are included here — cmd_mission.go keeps its
// own richer anonymous struct (title, tasks, ...) for `mission get`.
type MissionDetail struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// IsTerminal reports whether the mission has reached a terminal status
// (the MissionEngine DAG loop is no longer running). Mirrors
// RunDetail.IsTerminal's contract for PollMission.
func (m *MissionDetail) IsTerminal() bool {
	switch strings.ToUpper(m.Status) {
	case "COMPLETED", "FAILED":
		return true
	}
	return false
}

// GetMission fetches a single mission by (crewID, missionID). The crew id
// is required by the route — callers resolve it via resolveMission first
// (see cmd_mission.go).
func (c *Client) GetMission(ctx context.Context, crewID, missionID string) (*MissionDetail, error) {
	if strings.TrimSpace(crewID) == "" {
		return nil, errors.New("crew id required")
	}
	if strings.TrimSpace(missionID) == "" {
		return nil, errors.New("mission id required")
	}
	resp, err := c.WithContext(ctx).Get("/api/v1/crews/" + url.PathEscape(crewID) + "/missions/" + url.PathEscape(missionID))
	if err != nil {
		return nil, fmt.Errorf("get mission %q: %w", missionID, err)
	}
	if err := CheckError(resp); err != nil {
		return nil, fmt.Errorf("get mission %q: %w", missionID, err)
	}
	var detail MissionDetail
	if err := ReadJSON(resp, &detail); err != nil {
		return nil, fmt.Errorf("decode mission %q: %w", missionID, err)
	}
	return &detail, nil
}

// PollMission polls GetMission at `interval` until the mission reaches a
// terminal status, ctx is cancelled, or the deadline (if set in ctx) is
// reached. Mirrors PollRun's cadence/onTick contract exactly so `mission
// start --wait` behaves the same as `crewship wait` and `routine run
// --wait`.
func (c *Client) PollMission(ctx context.Context, crewID, missionID string, interval time.Duration, onTick func(*MissionDetail)) (*MissionDetail, error) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		detail, err := c.GetMission(ctx, crewID, missionID)
		if err != nil {
			return nil, fmt.Errorf("poll mission %q: %w", missionID, err)
		}
		if detail.IsTerminal() {
			return detail, nil
		}
		if onTick != nil {
			onTick(detail)
		}
		select {
		case <-ctx.Done():
			return detail, fmt.Errorf("poll mission %q: %w", missionID, ctx.Err())
		case <-t.C:
			continue
		}
	}
}
