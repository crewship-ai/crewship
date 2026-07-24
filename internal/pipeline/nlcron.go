package pipeline

// NL→cron conversion (#1422 item 1). Self-documented as
// designed-but-unshipped in docs/guides/routines.mdx's Limitations
// section ("No NL→cron converter — ops still hand-type `0 9 * * *`");
// this is that converter's first increment. Deliberately NOT a general
// NLP parser — no external dependency, just a small hand-rolled set of
// regexes for the handful of phrasings that cover the common onboarding
// asks ("every weekday at 9", "every day at 9am", "every monday at 14:00").
// Anything outside that set returns ErrNLCronUnrecognized so the caller
// can fall back to asking for a raw cron expression instead of silently
// guessing wrong.

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// ErrNLCronUnrecognized is returned when the phrase doesn't match any of
// the supported patterns.
var ErrNLCronUnrecognized = errors.New("nlcron: could not understand that schedule phrase — try a form like " +
	`"every weekday at 9", "every day at 9am", "every monday at 14:00", "every hour", or "every 15 minutes", ` +
	"or pass a raw cron expression with --cron instead")

var weekdayNames = map[string]int{
	"sunday": 0, "monday": 1, "tuesday": 2, "wednesday": 3,
	"thursday": 4, "friday": 5, "saturday": 6,
}

var (
	reEveryDayAt     = regexp.MustCompile(`^every\s+day\s+at\s+(.+)$`)
	reEveryWeekdayAt = regexp.MustCompile(`^every\s+weekday\s+at\s+(.+)$`)
	reEveryWeekendAt = regexp.MustCompile(`^every\s+weekend\s+at\s+(.+)$`)
	reEveryNamedDay  = regexp.MustCompile(`^every\s+(sunday|monday|tuesday|wednesday|thursday|friday|saturday)\s+at\s+(.+)$`)
	reEveryHour      = regexp.MustCompile(`^every\s+hour$`)
	reEveryNMinutes  = regexp.MustCompile(`^every\s+(\d{1,2})\s+minutes?$`)
	reEveryNHours    = regexp.MustCompile(`^every\s+(\d{1,2})\s+hours?$`)
	reTimeOfDay      = regexp.MustCompile(`^(\d{1,2})(?::(\d{2}))?\s*(am|pm)?$`)
)

// ParseNaturalCron converts a small set of common English schedule
// phrases into a 5-field cron expression. The result is always
// independently re-parseable by the real cron parser used at
// schedule-save time (see TestParseNaturalCron_OutputIsValidCron) —
// this function never hands back something schedules.go would reject.
func ParseNaturalCron(phrase string) (string, error) {
	norm := normalizeNLPhrase(phrase)
	if norm == "" {
		return "", ErrNLCronUnrecognized
	}

	if m := reEveryDayAt.FindStringSubmatch(norm); m != nil {
		h, min, err := parseTimeOfDay(m[1])
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d %d * * *", min, h), nil
	}
	if m := reEveryWeekdayAt.FindStringSubmatch(norm); m != nil {
		h, min, err := parseTimeOfDay(m[1])
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d %d * * 1-5", min, h), nil
	}
	if m := reEveryWeekendAt.FindStringSubmatch(norm); m != nil {
		h, min, err := parseTimeOfDay(m[1])
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d %d * * 0,6", min, h), nil
	}
	if m := reEveryNamedDay.FindStringSubmatch(norm); m != nil {
		dow := weekdayNames[m[1]]
		h, min, err := parseTimeOfDay(m[2])
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d %d * * %d", min, h, dow), nil
	}
	if reEveryHour.MatchString(norm) {
		return "0 * * * *", nil
	}
	if m := reEveryNMinutes.FindStringSubmatch(norm); m != nil {
		n, _ := strconv.Atoi(m[1])
		if n < 1 || n > 59 {
			return "", fmt.Errorf("nlcron: minute interval must be 1-59 (got %d) — use \"every hour\" for 60", n)
		}
		return fmt.Sprintf("*/%d * * * *", n), nil
	}
	if m := reEveryNHours.FindStringSubmatch(norm); m != nil {
		n, _ := strconv.Atoi(m[1])
		if n < 1 || n > 23 {
			return "", fmt.Errorf("nlcron: hour interval must be 1-23 (got %d)", n)
		}
		return fmt.Sprintf("0 */%d * * *", n), nil
	}
	return "", ErrNLCronUnrecognized
}

// normalizeNLPhrase lowercases, trims, and collapses internal whitespace so
// "  Every   Monday  At 2pm " matches the same regexes as "every monday at 2pm".
func normalizeNLPhrase(phrase string) string {
	fields := strings.Fields(strings.ToLower(phrase))
	return strings.Join(fields, " ")
}

// parseTimeOfDay accepts "9", "9am", "9:30am", "09:00", "14:00" and returns
// (hour 0-23, minute 0-59).
func parseTimeOfDay(s string) (int, int, error) {
	m := reTimeOfDay.FindStringSubmatch(s)
	if m == nil {
		return 0, 0, fmt.Errorf("nlcron: could not parse time of day %q (try \"9am\", \"9:30am\", or \"14:00\")", s)
	}
	hour, _ := strconv.Atoi(m[1])
	minute := 0
	if m[2] != "" {
		minute, _ = strconv.Atoi(m[2])
	}
	meridiem := m[3]
	if meridiem != "" {
		if hour < 1 || hour > 12 {
			return 0, 0, fmt.Errorf("nlcron: hour %d out of range for %s (must be 1-12)", hour, meridiem)
		}
		switch meridiem {
		case "am":
			if hour == 12 {
				hour = 0
			}
		case "pm":
			if hour != 12 {
				hour += 12
			}
		}
	} else if hour < 0 || hour > 23 {
		return 0, 0, fmt.Errorf("nlcron: hour %d out of range (must be 0-23, or add am/pm)", hour)
	}
	if minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("nlcron: minute %d out of range (must be 0-59)", minute)
	}
	return hour, minute, nil
}

// NextOccurrences computes the next n fire times for a cron expression in
// the given IANA timezone, starting strictly after `from`. Shared by the
// `--when` NL confirmation flow and can be reused anywhere a caller wants
// "what will this cron actually fire" without duplicating the parser setup
// that schedules.go's ScheduleStore.Save uses.
func NextOccurrences(cronExpr, timezone string, n int, from time.Time) ([]time.Time, error) {
	if timezone == "" {
		timezone = "UTC"
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, fmt.Errorf("invalid timezone %q: %w", timezone, err)
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	sched, err := parser.Parse(cronExpr)
	if err != nil {
		return nil, fmt.Errorf("invalid cron expression %q: %w", cronExpr, err)
	}
	if n < 0 {
		n = 0
	}
	t := from.In(loc)
	out := make([]time.Time, 0, n)
	for i := 0; i < n; i++ {
		t = sched.Next(t)
		out = append(out, t)
	}
	return out, nil
}
