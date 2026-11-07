package pipeline

import (
	"fmt"
	"hash/fnv"
	"time"

	"github.com/robfig/cron/v3"
)

// Spread window (pipeline_schedules.spread_seconds). A schedule with a spread
// is picked up at its nominal cron occurrence PLUS a stable offset in
// [0, spread), derived from the schedule id, so schedules sharing a cron
// expression do not all fire in the same second.
//
// The offset only moves WHEN an occurrence is picked up. The occurrence
// itself is still the nominal cron time: next_run_at, the idempotency bucket
// (ScheduledFireIdempotencyKey), the durable occurrence identity and the
// catch-up arithmetic all keep using it unchanged. Because spread is validated
// to be shorter than the cron interval, a delayed pick-up never crosses into
// the next occurrence.
//
// The scheduler polls every spreadTickSlack, so an occurrence whose offset has
// elapsed is picked up up to one poll later. A spread is therefore only valid
// when its largest offset plus one poll still lands before the next
// occurrence; rows stored under an older, looser rule are clamped at runtime
// (EffectiveSpreadOffset).

// spreadScanHorizon bounds how far ahead the cron interval is measured.
const (
	// spreadTickSlack is the scheduler poll period (PipelineScheduler.run).
	spreadTickSlack       = 30 * time.Second
	spreadScanOccurrences = 128
	spreadScanHorizon     = 400 * 24 * time.Hour
)

// ScheduleSpreadOffset returns the stable offset in seconds, within
// [0, spreadSeconds), for the schedule id. spreadSeconds <= 0 means no spread.
func ScheduleSpreadOffset(scheduleID string, spreadSeconds int) int {
	if spreadSeconds <= 0 {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(scheduleID))
	return int(h.Sum64() % uint64(spreadSeconds))
}

// EffectiveNextRunAt is when the schedule will actually be picked up next: the
// nominal next_run_at plus its spread offset. It is what operators should see.
func (s *Schedule) EffectiveNextRunAt() *time.Time {
	if s.NextRunAt == nil {
		return nil
	}
	t := s.NextRunAt.Add(time.Duration(EffectiveSpreadOffset(s)) * time.Second)
	return &t
}

// EffectiveSpreadOffset is the stored offset clamped so that pick-up (offset
// plus one scheduler poll) cannot reach the next occurrence. Rows created
// before the poll slack was part of validation may carry a larger offset.
func EffectiveSpreadOffset(s *Schedule) int {
	if s.SpreadOffsetSeconds <= 0 {
		return 0
	}
	interval, ok, err := MinCronInterval(s.CronExpr, s.Timezone, time.Now())
	if err != nil || !ok {
		return s.SpreadOffsetSeconds
	}
	return clampSpreadOffset(s.SpreadOffsetSeconds, interval)
}

func clampSpreadOffset(offset int, interval time.Duration) int {
	limit := int((interval-spreadTickSlack)/time.Second) - 1
	if limit < 0 {
		limit = 0
	}
	if offset > limit {
		return limit
	}
	return offset
}

// MinCronInterval is the shortest gap between consecutive occurrences of the
// expression over the next ~400 days (or 128 occurrences). ok is false when
// fewer than two occurrences fall in that window, i.e. the expression is rarer
// than the horizon and any sane spread is shorter than its interval.
func MinCronInterval(cronExpr, timezone string, from time.Time) (d time.Duration, ok bool, err error) {
	if timezone == "" {
		timezone = "UTC"
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return 0, false, fmt.Errorf("invalid timezone %q: %w", timezone, err)
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	sched, err := parser.Parse(cronExpr)
	if err != nil {
		return 0, false, fmt.Errorf("invalid cron expression %q: %w", cronExpr, err)
	}
	prev := sched.Next(from.In(loc))
	limit := from.Add(spreadScanHorizon)
	for i := 0; i < spreadScanOccurrences; i++ {
		next := sched.Next(prev)
		if next.IsZero() || next.After(limit) {
			break
		}
		if gap := next.Sub(prev); !ok || gap < d {
			d, ok = gap, true
		}
		prev = next
	}
	return d, ok, nil
}

// validateSpread rejects a negative spread and one that, plus one scheduler poll,
// exceeds the shortest interval of the cron expression. Errors wrap
// ErrInvalidTrigger so API callers answer 400/422, not 500.
func validateSpread(cronExpr, timezone string, spreadSeconds int) error {
	if spreadSeconds < 0 {
		return fmt.Errorf("%w: spread_seconds must be >= 0", ErrInvalidTrigger)
	}
	if spreadSeconds == 0 {
		return nil
	}
	interval, ok, err := MinCronInterval(cronExpr, timezone, time.Now())
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidTrigger, err)
	}
	if ok && time.Duration(spreadSeconds)*time.Second+spreadTickSlack > interval {
		return fmt.Errorf("%w: spread_seconds (%d) plus the %d second scheduler poll must not exceed the shortest cron interval (%d seconds)",
			ErrInvalidTrigger, spreadSeconds, int(spreadTickSlack/time.Second), int(interval/time.Second))
	}
	return nil
}
