package retention

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestWindowEqualityDistinguishesForeverFromFiniteValues(t *testing.T) {
	for _, tc := range []struct {
		a, b  *int
		equal bool
	}{
		{nil, nil, true}, {nil, days(30), false}, {days(30), nil, false}, {days(30), days(30), true}, {days(30), days(31), false},
	} {
		if Equal(tc.a, tc.b) != tc.equal {
			t.Fatalf("Equal(%v,%v)", val(tc.a), val(tc.b))
		}
	}
	_, err := ParsePatch(map[string]json.RawMessage{"unknown": json.RawMessage(`30`)})
	if err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("validation error lost field: %v", err)
	}
}

func TestDefaultsDiscardInvalidStoredWindowsAndRejectCorruptJSON(t *testing.T) {
	db := newDB(t)
	raw := map[Key]any{"unknown": 20, RoutineRuns: nil, Chats: 0, Inbox: 20, Audit: nil, MemoryVersions: MaxDays + 1}
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	exec(t, db, `INSERT INTO app_settings(key,value) VALUES (?,?)`, DefaultsSettingKey, string(b))
	got, configured, err := Defaults(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if len(configured) != 2 || !Equal(got[Inbox], days(20)) || got[Audit] != nil || !Equal(got[RoutineRuns], ProductDefaults()[RoutineRuns]) || got[Chats] != nil {
		t.Fatalf("invalid defaults applied: %+v %v", got, configured)
	}
	exec(t, db, `UPDATE app_settings SET value='broken' WHERE key=?`, DefaultsSettingKey)
	if _, _, err := Defaults(t.Context(), db); err == nil {
		t.Fatal("corrupt defaults accepted")
	}
	if err := MergeDefaults(t.Context(), db, Windows{Inbox: days(14)}, time.Now()); err == nil {
		t.Fatal("merge silently replaced corrupt configuration")
	}
	if err := ApplyDefaults(t.Context(), db, "ws1"); err == nil {
		t.Fatal("workspace defaults silently ignored corrupt configuration")
	}
}

func TestRetentionStoreRecoversCorruptMemoryDocumentAndReportsWriteFailure(t *testing.T) {
	db := newDB(t)
	for _, raw := range []string{"broken", "null"} {
		exec(t, db, `UPDATE workspaces SET memory_config=? WHERE id='ws1'`, raw)
		if err := Store(t.Context(), db, "ws1", MemoryVersions, days(14), "", time.Now()); err != nil {
			t.Fatal(err)
		}
		var saved string
		if err := db.QueryRow(`SELECT memory_config FROM workspaces WHERE id='ws1'`).Scan(&saved); err != nil {
			t.Fatal(err)
		}
		if saved != `{"versions_retention_days":14}` {
			t.Fatalf("recovery=%s", saved)
		}
	}
	if err := Store(t.Context(), db, "missing", MemoryVersions, days(14), "", time.Now()); err == nil {
		t.Fatal("missing workspace accepted")
	}
	if err := MergeDefaults(t.Context(), db, Windows{Inbox: days(14)}, time.Now()); err != nil {
		t.Fatal(err)
	}
	exec(t, db, `CREATE TRIGGER reject_retention BEFORE INSERT ON retention_settings BEGIN SELECT RAISE(ABORT,'read only'); END`)
	if err := ApplyDefaults(t.Context(), db, "ws1"); err == nil {
		t.Fatal("failed default write reported success")
	}
	exec(t, db, `CREATE TRIGGER reject_defaults BEFORE UPDATE ON app_settings BEGIN SELECT RAISE(ABORT,'read only'); END`)
	if err := MergeDefaults(t.Context(), db, Windows{Inbox: days(30)}, time.Now()); err == nil {
		t.Fatal("failed defaults merge reported success")
	}
}

type sweepLogSignal struct {
	slog.Handler
	messages chan string
}

func (h sweepLogSignal) Handle(_ context.Context, r slog.Record) error {
	select {
	case h.messages <- r.Message:
	default:
	}
	return nil
}

func TestRetentionSweeperReportsInitialAndPeriodicFailuresAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	messages := make(chan string, 8)
	logger := slog.New(sweepLogSignal{Handler: slog.Default().Handler(), messages: messages})
	done := make(chan struct{})
	go func() { defer close(done); StartSweeper(ctx, nil, logger, time.Millisecond) }()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for _, want := range []string{"retention sweeper: initial sweep failed", "retention sweeper: tick failed"} {
		select {
		case got := <-messages:
			if got != want {
				t.Fatalf("log=%s want=%s", got, want)
			}
		case <-deadline.C:
			t.Fatal("sweeper did not report failure")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("sweeper did not stop")
	}
	// A canceled caller must return even when using the default daily interval.
	StartSweeper(ctx, nil, nil, 0)
	if err := SweepAll(ctx, newDB(t), nil, time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
}

func TestRetentionClosedDatabaseErrorsRemainVisible(t *testing.T) {
	db := newDB(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Defaults(t.Context(), db); err == nil {
		t.Fatal("closed defaults store accepted")
	}
	if _, err := Load(t.Context(), db, "ws1"); err == nil {
		t.Fatal("closed workspace store accepted")
	}
	if err := SweepAll(t.Context(), db, nil, time.Now()); err == nil {
		t.Fatal("sweep on closed store reported success")
	}
	if _, err := Count(t.Context(), db, "ws1", Inbox, days(30), time.Now()); err == nil {
		t.Fatal("count on closed store reported an empty backlog")
	}
	for _, sweep := range []struct {
		name string
		run  func() (int, bool, error)
	}{
		{"inbox", func() (int, bool, error) { return SweepInbox(t.Context(), db, "ws1", 30, time.Now()) }},
		{"chats", func() (int, bool, error) { return SweepChats(t.Context(), db, "ws1", 30, time.Now()) }},
		{"keeper", func() (int, bool, error) { return SweepKeeperDecisions(t.Context(), db, "ws1", 30, time.Now()) }},
	} {
		n, _, err := sweep.run()
		if err == nil || n != 0 {
			t.Fatalf("%s deletion count=%d error=%v", sweep.name, n, err)
		}
	}
}

func TestCanceledSweepsNeverOpenDatabaseOrDeleteRows(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, sweep := range []struct {
		name string
		run  func() (int, bool, error)
	}{
		{"inbox", func() (int, bool, error) { return SweepInbox(ctx, nil, "ws1", 30, time.Now()) }},
		{"chats", func() (int, bool, error) { return SweepChats(ctx, nil, "ws1", 30, time.Now()) }},
		{"keeper", func() (int, bool, error) { return SweepKeeperDecisions(ctx, nil, "ws1", 30, time.Now()) }},
	} {
		n, capped, err := sweep.run()
		if n != 0 || capped || !errors.Is(err, context.Canceled) {
			t.Fatalf("%s: deleted=%d capped=%v error=%v", sweep.name, n, capped, err)
		}
	}
}
