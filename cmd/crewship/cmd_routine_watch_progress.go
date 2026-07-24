package main

// Client-facing progress for `routine watch --progress` (#840). The default
// watch is a raw event tail for CI/scripting; --progress instead renders ONE
// updating status line a non-engineer can read: step N/M, current step, live
// cost, elapsed. Rebuilt from the event window each poll (idempotent), so it
// reads correctly whether the watch attached at run start or mid-flight.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
)

// fetchRoutineStepCount returns the number of steps in the routine's current
// definition (for the progress "step N/M"). Best-effort: 0 on any failure, so
// progress degrades to "N/?" rather than erroring the whole watch.
func fetchRoutineStepCount(client *cli.Client, ws, slug string) int {
	resp, err := client.Get("/api/v1/workspaces/" + url.PathEscape(ws) + "/pipelines/" + url.PathEscape(slug))
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0
	}
	var body struct {
		Definition struct {
			Steps []json.RawMessage `json:"steps"`
		} `json:"definition"`
	}
	if json.NewDecoder(resp.Body).Decode(&body) != nil {
		return 0
	}
	return len(body.Definition.Steps)
}

type runProgress struct {
	RunID       string
	Status      string // running | waiting | completed | failed
	CurrentStep string
	Completed   int
	Total       int // 0 when the routine's step count is unknown
	CostUSD     float64
	Elapsed     time.Duration
	Terminal    bool
	Failed      bool
}

// computeProgress rebuilds the target run's progress from the event window.
// The target is runIDFilter when set, else the run of the newest event. Rows
// are expected oldest-first (as the watch loop delivers them) so the last
// status-bearing event wins chronologically. Returns nil when no run is
// present in the window.
func computeProgress(rows []watchEntry, totalSteps int, runIDFilter string, now time.Time) *runProgress {
	target := runIDFilter
	if target == "" {
		var newest time.Time
		for _, r := range rows {
			if r.RunID == "" {
				continue
			}
			if t := parseWatchTS(r.Timestamp); t.After(newest) {
				newest = t
				target = r.RunID
			}
		}
	}
	if target == "" {
		return nil
	}

	p := &runProgress{RunID: target, Total: totalSteps, Status: "running"}
	completed := map[string]bool{}
	var startedAt, endedAt time.Time
	for _, r := range rows {
		if r.RunID != target {
			continue
		}
		switch r.EntryType {
		case "pipeline.run.started":
			startedAt = parseWatchTS(r.Timestamp)
		case "pipeline.step.started":
			if sid := payloadStepID(r.Payload); sid != "" {
				p.CurrentStep = sid
			}
			if !p.Terminal {
				p.Status = "running" // resumed from a waitpoint
			}
		case "pipeline.step.completed":
			if sid := payloadStepID(r.Payload); sid != "" {
				completed[sid] = true
			}
			p.CostUSD += payloadFloat(r.Payload, "cost_usd")
		case "pipeline.step.skipped":
			// A skipped step is "processed" for progress purposes (step N/total
			// shouldn't stall below total just because a branch was skipped).
			// Pre-dedicated-type rows arrived as completed+kind=skipped and were
			// already counted by the case above; this covers the new type.
			if sid := payloadStepID(r.Payload); sid != "" {
				completed[sid] = true
			}
		case "pipeline.waitpoint.created":
			if !p.Terminal {
				p.Status = "waiting"
			}
		case "pipeline.run.completed":
			p.Terminal = true
			p.Status = "completed"
			endedAt = parseWatchTS(r.Timestamp)
			if tc := payloadFloat(r.Payload, "total_cost_usd"); tc > 0 {
				p.CostUSD = tc
			}
		case "pipeline.run.failed":
			p.Terminal = true
			p.Failed = true
			p.Status = "failed"
			endedAt = parseWatchTS(r.Timestamp)
			if tc := payloadFloat(r.Payload, "total_cost_usd"); tc > 0 {
				p.CostUSD = tc
			}
		}
	}
	p.Completed = len(completed)
	if !startedAt.IsZero() {
		end := now
		if !endedAt.IsZero() {
			end = endedAt // a finished run's elapsed is fixed, not still ticking
		}
		if d := end.Sub(startedAt); d > 0 {
			p.Elapsed = d
		}
	}
	return p
}

// formatProgressLine renders the single updating status line.
func formatProgressLine(p *runProgress, slug string) string {
	if p == nil {
		return fmt.Sprintf("▶ %s · waiting for a run…", slug)
	}
	total := "?"
	if p.Total > 0 {
		total = strconv.Itoa(p.Total)
	}
	step := fmt.Sprintf("step %d/%s", p.Completed, total)
	if p.CurrentStep != "" && !p.Terminal {
		step += " (" + p.CurrentStep + ")"
	}
	cost := "—"
	if p.CostUSD > 0 {
		cost = fmt.Sprintf("$%.4f", p.CostUSD)
	}
	return fmt.Sprintf("▶ %s · %s · %s · %s · %s",
		slug, step, strings.ToUpper(p.Status), cost, formatElapsed(p.Elapsed))
}

// parseWatchTS parses a journal timestamp, tolerating both the nano and
// second RFC3339 shapes the API emits. Zero time on failure.
func parseWatchTS(ts string) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		return t
	}
	return time.Time{}
}

func payloadStepID(p map[string]interface{}) string {
	if p == nil {
		return ""
	}
	s, _ := p["step_id"].(string)
	return s
}

// payloadFloat reads a numeric payload field, tolerating float64/int (JSON
// numbers decode to float64, but be defensive).
func payloadFloat(p map[string]interface{}, key string) float64 {
	if p == nil {
		return 0
	}
	switch v := p[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	}
	return 0
}

// formatElapsed renders a duration as the same shape as the cost/duration
// columns: Xs / XmYYs.
func formatElapsed(d time.Duration) string {
	if d <= 0 {
		return "—"
	}
	sec := int(d.Seconds() + 0.5)
	if sec < 60 {
		return strconv.Itoa(sec) + "s"
	}
	return fmt.Sprintf("%dm%02ds", sec/60, sec%60)
}
