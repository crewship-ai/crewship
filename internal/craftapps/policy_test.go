package craftapps

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func settingsDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	schema, err := os.ReadFile("../database/migrations/20261113101241_craft_application_settings.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestModeAllows(t *testing.T) {
	cases := []struct {
		mode Mode
		want [4]bool // OpCreate, OpStartEdit, OpContinue, OpAlways
	}{
		{ModeOpen, [4]bool{true, true, true, true}},
		{ModeDraining, [4]bool{false, false, true, true}},
		{ModeClosed, [4]bool{false, false, false, true}},
		{Mode("bogus"), [4]bool{false, false, false, true}},
	}
	for _, c := range cases {
		for op, want := range c.want {
			if got := c.mode.Allows(Operation(op)); got != want {
				t.Errorf("%s.Allows(%d) = %v, want %v", c.mode, op, got, want)
			}
		}
	}
}

func TestPolicyDefaultsToDisabledAndRecordsAdminChanges(t *testing.T) {
	db := settingsDB(t)
	now := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	p := &Policy{Now: func() time.Time { return now }}
	ctx := context.Background()
	for _, subject := range []string{Word, Sheet, Agents} {
		s, err := p.Get(ctx, db, subject)
		if err != nil || s.Desired != Disabled || s.Source != SourceDefault || s.Mode(now) != ModeClosed {
			t.Fatalf("%s default = %+v, %v", subject, s, err)
		}
	}
	before, after, err := p.Set(ctx, db, Word, Enabled, "admin@example.test", 0)
	if err != nil || before.Source != SourceDefault || after.Desired != Enabled || after.Source != SourceAdmin || after.UpdatedBy != "admin@example.test" {
		t.Fatalf("enable: %+v -> %+v, %v", before, after, err)
	}
	if p.Mode(ctx, db, Word) != ModeOpen || p.Mode(ctx, db, Sheet) != ModeClosed {
		t.Fatal("enabling Word changed another subject or did not open Word")
	}
	// Repeating the same state is a no-op.
	again, same, err := p.Set(ctx, db, Word, Enabled, "someone-else", 0)
	if err != nil || again != same || same.UpdatedBy != "admin@example.test" {
		t.Fatalf("idempotent enable rewrote the row: %+v %v", same, err)
	}
}

func TestPolicyDrainLifecycle(t *testing.T) {
	db := settingsDB(t)
	now := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	p := &Policy{Now: func() time.Time { return now }}
	ctx := context.Background()
	if _, _, err := p.Set(ctx, db, Sheet, Enabled, "a", 0); err != nil {
		t.Fatal(err)
	}
	_, after, err := p.Set(ctx, db, Sheet, Disabled, "a", DefaultDrain)
	if err != nil || after.DrainUntil == nil || !after.DrainUntil.Equal(now.Add(DefaultDrain)) {
		t.Fatalf("drain not started: %+v %v", after, err)
	}
	if m := p.Mode(ctx, db, Sheet); m != ModeDraining {
		t.Fatalf("mode during drain = %s", m)
	}
	now = now.Add(DefaultDrain)
	if m := p.Mode(ctx, db, Sheet); m != ModeClosed {
		t.Fatalf("mode after drain deadline = %s", m)
	}
	// A drain never starts from an already disabled application.
	now = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	if _, _, err = p.Set(ctx, db, Word, Disabled, "a", time.Hour); err != nil {
		t.Fatal(err)
	}
	if p.Mode(ctx, db, Word) != ModeClosed {
		t.Fatal("disabling a disabled application started a drain")
	}
	// "Disable now" during a drain closes immediately.
	if _, _, err = p.Set(ctx, db, Sheet, Enabled, "a", 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err = p.Set(ctx, db, Sheet, Disabled, "a", time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, after, err = p.Set(ctx, db, Sheet, Disabled, "a", 0); err != nil || after.DrainUntil != nil || p.Mode(ctx, db, Sheet) != ModeClosed {
		t.Fatalf("immediate close during drain: %+v %v", after, err)
	}
}

func TestPolicyRejectsInvalidInputAndLockedSubjects(t *testing.T) {
	db := settingsDB(t)
	ctx := context.Background()
	p := &Policy{Locked: map[string]bool{Agents: false, Word: true}}
	cases := []struct {
		name    string
		subject string
		desired Desired
		drain   time.Duration
		want    error
	}{
		{"unknown subject", "powerpoint", Enabled, 0, ErrSubject},
		{"bad desired", Sheet, Desired("maybe"), 0, ErrDesired},
		{"negative drain", Sheet, Disabled, -time.Second, ErrDrain},
		{"drain too long", Sheet, Disabled, MaxDrain + time.Second, ErrDrain},
		{"locked agents", Agents, Enabled, 0, ErrLocked},
		{"locked word", Word, Disabled, 0, ErrLocked},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, _, err := p.Set(ctx, db, c.subject, c.desired, "a", c.drain); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
	s, err := p.Get(ctx, db, Word)
	if err != nil || s.Source != SourceServerConfig || s.Desired != Enabled {
		t.Fatalf("locked Word = %+v %v", s, err)
	}
	var rows int
	if err = db.QueryRow(`SELECT count(*) FROM craft_application_settings`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("rejected changes wrote %d rows (%v)", rows, err)
	}
}

func TestPolicyReadFailureFailsClosed(t *testing.T) {
	db := settingsDB(t)
	if _, err := db.Exec(`DROP TABLE craft_application_settings`); err != nil {
		t.Fatal(err)
	}
	if m := (&Policy{}).Mode(context.Background(), db, Word); m != ModeClosed {
		t.Fatalf("mode on read failure = %s", m)
	}
}

func TestEvaluate(t *testing.T) {
	now := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	later := now.Add(time.Minute)
	ready := Component{Available: true}
	cases := []struct {
		name   string
		s      Setting
		c      Component
		state  State
		reason Reason
	}{
		{"enabled and present", Setting{Desired: Enabled}, ready, StateReady, ReasonNone},
		{"disabled", Setting{Desired: Disabled}, ready, StateDisabled, ReasonAppDisabled},
		{"draining", Setting{Desired: Disabled, DrainUntil: &later}, ready, StateDraining, ReasonAppDraining},
		{"enabled but missing", Setting{Desired: Enabled}, Component{}, StateUnavailable, ReasonComponentMissing},
		{"disabled and broken keeps the component reason", Setting{Desired: Disabled}, Component{Reason: ReasonComponentIntegrity}, StateUnavailable, ReasonComponentIntegrity},
	}
	for _, c := range cases {
		if state, reason := Evaluate(c.s, c.c, now); state != c.state || reason != c.reason {
			t.Errorf("%s: %s/%s, want %s/%s", c.name, state, reason, c.state, c.reason)
		}
	}
}
