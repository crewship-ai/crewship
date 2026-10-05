package quiesce_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/crewship-ai/crewship/internal/quiesce"
)

func holdsDB(t *testing.T, schema bool) *sql.DB {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "crewship-3-quiesce-holds-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	db, err := sql.Open("sqlite", filepath.Join(dir, "holds.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if schema {
		if _, err := db.Exec(`CREATE TABLE instance_holds (key TEXT PRIMARY KEY, reason TEXT, count INTEGER, detail TEXT, created_at TEXT)`); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestPersistedHoldsResumeOnlyAfterCommit(t *testing.T) {
	db := holdsDB(t, true)
	ctx := t.Context()
	stamp := time.Date(2026, 10, 3, 1, 2, 3, 0, time.FixedZone("offset", 3600))
	initial := []quiesce.Hold{{Key: quiesce.HoldWebhooks, Reason: "restore", Count: 4, Detail: "retry after inspection", CreatedAt: stamp}, {Key: quiesce.HoldQueue, Reason: "queue inspection"}, {Key: quiesce.HoldRoutines}}
	before := time.Now().Add(-time.Second)
	if err := quiesce.SetHolds(ctx, db, initial); err != nil {
		t.Fatal(err)
	}
	h := quiesce.NewHolds()
	if err := h.Load(ctx, db); err != nil {
		t.Fatal(err)
	}
	got := h.List()
	if len(got) != 3 || got[0].Key != quiesce.HoldQueue || got[1].Key != quiesce.HoldRoutines || got[2].Key != quiesce.HoldWebhooks {
		t.Fatalf("unordered holds: %#v", got)
	}
	if !got[2].CreatedAt.Equal(stamp) || got[2].Reason != "restore" || got[2].Count != 4 || got[2].Detail != initial[0].Detail || got[0].CreatedAt.Before(before) {
		t.Fatalf("metadata lost: %#v", got)
	}
	got[0].Reason = "caller mutation"
	if h.List()[0].Reason != "queue inspection" {
		t.Fatal("List exposed internal state")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	prev, err := h.Resume(ctx, tx, quiesce.HoldQueue)
	if err != nil || prev.Reason != "queue inspection" || !h.Held(quiesce.HoldQueue) {
		t.Fatalf("premature resume: %#v %v", prev, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := h.Load(ctx, db); err != nil || !h.Held(quiesce.HoldQueue) {
		t.Fatalf("rollback lost hold: %v", err)
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Resume(ctx, tx, quiesce.HoldQueue); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	h.Forget(quiesce.HoldQueue)
	if h.Held(quiesce.HoldQueue) || !h.Held(quiesce.HoldWebhooks) {
		t.Fatal("resume cleared unrelated holds")
	}
	if _, err := h.Resume(ctx, db, quiesce.HoldQueue); !errors.Is(err, quiesce.ErrUnknownHold) {
		t.Fatalf("unknown hold: %v", err)
	}
	if err := quiesce.SetHolds(ctx, db, []quiesce.Hold{{Key: quiesce.HoldAll, Reason: "global pause"}}); err != nil {
		t.Fatal(err)
	}
	if err := h.Load(ctx, db); err != nil {
		t.Fatal(err)
	}
	if !h.Held(quiesce.HoldQueue) {
		t.Fatal("all did not include queue")
	}
	if _, err := h.Resume(ctx, db, quiesce.HoldAll); err != nil {
		t.Fatal(err)
	}
	if !h.Held(quiesce.HoldQueue) {
		t.Fatal("memory cleared before caller confirmed commit")
	}
	h.Forget(quiesce.HoldAll)
	rows, err := quiesce.ReadHolds(ctx, db)
	if err != nil || len(rows) != 0 || len(h.List()) != 0 {
		t.Fatalf("all did not clear everything: %#v %v", rows, err)
	}
}

func TestHoldsLoadCompatibilityAndMalformedRows(t *testing.T) {
	db := holdsDB(t, false)
	h := quiesce.NewHolds()
	h.Replace([]quiesce.Hold{{Key: quiesce.HoldAll}})
	if err := h.Load(t.Context(), db); err != nil || len(h.List()) != 0 {
		t.Fatalf("older schema: %#v %v", h.List(), err)
	}
	if _, err := db.Exec(`CREATE TABLE instance_holds (key TEXT PRIMARY KEY, reason TEXT, count INTEGER, detail TEXT, created_at TEXT);
INSERT INTO instance_holds VALUES ('queue',NULL,NULL,NULL,'2026-10-03 01:02:03'), ('routines','r',2,'d','invalid timestamp'), ('webhooks','w',3,'d','2026-10-03T01:02:03.123456789Z')`); err != nil {
		t.Fatal(err)
	}
	if err := h.Load(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	got := h.List()
	if got[0].Reason != "" || got[0].Count != 0 || got[0].Detail != "" || got[0].CreatedAt.Format(time.RFC3339) != "2026-10-03T01:02:03Z" || !got[1].CreatedAt.IsZero() || got[2].CreatedAt.Nanosecond() != 123456789 {
		t.Fatalf("legacy/null parsing: %#v", got)
	}
	if _, err := db.Exec(`UPDATE instance_holds SET count='not an integer' WHERE key='webhooks'`); err != nil {
		t.Fatal(err)
	}
	if rows, err := quiesce.ReadHolds(t.Context(), db); err == nil || rows != nil || !strings.Contains(err.Error(), "scan hold") {
		t.Fatalf("partial malformed rows: %#v %v", rows, err)
	}
	if err := h.Load(t.Context(), db); err == nil || !reflect.DeepEqual(h.List(), got) {
		t.Fatalf("failed load changed safety state: %#v %v", h.List(), err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := h.Load(t.Context(), db); err == nil || !reflect.DeepEqual(h.List(), got) {
		t.Fatalf("closed database changed safety state: %v", err)
	}
}

func TestHoldsStorageFailuresKeepAutomationPaused(t *testing.T) {
	db := holdsDB(t, true)
	ctx := t.Context()
	for _, key := range []string{quiesce.HoldQueue, quiesce.HoldRoutines, quiesce.HoldWebhooks, quiesce.HoldAll} {
		if !quiesce.ValidHoldKey(key) {
			t.Fatalf("valid key refused: %q", key)
		}
	}
	for _, key := range []string{"", "ALL", "unknown"} {
		if quiesce.ValidHoldKey(key) {
			t.Fatalf("invalid key allowed: %q", key)
		}
		if err := quiesce.SetHolds(ctx, db, []quiesce.Hold{{Key: key}}); err == nil {
			t.Fatalf("invalid hold persisted: %q", key)
		}
	}
	if err := quiesce.SetHolds(ctx, db, []quiesce.Hold{{Key: quiesce.HoldQueue, Reason: "first"}}); err != nil {
		t.Fatal(err)
	}
	if err := quiesce.SetHolds(ctx, db, []quiesce.Hold{{Key: quiesce.HoldQueue, Reason: "updated", Count: 7}}); err != nil {
		t.Fatal(err)
	}
	h := quiesce.NewHolds()
	if err := h.Load(ctx, db); err != nil {
		t.Fatal(err)
	}
	if got := h.List(); len(got) != 1 || got[0].Reason != "updated" || got[0].Count != 7 {
		t.Fatalf("upsert lost data: %#v", got)
	}
	if _, err := db.Exec(`CREATE TRIGGER refuse_resume BEFORE DELETE ON instance_holds BEGIN SELECT RAISE(ABORT,'storage unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Resume(ctx, db, quiesce.HoldQueue); err == nil || !strings.Contains(err.Error(), "storage unavailable") || !h.Held(quiesce.HoldQueue) {
		t.Fatalf("failed delete resumed automation: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := quiesce.SetHolds(cancelled, db, []quiesce.Hold{{Key: quiesce.HoldAll}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled write: %v", err)
	}
	if _, err := db.Exec(`DROP TRIGGER refuse_resume`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Resume(ctx, db, quiesce.HoldQueue); err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	h.Forget(quiesce.HoldQueue)
	if h.Held(quiesce.HoldQueue) {
		t.Fatal("successful retry left queue held")
	}
}
