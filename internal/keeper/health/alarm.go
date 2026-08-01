package health

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/notify"
)

// Record is what the credential decision path calls: it tallies the verdict
// on the process-wide Default monitor and, when that trips an alarm, writes
// the inbox card on a DETACHED goroutine.
//
// Detached because the caller is an HTTP handler holding an agent's credential
// request open. The inbox write is a SQL round-trip; doing it inline would put
// the metric's latency in front of a decision that has already been made, and
// a slow or failing write would then be able to affect a request whose outcome
// no longer depends on it. Nothing here reports back to the caller for the
// same reason — there is no error a credential decision could sensibly act on.
//
// ctx is stripped of cancellation: the handler returns as soon as it has
// answered the agent, and a card that vanishes because the request finished
// first is the exact failure this metric exists to prevent. Its values are
// kept so request-scoped logging still correlates.
func Record(ctx context.Context, db *sql.DB, logger *slog.Logger, v Verdict) {
	alarm, ok := Default.Record(v)
	if !ok || db == nil {
		return
	}
	go func() {
		detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), alarmWriteTimeout)
		defer cancel()
		// A panic on this goroutine would take the whole server down, and it
		// would do so for a metric — recovering keeps a bug in the alarm from
		// outranking the credential path it is supposed to be watching.
		defer func() {
			if r := recover(); r != nil {
				logf(logger).Error("keeper health: alarm write panicked", "panic", r, "kind", alarm.Kind)
			}
		}()
		_ = Raise(detached, db, logger, alarm)
	}()
}

// alarmWriteTimeout bounds the detached inbox write. Generous — it is not
// racing anything — but finite, so a wedged database cannot leak goroutines
// one per alarm.
const alarmWriteTimeout = 30 * time.Second

// Raise projects an alarm into the inbox. Exported so a caller that already
// has an Alarm (a replay, a test, a future admin "check now" command) can
// write it without going through the hot path.
//
// A nil db or an empty alarm is a no-op, matching inbox.Insert's contract that
// envelope problems are caller bugs to swallow rather than errors to
// propagate.
func Raise(ctx context.Context, db *sql.DB, logger *slog.Logger, a Alarm) error {
	if db == nil || a.Kind == "" || a.WorkspaceID == "" {
		return nil
	}
	return inbox.Insert(ctx, db, logger, AlarmItem(a))
}

// AlarmItem projects an alarm onto the inbox row it becomes.
//
// Kind is an escalation rather than a message: the keeper path already writes
// its non-blocking high-risk DENY notices as escalations (see
// internal/api/keeper_request.go), and inbox_items.kind is CHECK-constrained,
// so inventing a kind here would need a migration and would fail the insert
// silently until it landed.
//
// Category overrides that kind's default of agents.escalation. This is not one
// agent's request needing a decision; it is the instance reporting that its own
// security layer has stopped behaving, which is what system.health names. The
// override also keeps it out of the escalation lane's rate-gate bypass — an
// alarm is worth one card, not a guaranteed push past every throttle.
//
// Blocking is false: there is nothing here to approve. The action is to go
// look at the judge.
func AlarmItem(a Alarm) inbox.Item {
	s := a.Stats
	return inbox.Item{
		WorkspaceID: a.WorkspaceID,
		Kind:        inbox.KindEscalation,
		SourceID:    alarmSourceID(a),
		// The judge is configured by OWNER/ADMIN (keeper config set), so this
		// goes to the people who can actually act on it rather than to every
		// manager who would only be able to forward it.
		TargetRole: "ADMIN",
		Category:   notify.CategorySystemHealth,
		Title:      "Keeper health: " + a.Summary,
		BodyMD:     alarmBody(a),
		SenderType: "system",
		SenderID:   "keeper",
		SenderName: "Keeper",
		Priority:   "high",
		Blocking:   false,
		Payload: map[string]interface{}{
			"alarm":              string(a.Kind),
			"samples":            s.Samples,
			"allow":              s.Allow,
			"deny":               s.Deny,
			"escalate":           s.Escalate,
			"other":              s.Other,
			"judge_failures":     s.JudgeFailures,
			"allow_rate":         s.AllowRate(),
			"deny_rate":          s.DenyRate(),
			"escalate_rate":      s.EscalateRate(),
			"judge_failure_rate": s.JudgeFailureRate(),
			"p95_latency_ms":     s.P95Latency.Milliseconds(),
			"window_from":        s.Oldest.UTC().Format(time.RFC3339),
			"window_to":          s.Newest.UTC().Format(time.RFC3339),
		},
	}
}

// alarmSourceID is the (kind, source_id) dedup key inbox.Insert uses. It is
// bucketed by AlarmCooldown so the DB enforces the same one-card-per-outage
// rule the in-memory cooldown does — that cooldown lives in a process, and a
// crash-loop restarting every minute would otherwise write a card per restart.
func alarmSourceID(a Alarm) string {
	bucket := a.At.UTC().Truncate(AlarmCooldown).Unix()
	return fmt.Sprintf("keeperhealth_%s_%s_%d", a.WorkspaceID, a.Kind, bucket)
}

func summaryAllowCollapse(s Stats) string {
	return fmt.Sprintf("only %.0f%% of the last %d credential decisions were granted",
		s.AllowRate()*100, s.Samples)
}

func summaryJudgeFailures(s Stats) string {
	return fmt.Sprintf("the judge failed to answer on %.0f%% of the last %d credential decisions",
		s.JudgeFailureRate()*100, s.Samples)
}

// alarmBody spells out what to check. An alarm that only says "something is
// wrong" costs an operator the same investigation every time it fires, so the
// distribution that raised it and the next step are both in the card.
func alarmBody(a Alarm) string {
	s := a.Stats
	var b strings.Builder
	fmt.Fprintf(&b, "Over the last %d Keeper decisions in this workspace:\n\n", s.Samples)
	fmt.Fprintf(&b, "- ALLOW: %d (%.0f%%)\n", s.Allow, s.AllowRate()*100)
	fmt.Fprintf(&b, "- DENY: %d (%.0f%%)\n", s.Deny, s.DenyRate()*100)
	fmt.Fprintf(&b, "- ESCALATE: %d (%.0f%%)\n", s.Escalate, s.EscalateRate()*100)
	if s.Other > 0 {
		fmt.Fprintf(&b, "- unrecognised verdict: %d\n", s.Other)
	}
	fmt.Fprintf(&b, "- judge did not answer (unreachable, timed out, or unparseable): %d (%.0f%%)\n",
		s.JudgeFailures, s.JudgeFailureRate()*100)
	fmt.Fprintf(&b, "- p95 verdict latency: %s\n\n", s.P95Latency.Round(time.Millisecond))

	switch a.Kind {
	case AlarmJudgeFailures:
		b.WriteString("Keeper is failing closed on its own fallback rather than on the model's " +
			"judgement. Check that the judge is reachable and answering in budget: " +
			"`crewship keeper judge test`.")
	default:
		b.WriteString("Keeper is refusing effectively every credential request. That is what a " +
			"broken judge looks like from the outside as well as a genuinely hostile week — " +
			"check `crewship keeper judge test` and the recent decisions in " +
			"`crewship keeper requests` before assuming the requests were bad.")
	}
	return b.String()
}

// logf keeps the panic handler usable when a caller passed no logger, matching
// inbox.Insert's "nil logger means the default" behaviour.
func logf(l *slog.Logger) *slog.Logger {
	if l == nil {
		return slog.Default()
	}
	return l
}
