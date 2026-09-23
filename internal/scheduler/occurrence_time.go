package scheduler

import (
	"errors"
	"time"

	"github.com/robfig/cron/v3"
)

// NextOccurrence uses the same local time zone as the cron scheduler. The
// persisted cursor is converted to UTC only after the cron plan is evaluated.
func NextOccurrence(expr string, after time.Time) (time.Time, error) {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	plan, err := parser.Parse(expr)
	if err != nil {
		return time.Time{}, err
	}
	next := plan.Next(after.In(time.Local))
	if next.IsZero() {
		return time.Time{}, errors.New("schedule_cron has no next occurrence")
	}
	return next.UTC(), nil
}
