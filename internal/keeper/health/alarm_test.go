package health

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/keeper"
	"github.com/crewship-ai/crewship/internal/notify"
	"github.com/crewship-ai/crewship/internal/testutil"
	_ "modernc.org/sqlite"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func healthTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := testutil.MigratedSQLDB(t)
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1', 'ws', 'ws')`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	return db
}

func sampleAlarm(t *testing.T) Alarm {
	t.Helper()
	m := NewMonitor(DefaultWindowSize)
	a, ok := feed(m, "ws1", string(keeper.DecisionDeny), MinSamples, base)
	if !ok {
		t.Fatal("fixture produced no alarm")
	}
	return a
}

func TestAlarmItemProjection(t *testing.T) {
	item := AlarmItem(sampleAlarm(t))

	if item.WorkspaceID != "ws1" {
		t.Errorf("WorkspaceID = %q", item.WorkspaceID)
	}
	// The category is what decides whether this ever leaves the instance.
	if item.Category != notify.CategorySystemHealth {
		t.Errorf("Category = %q, want %q", item.Category, notify.CategorySystemHealth)
	}
	// The kind must be one the inbox_items CHECK admits, or the row is
	// rejected and the alarm reaches nobody.
	var known bool
	for _, k := range inbox.AllKinds {
		if k == item.Kind {
			known = true
		}
	}
	if !known {
		t.Errorf("Kind = %q, which is not in inbox.AllKinds", item.Kind)
	}
	if item.Blocking {
		t.Error("Blocking = true; there is nothing for a human to approve here")
	}
	if item.Priority != "high" {
		t.Errorf("Priority = %q, want high", item.Priority)
	}
	if !strings.Contains(item.SourceID, "ws1") || !strings.Contains(item.SourceID, string(AlarmAllowCollapse)) {
		t.Errorf("SourceID = %q, want it scoped to the workspace and the alarm kind", item.SourceID)
	}
	for _, want := range []string{"allow_rate", "judge_failure_rate", "samples", "p95_latency_ms"} {
		if _, ok := item.Payload[want]; !ok {
			t.Errorf("payload is missing %q: %v", want, item.Payload)
		}
	}
	if item.BodyMD == "" {
		t.Error("BodyMD is empty; the card would show a title and no numbers")
	}
}

// TestAlarmSourceIDIsBucketed: inbox.Insert dedups on (kind, source_id), and
// that is the only defence left after a restart wipes the in-memory cooldown.
// Two alarms inside one cooldown window must collapse onto one row; two in
// different windows must not.
func TestAlarmSourceIDIsBucketed(t *testing.T) {
	a := sampleAlarm(t)
	b := a
	b.At = a.At.Add(AlarmCooldown / 2)
	c := a
	c.At = a.At.Add(2 * AlarmCooldown)

	if AlarmItem(a).SourceID != AlarmItem(b).SourceID {
		t.Error("two alarms inside one cooldown window got different source ids")
	}
	if AlarmItem(a).SourceID == AlarmItem(c).SourceID {
		t.Error("two alarms two cooldowns apart share a source id; the second would be dedup'd away")
	}
}

func TestRaiseWritesInboxRow(t *testing.T) {
	db := healthTestDB(t)
	a := sampleAlarm(t)

	if err := Raise(context.Background(), db, quietLogger(), a); err != nil {
		t.Fatalf("Raise: %v", err)
	}

	var kind, title, body, priority string
	var blocking int
	err := db.QueryRow(`SELECT kind, title, body_md, priority, blocking
		FROM inbox_items WHERE workspace_id = 'ws1'`).Scan(&kind, &title, &body, &priority, &blocking)
	if err != nil {
		t.Fatalf("no inbox row written: %v", err)
	}
	if !strings.Contains(strings.ToLower(title), "keeper") {
		t.Errorf("title = %q, want it to name Keeper", title)
	}
	if blocking != 0 {
		t.Errorf("blocking = %d, want 0", blocking)
	}
	if priority != "high" {
		t.Errorf("priority = %q, want high", priority)
	}

	// Second raise in the same bucket must not add a row.
	if err := Raise(context.Background(), db, quietLogger(), a); err != nil {
		t.Fatalf("second Raise: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("inbox rows = %d, want 1 — the same outage wrote a second card", n)
	}
}

// TestRaiseSurvivesNoDatabase: this runs off the credential hot path via a
// detached goroutine. A nil db (tests, a boot path with no store) must be a
// no-op, never a panic that takes the process down with it.
func TestRaiseSurvivesNoDatabase(t *testing.T) {
	if err := Raise(context.Background(), nil, quietLogger(), sampleAlarm(t)); err != nil {
		t.Errorf("Raise with nil db returned %v, want nil", err)
	}
	if err := Raise(context.Background(), nil, nil, Alarm{}); err != nil {
		t.Errorf("Raise with an empty alarm returned %v, want nil", err)
	}
}

// TestRecordNeverBlocksOrFails is the hot-path contract: Record is called
// while an agent is waiting on a credential decision, so it must return
// promptly and must not surface an error the caller could act on.
func TestRecordNeverBlocksOrFails(t *testing.T) {
	start := time.Now()
	for i := 0; i < 5000; i++ {
		Record(context.Background(), nil, quietLogger(), Verdict{
			WorkspaceID: "ws-hotpath",
			Decision:    string(keeper.DecisionDeny),
			At:          base.Add(time.Duration(i) * time.Second),
		})
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("5000 Record calls took %v; this sits in front of a credential decision", elapsed)
	}
}
