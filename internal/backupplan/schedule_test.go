package backupplan

import (
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func str(s string) *string { return &s }

func TestNextOccurrences(t *testing.T) {
	type want struct {
		at  string
		env bool
	}
	cases := []struct {
		name  string
		plan  Plan
		after string
		want  []want
	}{
		{
			// 2026-10-25 is the last Sunday of October: Prague leaves CEST
			// (+02) for CET (+01). 03:00 stays 03:00 on the wall clock, and
			// Sunday takes the weekly environments.
			name:  "daily in a zone across the DST change, environments weekly",
			plan:  Plan{Cadence: CadenceDaily, TimeOfDay: "03:00", Timezone: "Europe/Prague", EnvMode: backup.EnvModeComplete, EnvCadence: EnvWeekly},
			after: "2026-10-23T12:00:00Z",
			want:  []want{{"2026-10-24T01:00:00Z", false}, {"2026-10-25T02:00:00Z", true}, {"2026-10-26T02:00:00Z", false}},
		},
		{
			name:  "a time already passed today moves to tomorrow",
			plan:  Plan{Cadence: CadenceDaily, TimeOfDay: "03:00", Timezone: "UTC", EnvMode: backup.EnvModeFiles, EnvCadence: EnvWeekly},
			after: "2026-09-30T03:00:00Z",
			want:  []want{{"2026-10-01T03:00:00Z", false}},
		},
		{
			name:  "weekly on Wednesday",
			plan:  Plan{Cadence: CadenceWeekly, Weekday: intPtr(3), TimeOfDay: "22:30", Timezone: "UTC", EnvMode: backup.EnvModeComplete, EnvCadence: EnvMonthly},
			after: "2026-09-30T23:00:00Z",
			want:  []want{{"2026-10-07T22:30:00Z", true}, {"2026-10-14T22:30:00Z", true}},
		},
		{
			name:  "monthly with no day is the first Sunday",
			plan:  Plan{Cadence: CadenceMonthly, TimeOfDay: "04:00", Timezone: "UTC", EnvMode: backup.EnvModeFiles, EnvCadence: EnvWeekly},
			after: "2026-09-30T00:00:00Z",
			want:  []want{{"2026-10-04T04:00:00Z", false}, {"2026-11-01T04:00:00Z", false}},
		},
		{
			name:  "monthly on the 31st skips short months",
			plan:  Plan{Cadence: CadenceMonthly, Monthday: intPtr(31), TimeOfDay: "04:00", Timezone: "UTC", EnvMode: backup.EnvModeFiles, EnvCadence: EnvWeekly},
			after: "2026-10-31T05:00:00Z",
			want:  []want{{"2026-12-31T04:00:00Z", false}, {"2027-01-31T04:00:00Z", false}},
		},
		{
			// 2026-10-04 is a Sunday: only its first run takes environments.
			name:  "custom cron every 6 hours, environments with the first run of Sunday",
			plan:  Plan{Cadence: CadenceCustom, CronExpr: str("0 */6 * * *"), Timezone: "UTC", EnvMode: backup.EnvModeComplete, EnvCadence: EnvWeekly},
			after: "2026-10-03T19:00:00Z",
			want:  []want{{"2026-10-04T00:00:00Z", true}, {"2026-10-04T06:00:00Z", false}, {"2026-10-04T12:00:00Z", false}},
		},
		{
			name:  "custom cron evaluates in the plan's zone",
			plan:  Plan{Cadence: CadenceCustom, CronExpr: str("30 2 * * *"), Timezone: "America/New_York", EnvMode: backup.EnvModeFiles, EnvCadence: EnvWeekly},
			after: "2026-09-30T00:00:00Z",
			want:  []want{{"2026-09-30T06:30:00Z", false}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NextOccurrences(&tc.plan, mustTime(t, tc.after), len(tc.want))
			if len(got) != len(tc.want) {
				t.Fatalf("got %d occurrences, want %d: %+v", len(got), len(tc.want), got)
			}
			for i, w := range tc.want {
				if !got[i].At.Equal(mustTime(t, w.at)) || got[i].Environments != w.env {
					t.Errorf("[%d] = %s env=%v, want %s env=%v", i, got[i].At.Format(time.RFC3339), got[i].Environments, w.at, w.env)
				}
			}
		})
	}
}

func TestOccurrencesBetween(t *testing.T) {
	p := Plan{Cadence: CadenceDaily, TimeOfDay: "03:00", Timezone: "UTC", EnvMode: backup.EnvModeFiles}
	got := OccurrencesBetween(&p, mustTime(t, "2026-09-27T03:00:00Z"), mustTime(t, "2026-09-30T10:00:00Z"), 100)
	if len(got) != 4 || !got[0].At.Equal(mustTime(t, "2026-09-27T03:00:00Z")) || !got[3].At.Equal(mustTime(t, "2026-09-30T03:00:00Z")) {
		t.Fatalf("got %+v", got)
	}
}
