package backupplan

import (
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
)

// Occurrence is one due time of a plan, and whether that run also takes
// complete container environments.
type Occurrence struct {
	At           time.Time
	Environments bool
}

// maxScanDays bounds the day-by-day walk for daily/weekly/monthly plans. A
// monthly plan on the 31st still hits within a few months.
const maxScanDays = 800

// NextOccurrences returns the plan's next n due times strictly after `after`,
// evaluated as wall-clock times in the plan's timezone (so 03:00 stays 03:00
// across a daylight-saving change; a 02:30 that does not exist on the
// spring-forward night runs at the first instant after the gap).
func NextOccurrences(p *Plan, after time.Time, n int) []Occurrence {
	var out []Occurrence
	if n <= 0 {
		return out
	}
	loc := p.Location()
	if p.Cadence == CadenceCustom {
		if p.CronExpr == nil {
			return out
		}
		sched, err := cronParser.Parse(*p.CronExpr)
		if err != nil {
			return out
		}
		t := after.In(loc)
		for len(out) < n {
			next := sched.Next(t)
			if next.IsZero() {
				break
			}
			out = append(out, Occurrence{At: next.UTC(), Environments: customEnv(p, sched.Next, next, loc)})
			t = next
		}
		return out
	}
	hh, mm := parseTimeOfDay(p.TimeOfDay)
	local := after.In(loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	for i := 0; i < maxScanDays && len(out) < n; i++ {
		d := day.AddDate(0, 0, i)
		if !hits(p, d) {
			continue
		}
		at := time.Date(d.Year(), d.Month(), d.Day(), hh, mm, 0, 0, loc)
		if !at.After(after) {
			continue
		}
		out = append(out, Occurrence{At: at.UTC(), Environments: dataRunEnv(p, d)})
	}
	return out
}

// OccurrencesBetween returns every due time in [from, to], capped at max.
func OccurrencesBetween(p *Plan, from, to time.Time, max int) []Occurrence {
	var out []Occurrence
	cursor := from.Add(-time.Nanosecond)
	for len(out) < max {
		next := NextOccurrences(p, cursor, 1)
		if len(next) == 0 || next[0].At.After(to) {
			break
		}
		out = append(out, next[0])
		cursor = next[0].At
	}
	return out
}

func parseTimeOfDay(s string) (int, int) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 3, 0
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return 3, 0
	}
	return h, m
}

func firstSunday(d time.Time) bool {
	return d.Weekday() == time.Sunday && d.Day() <= 7
}

// hits reports whether day d (midnight in the plan's zone) has a data run.
func hits(p *Plan, d time.Time) bool {
	switch p.Cadence {
	case CadenceDaily:
		return true
	case CadenceWeekly:
		wd := 0
		if p.Weekday != nil {
			wd = *p.Weekday
		}
		return int(d.Weekday()) == wd
	case CadenceMonthly:
		if p.Monthday != nil {
			return d.Day() == *p.Monthday
		}
		return firstSunday(d)
	}
	return false
}

// envDay reports whether day d is an environments day under env_cadence:
// every day, Sundays (weekly) or the first Sunday of the month (monthly).
func envDay(p *Plan, d time.Time) bool {
	switch p.EnvCadence {
	case EnvEvery:
		return true
	case EnvWeekly:
		return d.Weekday() == time.Sunday
	case EnvMonthly:
		return firstSunday(d)
	}
	return false
}

// dataRunEnv: with environments on, a daily plan takes them on its
// env_cadence days; a weekly or monthly plan with every run (the same rule
// the console's computeNextRuns applies).
func dataRunEnv(p *Plan, d time.Time) bool {
	if p.EnvMode != backup.EnvModeComplete {
		return false
	}
	if p.Cadence == CadenceDaily {
		return envDay(p, d)
	}
	return true
}

// customEnv: a cron plan takes environments with the first run of each
// environments day (every run for env_cadence=every).
func customEnv(p *Plan, next func(time.Time) time.Time, at time.Time, loc *time.Location) bool {
	if p.EnvMode != backup.EnvModeComplete {
		return false
	}
	local := at.In(loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	if !envDay(p, day) {
		return false
	}
	if p.EnvCadence == EnvEvery {
		return true
	}
	return next(day.Add(-time.Nanosecond)).Equal(at)
}
