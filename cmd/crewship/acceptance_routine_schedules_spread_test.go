package main

// Binary acceptance for the schedule fire-time spread: `routine schedules
// create/update --spread`, validation against the cron interval, the displayed
// next run including the stable offset, and clearing it again.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type spreadScheduleJSON struct {
	ID                  string `json:"id"`
	CronExpr            string `json:"cron_expr"`
	NextRunAt           string `json:"next_run_at"`
	NextOccurrenceAt    string `json:"next_occurrence_at"`
	SpreadSeconds       int    `json:"spread_seconds"`
	SpreadOffsetSeconds int    `json:"spread_offset_seconds"`
}

func TestAcceptanceRoutineSchedules_Spread(t *testing.T) {
	ownerCfg, _, _ := startRoutineSchedulesAcceptanceServer(t)
	saveRoutineSchedulesTarget(t, ownerCfg)
	run := func(args ...string) (spreadScheduleJSON, string, error) {
		t.Helper()
		out, stderr, err := runRoutineSchedulesCLI(t, ownerCfg, args...)
		var row spreadScheduleJSON
		if err == nil {
			if jerr := json.Unmarshal([]byte(out), &row); jerr != nil {
				t.Fatalf("not one schedule document: %v\n%s", jerr, out)
			}
		}
		return row, out + stderr, err
	}

	// `update` reports only {id, updated}; read the row back through list.
	reload := func(id string) (spreadScheduleJSON, string) {
		t.Helper()
		out, stderr, err := runRoutineSchedulesCLI(t, ownerCfg, "-f", "json", "routine", "schedules", "list")
		if err != nil {
			t.Fatalf("list: %v\n%s%s", err, out, stderr)
		}
		var rows []spreadScheduleJSON
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatalf("list JSON: %v\n%s", err, out)
		}
		for _, r := range rows {
			if r.ID == id {
				return r, out
			}
		}
		t.Fatalf("schedule %s not in list:\n%s", id, out)
		return spreadScheduleJSON{}, ""
	}

	// Default is off and adds nothing to the payload.
	plain, raw, err := run("-f", "json", "routine", "schedules", "create", "--slug", "acceptance-sched", "--cron", "0 * * * *")
	if err != nil || plain.SpreadSeconds != 0 || strings.Contains(raw, "spread") {
		t.Fatalf("default create: err=%v\n%s", err, raw)
	}

	// Refused locally or by the server: not a duration, negative, >= interval.
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"not a duration", []string{"--cron", "0 * * * *", "--spread", "soon"}},
		{"negative", []string{"--cron", "0 * * * *", "--spread", "-5m"}},
		{"fractional second", []string{"--cron", "0 * * * *", "--spread", "1500ms"}},
		{"equal to the hourly interval", []string{"--cron", "0 * * * *", "--spread", "1h"}},
		{"longer than the minutely interval", []string{"--cron", "* * * * *", "--spread", "90s"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"routine", "schedules", "create", "--slug", "acceptance-sched"}, tc.args...)
			if _, out, err := run(args...); err == nil {
				t.Fatalf("must be refused, got:\n%s", out)
			}
		})
	}

	created, raw, err := run("-f", "json", "routine", "schedules", "create", "--slug", "acceptance-sched",
		"--cron", "0 * * * *", "--spread", "5m")
	if err != nil {
		t.Fatalf("create --spread 5m: %v\n%s", err, raw)
	}
	if created.SpreadSeconds != 300 || created.SpreadOffsetSeconds < 0 || created.SpreadOffsetSeconds >= 300 {
		t.Fatalf("spread not applied: %+v", created)
	}
	next, _ := time.Parse(time.RFC3339Nano, created.NextRunAt)
	occ, _ := time.Parse(time.RFC3339Nano, created.NextOccurrenceAt)
	if next.IsZero() || !next.Equal(occ.Add(time.Duration(created.SpreadOffsetSeconds)*time.Second)) {
		t.Fatalf("displayed next run must be the cron time plus the offset: %+v", created)
	}

	// The offset is stable across an unrelated update, and the list shows it.
	if _, raw, err := run("-f", "json", "routine", "schedules", "update", created.ID, "--name", "renamed"); err != nil {
		t.Fatalf("rename: %v\n%s", err, raw)
	}
	renamed, raw := reload(created.ID)
	if renamed.SpreadOffsetSeconds != created.SpreadOffsetSeconds || renamed.SpreadSeconds != 300 {
		t.Fatalf("rename changed the spread: %+v\n%s", renamed, raw)
	}

	// A cron shorter than the stored spread is refused.
	if _, out, err := run("routine", "schedules", "update", created.ID, "--cron", "*/2 * * * *"); err == nil {
		t.Fatalf("a cron interval below the spread must be refused:\n%s", out)
	}
	if _, raw, err := run("-f", "json", "routine", "schedules", "update", created.ID, "--spread", "90s"); err != nil {
		t.Fatalf("update --spread 90s: %v\n%s", err, raw)
	}
	shrunk, raw := reload(created.ID)
	if shrunk.SpreadSeconds != 90 || shrunk.SpreadOffsetSeconds >= 90 {
		t.Fatalf("update --spread 90s: %+v\n%s", shrunk, raw)
	}
	if _, raw, err := run("-f", "json", "routine", "schedules", "update", created.ID, "--spread", "0"); err != nil {
		t.Fatalf("update --spread 0: %v\n%s", err, raw)
	}
	cleared, raw := reload(created.ID)
	if cleared.SpreadSeconds != 0 || cleared.NextOccurrenceAt != "" || cleared.SpreadOffsetSeconds != 0 {
		t.Fatalf("update --spread 0 must clear it:\n%s", raw)
	}
}
