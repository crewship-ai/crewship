package scheduler

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/work"
)

type mutableScheduleLeader struct{ yes atomic.Bool }

func (m *mutableScheduleLeader) IsLeader() bool { return m.yes.Load() }

func TestOverdueSweep_AcceptsWhenFollowerBecomesLeader(t *testing.T) {
	db, _ := dueFixture(t)
	if _, err := db.Exec(`UPDATE agents SET schedule_cron='0 8 * * *' WHERE id='a1'`); err != nil {
		t.Fatal(err)
	}
	s := newTestScheduler(db, &mockResolver{}, nil, nil)
	configureDurableCron(t, s, db)
	s.nowFn = func() time.Time { return acceptanceNow }
	s.overdueSweepInterval = 20 * time.Millisecond
	gate := &mutableScheduleLeader{}
	s.SetLeaderGate(gate)
	if err := s.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	time.Sleep(100 * time.Millisecond)
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM work_items WHERE source='schedule'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("follower accepted %d scheduled items", count)
	}
	gate.yes.Store(true)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := db.QueryRow(`SELECT COUNT(*) FROM work_items WHERE source='schedule'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("new leader left overdue schedule unaccepted until next daily tick")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestInitializeMissingCursors_OnlyMissingValues(t *testing.T) {
	db, _ := dueFixture(t)
	s := newTestScheduler(db, &mockResolver{}, nil, nil)
	if err := s.InitializeMissingCursors(t.Context(), acceptanceNow); err != nil {
		t.Fatal(err)
	}
	_, next := agentSchedule(t, db, "a1")
	if next.String != acceptanceDue {
		t.Fatalf("existing overdue occurrence was overwritten: %q", next.String)
	}
	for _, missing := range []string{"NULL", "''"} {
		if _, err := db.Exec(`UPDATE agents SET schedule_next_run=` + missing + ` WHERE id='a1'`); err != nil {
			t.Fatal(err)
		}
		if err := s.InitializeMissingCursors(t.Context(), acceptanceNow); err != nil {
			t.Fatal(err)
		}
		_, next = agentSchedule(t, db, "a1")
		parsed, err := time.Parse(time.RFC3339Nano, next.String)
		if err != nil || !parsed.After(acceptanceNow) {
			t.Fatalf("missing cursor initialized to %q: %v", next.String, err)
		}
	}
}

func TestInitializeMissingCursors_InvalidCronDoesNotBlockOtherSchedules(t *testing.T) {
	db, _ := dueFixture(t)
	if _, err := db.Exec(`UPDATE agents SET schedule_cron='invalid cron', schedule_next_run=NULL WHERE id='a1'`); err != nil {
		t.Fatal(err)
	}
	s := newTestScheduler(db, &mockResolver{}, nil, nil)
	if err := s.InitializeMissingCursors(t.Context(), acceptanceNow); err != nil {
		t.Fatalf("one bad schedule took startup down: %v", err)
	}
	_, next := agentSchedule(t, db, "a1")
	if next.Valid {
		t.Fatalf("invented due identity for malformed schedule: %v", next)
	}
}

func TestStart_AcceptsPersistedOverdueOccurrenceBeforeNextCronTick(t *testing.T) {
	db, _ := dueFixture(t)
	// A daily cron will not fire during this short test. Acceptance must come
	// from the boot sweep, using the persisted due identity.
	if _, err := db.Exec(`UPDATE agents SET schedule_cron='0 8 * * *' WHERE id='a1'`); err != nil {
		t.Fatal(err)
	}
	s := newTestScheduler(db, &mockResolver{}, nil, nil)
	configureDurableCron(t, s, db)
	s.nowFn = func() time.Time { return acceptanceNow }
	if err := s.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	deadline := time.Now().Add(2 * time.Second)
	for {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM work_items WHERE source='schedule'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("overdue occurrence remained unaccepted until the next daily cron tick")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM work_items WHERE source='schedule'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(work.StateQueued) {
		t.Fatalf("boot acceptance state=%s", state)
	}
}

func TestStart_EnabledScheduleWithoutDurableAdmissionFailsClosed(t *testing.T) {
	db, _ := dueFixture(t)
	s := newTestScheduler(db, &mockResolver{}, nil, nil)
	err := s.Start(t.Context())
	if err == nil || !strings.Contains(err.Error(), "durable acceptance") {
		t.Fatalf("enabled schedule started without a work owner: %v", err)
	}
}
