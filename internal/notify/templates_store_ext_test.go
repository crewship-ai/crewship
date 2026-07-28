// Package notify_test — the template store, exercised against the REAL
// migrated schema.
//
// An external test package on purpose. internal/database imports
// internal/notify, so this package cannot import it back; `notify_test` is a
// separate package, so it can, and nothing cycles.
//
// The alternative is what the rest of this package's tests do: hand-roll the
// table in the test. That is exactly how the schedule circuit-breaker alert
// shipped broken — its rig built an inbox_items with no CHECK constraint, so
// the test asserted the alert landed and passed green while production
// rejected every insert. A store whose correctness depends on a unique index
// and an ON CONFLICT clause has to be tested against the index that actually
// exists.
package notify_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/notify"
)

var tmplTestCounter atomic.Int64

func newTemplateStore(t *testing.T) (*notify.TemplateStore, string) {
	t.Helper()
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("0123456789abcdef", 4))
	name := fmt.Sprintf("%s/templates-%d.db", t.TempDir(), tmplTestCounter.Add(1))
	db, err := database.Open("file:" + name)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	quiet := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	if err := database.Migrate(context.Background(), db.DB, quiet); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1','WS','ws1')`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	// A real channel row, because channel_id is a real foreign key. Seeding
	// it is the point of testing against the migrated schema rather than a
	// hand-rolled table that would have accepted anything.
	if _, err := db.Exec(
		`INSERT INTO notification_channels (id, workspace_id, type) VALUES ('nch_1','ws1','webhook')`); err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	return notify.NewTemplateStore(db.DB), "ws1"
}

func TestTemplateStore_UpsertIsIdempotentPerScope(t *testing.T) {
	// The unique index treats NULL channel_id as its own slot via COALESCE,
	// because SQLite counts two NULLs as distinct in a UNIQUE constraint.
	// Without that, saving an all-channels template twice would insert a
	// second row and the winner at read time would be arbitrary.
	s, ws := newTemplateStore(t)
	ctx := context.Background()

	for _, title := range []string{"first", "second", "third"} {
		if err := s.Upsert(ctx, ws, notify.MessageTemplate{
			Category: notify.CategoryRoutinesFailed, Title: title,
		}); err != nil {
			t.Fatalf("upsert %q: %v", title, err)
		}
	}

	all, err := s.List(ctx, ws)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("want 1 row after three upserts, got %d: %+v", len(all), all)
	}
	if all[0].Title != "third" {
		t.Errorf("title = %q, want the last write", all[0].Title)
	}
}

func TestTemplateStore_ChannelScopeIsSeparateFromAllChannels(t *testing.T) {
	s, ws := newTemplateStore(t)
	ctx := context.Background()

	if err := s.Upsert(ctx, ws, notify.MessageTemplate{
		Category: notify.CategoryRoutinesFailed, Title: "everywhere",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(ctx, ws, notify.MessageTemplate{
		Category: notify.CategoryRoutinesFailed, ChannelID: "nch_1", Title: "just this one",
	}); err != nil {
		t.Fatal(err)
	}

	all, _ := s.List(ctx, ws)
	if len(all) != 2 {
		t.Fatalf("want both scopes stored, got %d: %+v", len(all), all)
	}
}

func TestTemplateStore_ResolvePrefersTheChannelSpecificRow(t *testing.T) {
	// The narrower row exists precisely because that destination should
	// differ; letting the general one win would make it pointless.
	s, ws := newTemplateStore(t)
	ctx := context.Background()

	_ = s.Upsert(ctx, ws, notify.MessageTemplate{Category: notify.CategoryRoutinesFailed, Title: "everywhere"})
	_ = s.Upsert(ctx, ws, notify.MessageTemplate{Category: notify.CategoryRoutinesFailed, ChannelID: "nch_1", Title: "pager"})

	got, err := s.Resolve(ctx, ws, notify.CategoryRoutinesFailed, "nch_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "pager" {
		t.Errorf("title = %q, want the channel-specific one", got.Title)
	}

	// A channel with no row of its own falls back to the general one.
	got, _ = s.Resolve(ctx, ws, notify.CategoryRoutinesFailed, "nch_other")
	if got.Title != "everywhere" {
		t.Errorf("fallback title = %q, want the all-channels one", got.Title)
	}
}

func TestTemplateStore_ResolveWithNoTemplateIsNotAnError(t *testing.T) {
	// Almost every notification takes this path — no template configured —
	// so it has to be cheap and quiet, not an error the caller handles.
	s, ws := newTemplateStore(t)
	got, err := s.Resolve(context.Background(), ws, notify.CategoryRoutinesFailed, "nch_1")
	if err != nil {
		t.Fatalf("resolve with nothing stored: %v", err)
	}
	if got.Title != "" || got.Body != "" {
		t.Errorf("want a zero template, got %+v", got)
	}
}

func TestTemplateStore_ClearingBothFieldsRemovesTheRow(t *testing.T) {
	// An operator emptying both boxes in a form means "stop overriding this".
	// Keeping the row would make List report a template where none applies.
	s, ws := newTemplateStore(t)
	ctx := context.Background()

	_ = s.Upsert(ctx, ws, notify.MessageTemplate{Category: notify.CategoryRoutinesFailed, Title: "x"})
	if err := s.Upsert(ctx, ws, notify.MessageTemplate{Category: notify.CategoryRoutinesFailed}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if all, _ := s.List(ctx, ws); len(all) != 0 {
		t.Errorf("want the row gone, got %+v", all)
	}
}

func TestTemplateStore_DeletingSomethingAbsentIsFine(t *testing.T) {
	s, ws := newTemplateStore(t)
	if err := s.Delete(context.Background(), ws, notify.CategoryRoutinesFailed, ""); err != nil {
		t.Errorf("deleting an absent template must not error: %v", err)
	}
}

func TestTemplateStore_RejectsWhatCouldNeverApply(t *testing.T) {
	s, ws := newTemplateStore(t)
	ctx := context.Background()

	if err := s.Upsert(ctx, ws, notify.MessageTemplate{Category: "routines.exploded", Title: "x"}); err == nil {
		t.Error("a category that does not exist routes to nobody and must be rejected at the write")
	}
	err := s.Upsert(ctx, ws, notify.MessageTemplate{
		Category: notify.CategoryRoutinesFailed, Title: "{{ nonsense.field }}",
	})
	if err == nil {
		t.Error("a reference to a namespace that does not exist must be rejected")
	} else if !strings.Contains(err.Error(), "nonsense.field") {
		t.Errorf("the error should name the bad reference, got: %v", err)
	}
}

func TestTemplateStore_AcceptsAnUnknownFactKey(t *testing.T) {
	// Which facts an event carries depends on the event. Refusing a template
	// because today's sample lacks the field would be worse than rendering
	// that fragment empty — the namespace is checkable, the key is not.
	s, ws := newTemplateStore(t)
	err := s.Upsert(context.Background(), ws, notify.MessageTemplate{
		Category: notify.CategoryRoutinesFailed,
		Title:    "{{ vars.something_only_some_events_have }}",
	})
	if err != nil {
		t.Errorf("an unrecognised fact key must be allowed: %v", err)
	}
}

func TestTemplateStore_ScopesToItsWorkspace(t *testing.T) {
	s, ws := newTemplateStore(t)
	ctx := context.Background()
	_ = s.Upsert(ctx, ws, notify.MessageTemplate{Category: notify.CategoryRoutinesFailed, Title: "mine"})

	if all, _ := s.List(ctx, "ws_other"); len(all) != 0 {
		t.Errorf("another workspace's templates leaked: %+v", all)
	}
	got, _ := s.Resolve(ctx, "ws_other", notify.CategoryRoutinesFailed, "")
	if got.Title != "" {
		t.Errorf("resolve crossed a workspace boundary: %+v", got)
	}
}
