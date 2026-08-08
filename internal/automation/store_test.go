package automation

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// seedWorkspace creates the minimum rows ListActive's join needs: a workspace
// and a routine to point at.
func seedWorkspace(t *testing.T, db *sql.DB, workspaceID, pipelineID, slug string) {
	t.Helper()
	testutil.MustExec(t, db,
		`INSERT OR IGNORE INTO workspaces (id, name, slug) VALUES (?, ?, ?)`,
		workspaceID, workspaceID, workspaceID)
	testutil.MustExec(t, db, `
INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash)
VALUES (?, ?, ?, ?, '{}', 'h')`, pipelineID, workspaceID, slug, slug)
}

func newStore(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	db := testutil.MigratedSQLDB(t)
	return NewStore(db), db
}

func sample(workspaceID, name string) Automation {
	return Automation{
		WorkspaceID: workspaceID,
		Name:        name,
		Enabled:     true,
		EventType:   "mission.status_change",
		Matcher:     Matcher{PayloadEquals: map[string]any{"action": "status_changed"}},
		ActionKind:  ActionKindRoutine,
		Action:      Action{RoutineSlug: "triage", Inputs: map[string]any{"issue": "{{ event.mission_id }}"}},
	}
}

func TestStoreCreateReadRoundTrip(t *testing.T) {
	st, db := newStore(t)
	seedWorkspace(t, db, "ws_1", "pl_1", "triage")
	ctx := context.Background()

	created, err := st.Create(ctx, sample("ws_1", "close triage"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == "" {
		t.Fatal("create returned an empty id")
	}
	// Unset burst controls take the documented defaults, in Go, so a row
	// written by the store never depends on the column default.
	if created.DebounceSeconds != DefaultDebounceSeconds || created.MaxPerHour != DefaultMaxPerHour {
		t.Errorf("defaults = %d/%d, want %d/%d",
			created.DebounceSeconds, created.MaxPerHour, DefaultDebounceSeconds, DefaultMaxPerHour)
	}

	got, err := st.Get(ctx, "ws_1", created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "close triage" || got.EventType != "mission.status_change" {
		t.Errorf("round trip lost fields: %+v", got)
	}
	if got.Action.RoutineSlug != "triage" || got.Action.Inputs["issue"] != "{{ event.mission_id }}" {
		t.Errorf("action did not survive the round trip: %+v", got.Action)
	}
	if got.Matcher.PayloadEquals["action"] != "status_changed" {
		t.Errorf("matcher did not survive the round trip: %+v", got.Matcher)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Errorf("timestamps did not parse back: %+v", got)
	}
}

// Reads are workspace-scoped, and a foreign row reports not-found rather than
// forbidden — the caller has no business learning the id exists.
func TestStoreGetIsWorkspaceScoped(t *testing.T) {
	st, db := newStore(t)
	seedWorkspace(t, db, "ws_A", "pl_A", "triage")
	seedWorkspace(t, db, "ws_B", "pl_B", "triage")
	ctx := context.Background()

	created, err := st.Create(ctx, sample("ws_A", "a"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := st.Get(ctx, "ws_B", created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-workspace get err = %v, want ErrNotFound", err)
	}
	if _, err := st.Update(ctx, "ws_B", created.ID, Patch{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-workspace update err = %v, want ErrNotFound", err)
	}
	if err := st.Delete(ctx, "ws_B", created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-workspace delete err = %v, want ErrNotFound", err)
	}
	list, err := st.List(ctx, "ws_B")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("workspace B sees %d of workspace A's automations", len(list))
	}
}

// The patch is sparse: `automation disable` writes one field and must not
// clobber a matcher edited a moment earlier.
func TestStoreUpdateIsSparse(t *testing.T) {
	st, db := newStore(t)
	seedWorkspace(t, db, "ws_1", "pl_1", "triage")
	ctx := context.Background()

	created, err := st.Create(ctx, sample("ws_1", "a"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	off := false
	updated, err := st.Update(ctx, "ws_1", created.ID, Patch{Enabled: &off})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Enabled {
		t.Error("enabled was not written")
	}
	if updated.Name != "a" || updated.Matcher.PayloadEquals["action"] != "status_changed" {
		t.Errorf("a one-field patch clobbered the rest: %+v", updated)
	}
}

func TestStoreDeleteIsSoftAndHidesTheRow(t *testing.T) {
	st, db := newStore(t)
	seedWorkspace(t, db, "ws_1", "pl_1", "triage")
	ctx := context.Background()

	created, err := st.Create(ctx, sample("ws_1", "a"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := st.Delete(ctx, "ws_1", created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := st.Get(ctx, "ws_1", created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete err = %v, want ErrNotFound", err)
	}
	// The row is still there, so a run it caused can still be explained.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM automations WHERE id = ?`, created.ID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("rows after soft delete = %d, want 1", n)
	}
	if err := st.Delete(ctx, "ws_1", created.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete err = %v, want ErrNotFound", err)
	}
}

// ListActive is the registry's only query. It must resolve routine_slug to a
// pipeline id, drop what it cannot resolve, and never return a disabled or
// deleted rule.
func TestListActiveResolvesRoutinesAndSkipsTheRest(t *testing.T) {
	st, db := newStore(t)
	seedWorkspace(t, db, "ws_1", "pl_1", "triage")
	ctx := context.Background()

	live, err := st.Create(ctx, sample("ws_1", "live"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	off := sample("ws_1", "disabled")
	off.Enabled = false
	if _, err := st.Create(ctx, off); err != nil {
		t.Fatalf("create disabled: %v", err)
	}

	gone := sample("ws_1", "deleted")
	deleted, err := st.Create(ctx, gone)
	if err != nil {
		t.Fatalf("create deleted: %v", err)
	}
	if err := st.Delete(ctx, "ws_1", deleted.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	dangling := sample("ws_1", "dangling")
	dangling.Action.RoutineSlug = "does-not-exist"
	if _, err := st.Create(ctx, dangling); err != nil {
		t.Fatalf("create dangling: %v", err)
	}

	active, err := st.ListActive(ctx)
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("active = %d rules, want 1: %+v", len(active), active)
	}
	if active[0].ID != live.ID {
		t.Errorf("active rule = %q, want %q", active[0].ID, live.ID)
	}
	if active[0].PipelineID != "pl_1" || active[0].PipelineSlug != "triage" {
		t.Errorf("routine not resolved: %q/%q", active[0].PipelineID, active[0].PipelineSlug)
	}
}

// A routine in ANOTHER workspace must not satisfy the join. Slugs are unique
// per workspace, so an unscoped join would silently point one tenant's rule
// at another tenant's routine.
func TestListActiveJoinIsWorkspaceScoped(t *testing.T) {
	st, db := newStore(t)
	seedWorkspace(t, db, "ws_A", "pl_A", "triage")
	// ws_B exists but has no `triage` routine.
	testutil.MustExec(t, db, `INSERT OR IGNORE INTO workspaces (id, name, slug) VALUES ('ws_B','ws_B','ws_B')`)
	ctx := context.Background()

	if _, err := st.Create(ctx, sample("ws_B", "b")); err != nil {
		t.Fatalf("create: %v", err)
	}
	active, err := st.ListActive(ctx)
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("ws_B's rule resolved to ws_A's routine: %+v", active)
	}
}

// The registry is refreshed FROM the store, so the two have to agree. This
// drives the real path: seed rows, Refresh, then observe.
func TestRegistryRefreshLoadsFromTheStore(t *testing.T) {
	st, db := newStore(t)
	seedWorkspace(t, db, "ws_1", "pl_1", "triage")
	ctx := context.Background()
	if _, err := st.Create(ctx, sample("ws_1", "a")); err != nil {
		t.Fatalf("create: %v", err)
	}

	enq := &recordingEnqueuer{}
	reg := NewRegistry(st, enq, Options{})
	if err := reg.Refresh(ctx); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	reg.Observer([]journal.Entry{entry("ws_1", "mission.status_change", "m_1")})
	reg.Flush(ctx)
	if enq.n() != 1 {
		t.Fatalf("enqueues = %d, want 1", enq.n())
	}
	if got := enq.at(0).PipelineSlug; got != "triage" {
		t.Errorf("pipeline slug = %q, want triage", got)
	}
}

func TestValidateRejectsUnsupportedActionKind(t *testing.T) {
	a := sample("ws_1", "a")
	a.ActionKind = "notify"
	if err := a.Validate(); err == nil {
		t.Fatal("action_kind 'notify' was accepted; v1 supports only 'routine'")
	}
}

func TestValidateRejectsMissingRoutine(t *testing.T) {
	a := sample("ws_1", "a")
	a.Action.RoutineSlug = ""
	if err := a.Validate(); err == nil {
		t.Fatal("an automation with no routine_slug was accepted")
	}
}

func TestStoreRejectsUnsupportedActionKindAtWriteTime(t *testing.T) {
	st, db := newStore(t)
	seedWorkspace(t, db, "ws_1", "pl_1", "triage")
	a := sample("ws_1", "a")
	a.ActionKind = "issue"
	if _, err := st.Create(context.Background(), a); err == nil {
		t.Fatal("store accepted action_kind 'issue'")
	}
}
