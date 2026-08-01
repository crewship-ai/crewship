package health

import (
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/keeper"
)

// base is a fixed clock so every test drives Verdict.At explicitly — the
// cooldown and the rolling window are both time-sensitive, and a test that
// leans on time.Now() cannot pin either.
var base = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

// feed records n verdicts of one decision, one second apart, and returns the
// last alarm the monitor raised (if any).
func feed(m *Monitor, ws string, decision string, n int, start time.Time) (Alarm, bool) {
	var last Alarm
	var raised bool
	for i := 0; i < n; i++ {
		a, ok := m.Record(Verdict{
			WorkspaceID: ws,
			Decision:    decision,
			Latency:     time.Second,
			At:          start.Add(time.Duration(i) * time.Second),
		})
		if ok {
			last, raised = a, true
		}
	}
	return last, raised
}

func TestRecordCountsDecisionShares(t *testing.T) {
	m := NewMonitor(DefaultWindowSize)
	feed(m, "ws1", string(keeper.DecisionAllow), 6, base)
	feed(m, "ws1", string(keeper.DecisionDeny), 3, base.Add(time.Minute))
	feed(m, "ws1", string(keeper.DecisionEscalate), 1, base.Add(2*time.Minute))

	s, ok := m.Snapshot("ws1")
	if !ok {
		t.Fatal("Snapshot(ws1) reports no data after 10 verdicts")
	}
	if s.Samples != 10 || s.Allow != 6 || s.Deny != 3 || s.Escalate != 1 {
		t.Fatalf("counts = %+v, want 10 samples / 6 allow / 3 deny / 1 escalate", s)
	}
	if got := s.AllowRate(); got != 0.6 {
		t.Errorf("AllowRate() = %v, want 0.6", got)
	}
	if got := s.DenyRate(); got != 0.3 {
		t.Errorf("DenyRate() = %v, want 0.3", got)
	}
	if got := s.EscalateRate(); got != 0.1 {
		t.Errorf("EscalateRate() = %v, want 0.1", got)
	}
}

// TestColdStartDoesNotAlarm is the false-alarm guard. A fresh instance that
// has judged four requests — all denied, because the four happened to be
// bad — is not evidence Keeper is broken, and paging on it teaches operators
// to ignore the alarm that matters.
func TestColdStartDoesNotAlarm(t *testing.T) {
	m := NewMonitor(DefaultWindowSize)
	if _, raised := feed(m, "ws1", string(keeper.DecisionDeny), 4, base); raised {
		t.Fatal("4 DENYs raised an alarm; a cold instance must stay quiet")
	}
	// One short of the quorum still stays quiet...
	m2 := NewMonitor(DefaultWindowSize)
	if _, raised := feed(m2, "ws1", string(keeper.DecisionDeny), MinSamples-1, base); raised {
		t.Fatalf("%d DENYs raised an alarm; MinSamples is %d", MinSamples-1, MinSamples)
	}
	// ...and the very next one fires it.
	if _, raised := feed(m2, "ws1", string(keeper.DecisionDeny), 1, base.Add(time.Hour)); !raised {
		t.Fatalf("%d DENYs did not raise an alarm", MinSamples)
	}
}

func TestAllowCollapseAlarm(t *testing.T) {
	m := NewMonitor(DefaultWindowSize)
	a, raised := feed(m, "ws1", string(keeper.DecisionDeny), MinSamples, base)
	if !raised {
		t.Fatal("a window of pure DENY raised no alarm")
	}
	if a.Kind != AlarmAllowCollapse {
		t.Errorf("Kind = %q, want %q", a.Kind, AlarmAllowCollapse)
	}
	if a.WorkspaceID != "ws1" {
		t.Errorf("WorkspaceID = %q, want ws1", a.WorkspaceID)
	}
	if a.Stats.Samples != MinSamples || a.Stats.AllowRate() != 0 {
		t.Errorf("Stats = %+v, want %d samples at 0 allow rate", a.Stats, MinSamples)
	}
	if a.Summary == "" {
		t.Error("Summary is empty; the inbox card would say nothing")
	}
}

// TestJudgeFailureAlarmWinsOverAllowCollapse pins which of the two alarms is
// reported when both conditions hold. A judge that cannot be parsed denies
// everything by design, so "allow rate is zero" is the symptom and "the judge
// is not answering" is the cause; reporting the symptom would send an
// operator to the policy instead of the model.
func TestJudgeFailureAlarmWinsOverAllowCollapse(t *testing.T) {
	m := NewMonitor(DefaultWindowSize)
	var last Alarm
	var raised bool
	for i := 0; i < MinSamples; i++ {
		a, ok := m.Record(Verdict{
			WorkspaceID: "ws1",
			Decision:    string(keeper.DecisionDeny),
			JudgeFailed: true,
			At:          base.Add(time.Duration(i) * time.Second),
		})
		if ok {
			last, raised = a, true
		}
	}
	if !raised {
		t.Fatal("a window of unusable judge responses raised no alarm")
	}
	if last.Kind != AlarmJudgeFailures {
		t.Fatalf("Kind = %q, want %q — the cause, not the symptom", last.Kind, AlarmJudgeFailures)
	}
	if last.Stats.JudgeFailureRate() != 1 {
		t.Errorf("JudgeFailureRate() = %v, want 1", last.Stats.JudgeFailureRate())
	}
}

// TestJudgeFailureAlarmUnderHealthyAllowRate proves the parse-failure alarm is
// not merely the allow-collapse alarm under another name: a judge that answers
// usefully most of the time still gets reported when a quarter of its answers
// come back unusable.
func TestJudgeFailureAlarmUnderHealthyAllowRate(t *testing.T) {
	m := NewMonitor(DefaultWindowSize)
	var last Alarm
	var raised bool
	for i := 0; i < 40; i++ {
		v := Verdict{
			WorkspaceID: "ws1",
			Decision:    string(keeper.DecisionAllow),
			At:          base.Add(time.Duration(i) * time.Second),
		}
		if i%3 == 0 { // 14 of 40 = 35%, over the threshold
			v.Decision = string(keeper.DecisionDeny)
			v.JudgeFailed = true
		}
		if a, ok := m.Record(v); ok {
			last, raised = a, true
		}
	}
	if !raised {
		t.Fatal("35% unusable judge responses raised no alarm")
	}
	if last.Kind != AlarmJudgeFailures {
		t.Errorf("Kind = %q, want %q", last.Kind, AlarmJudgeFailures)
	}
	if last.Stats.AllowRate() < AlarmAllowRate {
		t.Fatalf("test is not proving what it claims: allow rate %v is itself under the floor",
			last.Stats.AllowRate())
	}
}

func TestHealthyDistributionDoesNotAlarm(t *testing.T) {
	m := NewMonitor(DefaultWindowSize)
	for i := 0; i < 100; i++ {
		d := string(keeper.DecisionAllow)
		switch i % 4 {
		case 1:
			d = string(keeper.DecisionDeny)
		case 2:
			d = string(keeper.DecisionEscalate)
		}
		if _, ok := m.Record(Verdict{
			WorkspaceID: "ws1",
			Decision:    d,
			At:          base.Add(time.Duration(i) * time.Second),
		}); ok {
			t.Fatalf("healthy mix alarmed at sample %d", i)
		}
	}
}

// TestWindowIsRolling proves the window forgets: an instance that was broken
// and got fixed must stop alarming without anyone clearing state, which is the
// whole reason the metric is a rolling window rather than a lifetime counter.
func TestWindowIsRolling(t *testing.T) {
	m := NewMonitor(20)
	feed(m, "ws1", string(keeper.DecisionDeny), 20, base)
	s, _ := m.Snapshot("ws1")
	if s.Samples != 20 || s.Deny != 20 {
		t.Fatalf("after 20 denies: %+v", s)
	}
	feed(m, "ws1", string(keeper.DecisionAllow), 20, base.Add(time.Hour))
	s, _ = m.Snapshot("ws1")
	if s.Samples != 20 || s.Allow != 20 || s.Deny != 0 {
		t.Fatalf("after 20 more allows the window should hold only allows, got %+v", s)
	}
	if _, ok := s.Alarm(); ok {
		t.Error("a recovered instance still reports an alarm")
	}
}

func TestP95Latency(t *testing.T) {
	m := NewMonitor(DefaultWindowSize)
	// 19 fast verdicts and one slow one: at n=20 the 95th percentile is the
	// 19th value by nearest rank, so a single outlier must NOT set it.
	for i := 0; i < 19; i++ {
		m.Record(Verdict{WorkspaceID: "ws1", Decision: string(keeper.DecisionAllow),
			Latency: time.Duration(i+1) * time.Second, At: base.Add(time.Duration(i) * time.Second)})
	}
	m.Record(Verdict{WorkspaceID: "ws1", Decision: string(keeper.DecisionAllow),
		Latency: 90 * time.Second, At: base.Add(20 * time.Second)})

	s, _ := m.Snapshot("ws1")
	if s.P95Latency != 19*time.Second {
		t.Errorf("P95Latency = %v, want 19s (nearest rank over 20 samples)", s.P95Latency)
	}
}

func TestWorkspacesAreIsolated(t *testing.T) {
	m := NewMonitor(DefaultWindowSize)
	feed(m, "ws1", string(keeper.DecisionDeny), MinSamples, base)
	feed(m, "ws2", string(keeper.DecisionAllow), 5, base)

	s2, ok := m.Snapshot("ws2")
	if !ok {
		t.Fatal("ws2 has no snapshot")
	}
	if s2.Samples != 5 || s2.Allow != 5 {
		t.Fatalf("ws2 stats = %+v, want 5 allows — ws1's denies leaked", s2)
	}
	if _, ok := s2.Alarm(); ok {
		t.Error("ws2 alarmed on ws1's decisions")
	}
}

// TestAlarmCooldown: the condition stays true for as long as the window holds
// bad samples, so without a cooldown every subsequent credential request would
// mint another inbox card for the same outage.
func TestAlarmCooldown(t *testing.T) {
	m := NewMonitor(DefaultWindowSize)
	if _, raised := feed(m, "ws1", string(keeper.DecisionDeny), MinSamples, base); !raised {
		t.Fatal("no first alarm")
	}
	if _, raised := feed(m, "ws1", string(keeper.DecisionDeny), 10, base.Add(time.Minute)); raised {
		t.Error("alarmed again inside the cooldown window")
	}
	if _, raised := feed(m, "ws1", string(keeper.DecisionDeny), 1, base.Add(AlarmCooldown+time.Minute)); !raised {
		t.Error("did not re-alarm after the cooldown elapsed; a still-broken Keeper goes quiet forever")
	}
}

// TestRecordIgnoresUnusableInput: Record sits on the credential hot path, so
// nothing it is handed may panic or be treated as a real verdict.
func TestRecordIgnoresUnusableInput(t *testing.T) {
	var nilMon *Monitor
	if _, ok := nilMon.Record(Verdict{WorkspaceID: "ws1", Decision: "ALLOW"}); ok {
		t.Error("a nil monitor reported an alarm")
	}
	m := NewMonitor(DefaultWindowSize)
	m.Record(Verdict{Decision: "ALLOW"})                // no workspace
	m.Record(Verdict{WorkspaceID: "ws1", Decision: ""}) // no decision
	if _, ok := m.Snapshot("ws1"); ok {
		t.Error("a verdict with no decision was counted as a sample")
	}
}

// TestUnknownDecisionIsNotCountedAsAllow: NormalizeRawResponse forces unknown
// verdicts to DENY, but this metric exists precisely for the failures nobody
// predicted — a decision string it does not recognise must never inflate the
// allow rate and mask the alarm.
func TestUnknownDecisionIsNotCountedAsAllow(t *testing.T) {
	m := NewMonitor(DefaultWindowSize)
	for i := 0; i < MinSamples; i++ {
		m.Record(Verdict{WorkspaceID: "ws1", Decision: "MAYBE",
			At: base.Add(time.Duration(i) * time.Second)})
	}
	s, _ := m.Snapshot("ws1")
	if s.Allow != 0 || s.Other != MinSamples {
		t.Fatalf("stats = %+v, want all %d in Other", s, MinSamples)
	}
	if s.AllowRate() != 0 {
		t.Errorf("AllowRate() = %v, want 0", s.AllowRate())
	}
	if _, ok := s.Alarm(); !ok {
		t.Error("a window of unrecognised verdicts did not alarm")
	}
}

func TestWorkspaceTrackingIsBounded(t *testing.T) {
	m := NewMonitor(DefaultWindowSize)
	for i := 0; i < MaxWorkspaces*2; i++ {
		m.Record(Verdict{
			WorkspaceID: "ws" + time.Duration(i).String(),
			Decision:    string(keeper.DecisionAllow),
			At:          base.Add(time.Duration(i) * time.Second),
		})
	}
	if n := m.TrackedWorkspaces(); n > MaxWorkspaces {
		t.Errorf("TrackedWorkspaces() = %d, want at most %d", n, MaxWorkspaces)
	}
}
