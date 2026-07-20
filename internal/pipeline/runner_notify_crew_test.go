package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/inbox"
)

// #842: crew:<slug> notify targeting. The Phase 1 deferral premise ("there is
// no crew→user mapping in the schema") is stale — crew_members(crew_id,
// user_id) has existed since migration v01 — so the notify step now fans a
// notice out to every human member of the addressed crew.
//
// The first two tests are the inverted Phase 1 rejection tests: they assert
// the target is ACCEPTED, at save time and at run time.

func TestValidateNotifyTarget_CrewAccepted(t *testing.T) {
	if err := validateNotifyTarget("crew:engineering"); err != nil {
		t.Fatalf("crew: target must be accepted at validate time, got: %v", err)
	}
}

func TestResolveNotifyTargets_CrewAcceptedAtRuntime(t *testing.T) {
	recips, slug, err := resolveNotifyTargets("crew:ops", "u_trigger")
	if err != nil {
		t.Fatalf("crew: target must resolve at run time, got: %v", err)
	}
	if slug != "ops" {
		t.Errorf("crew slug = %q, want ops", slug)
	}
	if len(recips) != 0 {
		t.Errorf("crew: recipients are expanded through the audience seam, want none inline, got %+v", recips)
	}
	// An empty slug is still a shape error (e.g. `crew:{{ inputs.team }}`
	// rendering to `crew:`), and must degrade rather than address everyone.
	if _, _, err := resolveNotifyTargets("crew:", "u"); err == nil {
		t.Error("crew: with no slug must be an error")
	}
}

// crewFanoutExecutor wires a fake audience resolver returning users for slug.
func crewFanoutExecutor(fake InboxNotifier, audience map[string][]string, err error) *Executor {
	return notifyExecutor(fake).WithCrewAudienceResolver(
		func(_ context.Context, _, slug string) ([]string, error) {
			if err != nil {
				return nil, err
			}
			return audience[slug], nil
		})
}

func crewStep() Step {
	return Step{ID: "tell", Type: StepNotify, Notify: &NotifyStep{To: "crew:ops", Body: "shipped"}}
}

// TestRunNotifyStep_CrewFansOutToEveryMember is the core #842 contract: one
// inbox item per crew member, each individually targeted.
func TestRunNotifyStep_CrewFansOutToEveryMember(t *testing.T) {
	fake := &fakeInboxNotifier{}
	exec := crewFanoutExecutor(fake, map[string][]string{"ops": {"u_ann", "u_bob", "u_cat"}}, nil)

	out, _, _, err := exec.runNotifyStep(context.Background(), crewStep(), RenderContext{}, notifyRunInput("ws_1", ""), "run_1")
	if err != nil {
		t.Fatalf("runNotifyStep: %v", err)
	}
	if len(fake.items) != 3 {
		t.Fatalf("want one item per crew member (3), got %d", len(fake.items))
	}
	got := []string{}
	for _, it := range fake.items {
		got = append(got, it.TargetUserID)
		if it.TargetRole != "" {
			t.Errorf("crew fan-out must target users, not roles; got role %q", it.TargetRole)
		}
		if it.Payload["crew_slug"] != "ops" {
			t.Errorf("item should carry the addressed crew slug, got %v", it.Payload["crew_slug"])
		}
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "u_ann,u_bob,u_cat" {
		t.Errorf("recipients = %v, want all three crew members", got)
	}
	if strings.HasPrefix(out, "notified:degraded") {
		t.Errorf("a fully-honoured crew fan-out must not be marked degraded, got %q", out)
	}
}

// TestRunNotifyStep_CrewFanoutSourceIDsAreDistinctAndIdempotent pins the
// idempotency key: one SourceID per (run, step, recipient), so re-running the
// same run (boot resume, retry) can't double-post to anyone — and two members
// can't collide on one key and silently lose a delivery.
func TestRunNotifyStep_CrewFanoutSourceIDsAreDistinctAndIdempotent(t *testing.T) {
	fake := &fakeInboxNotifier{}
	exec := crewFanoutExecutor(fake, map[string][]string{"ops": {"u_ann", "u_bob"}}, nil)
	ctx := context.Background()

	if _, _, _, err := exec.runNotifyStep(ctx, crewStep(), RenderContext{}, notifyRunInput("ws_1", ""), "run_1"); err != nil {
		t.Fatal(err)
	}
	first := map[string]string{}
	for _, it := range fake.items {
		if prev, dup := first[it.SourceID]; dup {
			t.Fatalf("two recipients share SourceID %q (%s and %s) — one delivery would be swallowed by the unique index",
				it.SourceID, prev, it.TargetUserID)
		}
		first[it.SourceID] = it.TargetUserID
	}
	if _, ok := first["run_1:tell:u_ann"]; !ok {
		t.Errorf("SourceIDs = %v, want per-recipient keys like run_1:tell:u_ann", first)
	}

	// Re-run the same run id: the SAME keys must come back, so the inbox's
	// (kind, source_id) unique index dedupes instead of double-posting.
	fake.items = nil
	if _, _, _, err := exec.runNotifyStep(ctx, crewStep(), RenderContext{}, notifyRunInput("ws_1", ""), "run_1"); err != nil {
		t.Fatal(err)
	}
	for _, it := range fake.items {
		if first[it.SourceID] != it.TargetUserID {
			t.Errorf("re-run produced SourceID %q for %s; want the same deterministic key as the first run (%v)",
				it.SourceID, it.TargetUserID, first)
		}
	}
}

// TestRunNotifyStep_CrewSoftCapIsPerRecipient: a member already at the cap is
// dropped, the others still receive the notice.
func TestRunNotifyStep_CrewSoftCapIsPerRecipient(t *testing.T) {
	fake := &fakeInboxNotifier{}
	counter := &fakeNoticeCounter{counts: map[string]int{"run_1|u_bob": perRunNotifyCap}}
	exec := crewFanoutExecutor(fake, map[string][]string{"ops": {"u_ann", "u_bob"}}, nil).
		WithNoticeCounter(counter.count)

	if _, _, _, err := exec.runNotifyStep(context.Background(), crewStep(), RenderContext{}, notifyRunInput("ws_1", ""), "run_1"); err != nil {
		t.Fatal(err)
	}
	if len(fake.items) != 1 || fake.items[0].TargetUserID != "u_ann" {
		t.Fatalf("capped member must be dropped and the rest delivered, got %+v", fake.items)
	}
}

// TestRunNotifyStep_CrewNonMembersDropped: a crew member who is no longer a
// workspace member is dropped from the fan-out (the membership guard applies
// per recipient), while the rest still get the notice.
func TestRunNotifyStep_CrewNonMembersDropped(t *testing.T) {
	fake := &fakeInboxNotifier{}
	exec := crewFanoutExecutor(fake, map[string][]string{"ops": {"u_ann", "u_ghost"}}, nil).
		WithMemberChecker(func(_ context.Context, _, uid string) (bool, error) { return uid != "u_ghost", nil })

	if _, _, _, err := exec.runNotifyStep(context.Background(), crewStep(), RenderContext{}, notifyRunInput("ws_1", ""), "run_1"); err != nil {
		t.Fatal(err)
	}
	if len(fake.items) != 1 || fake.items[0].TargetUserID != "u_ann" {
		t.Fatalf("non-member must be dropped, remaining member delivered; got %+v", fake.items)
	}
}

// TestRunNotifyStep_CrewDegradesNeverFails covers every "can't honour the
// crew" path: unknown slug, empty crew, resolver error, no resolver wired.
// All must land a workspace notice and NEVER fail the run.
func TestRunNotifyStep_CrewDegradesNeverFails(t *testing.T) {
	cases := []struct {
		name string
		exec func(f InboxNotifier) *Executor
	}{
		{"unknown slug", func(f InboxNotifier) *Executor {
			return crewFanoutExecutor(f, map[string][]string{"other": {"u_x"}}, nil)
		}},
		{"empty crew", func(f InboxNotifier) *Executor {
			return crewFanoutExecutor(f, map[string][]string{"ops": {}}, nil)
		}},
		{"resolver error", func(f InboxNotifier) *Executor {
			return crewFanoutExecutor(f, nil, errors.New("db down"))
		}},
		{"no resolver wired", func(f InboxNotifier) *Executor { return notifyExecutor(f) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeInboxNotifier{}
			out, _, _, err := tc.exec(fake).runNotifyStep(context.Background(), crewStep(), RenderContext{}, notifyRunInput("ws_1", ""), "run_1")
			if err != nil {
				t.Fatalf("crew targeting must never fail the run, got %v", err)
			}
			if len(fake.items) != 1 {
				t.Fatalf("want exactly 1 degraded workspace notice, got %d", len(fake.items))
			}
			if fake.items[0].TargetUserID != "" || fake.items[0].TargetRole != "" {
				t.Errorf("degraded notice must be workspace-wide, got user=%q role=%q",
					fake.items[0].TargetUserID, fake.items[0].TargetRole)
			}
			if fake.items[0].SourceID != "run_1:tell" {
				t.Errorf("degraded notice keeps the (run, step) key, got %q", fake.items[0].SourceID)
			}
			if !strings.HasPrefix(out, "notified:degraded") {
				t.Errorf("marker = %q, want notified:degraded (a silent fallback would be a bug)", out)
			}
		})
	}
}

// TestRunNotifyStep_CrewDryRunNoLookup: a dry run previews without expanding
// the audience or writing anything.
func TestRunNotifyStep_CrewDryRunNoLookup(t *testing.T) {
	fake := &fakeInboxNotifier{}
	called := false
	exec := notifyExecutor(fake).WithCrewAudienceResolver(func(_ context.Context, _, _ string) ([]string, error) {
		called = true
		return []string{"u_ann"}, nil
	})
	in := notifyRunInput("ws_1", "")
	in.Mode = ModeDryRun
	out, _, _, err := exec.runNotifyStep(context.Background(), crewStep(), RenderContext{}, in, "run_1")
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("dry run must not expand the crew audience")
	}
	if len(fake.items) != 0 || !strings.Contains(out, "preview") {
		t.Errorf("dry run must not write; got %d items, marker %q", len(fake.items), out)
	}
}

// TestCrewAudienceResolver_RealDB exercises the production resolver against
// the real schema, including the SECURITY-CRITICAL property: the lookup is
// scoped to (workspace_id, slug), so an identical slug in another tenant is
// never addressable.
func TestCrewAudienceResolver_RealDB(t *testing.T) {
	db := newNotifyIntegrationDB(t)
	mustExec(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws1','ws one','ws-one')`)
	mustExec(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws2','ws two','ws-two')`)
	for _, u := range []string{"u_ann", "u_bob", "u_evil"} {
		mustExec(t, db, fmt.Sprintf(`INSERT INTO users (id, email) VALUES ('%s','%s@example.com')`, u, u))
	}
	// Same slug "ops" in BOTH workspaces — the cross-tenant trap.
	mustExec(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('c1','ws1','Ops','ops')`)
	mustExec(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('c2','ws2','Ops','ops')`)
	mustExec(t, db, `INSERT INTO crew_members (id, crew_id, user_id) VALUES ('cm1','c1','u_ann')`)
	mustExec(t, db, `INSERT INTO crew_members (id, crew_id, user_id) VALUES ('cm2','c1','u_bob')`)
	mustExec(t, db, `INSERT INTO crew_members (id, crew_id, user_id) VALUES ('cm3','c2','u_evil')`)
	// A soft-deleted crew resolves to nobody.
	mustExec(t, db, `INSERT INTO crews (id, workspace_id, name, slug, deleted_at) VALUES ('c3','ws1','Old','retired','2026-01-01T00:00:00Z')`)
	mustExec(t, db, `INSERT INTO crew_members (id, crew_id, user_id) VALUES ('cm4','c3','u_ann')`)

	resolve := NewCrewAudienceResolver(db)
	ctx := context.Background()

	got, err := resolve(ctx, "ws1", "ops")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if strings.Join(got, ",") != "u_ann,u_bob" {
		t.Fatalf("ws1/ops audience = %v, want [u_ann u_bob]", got)
	}
	// SECURITY: ws1's slug must not reach ws2's crew, and vice versa.
	for _, id := range got {
		if id == "u_evil" {
			t.Fatal("cross-tenant leak: ws2's crew member resolved for ws1")
		}
	}
	other, err := resolve(ctx, "ws2", "ops")
	if err != nil || strings.Join(other, ",") != "u_evil" {
		t.Fatalf("ws2/ops audience = %v (err %v), want [u_evil]", other, err)
	}
	// A workspace that has no such crew gets an empty audience, not someone
	// else's members.
	if got, err := resolve(ctx, "ws2", "nope"); err != nil || len(got) != 0 {
		t.Errorf("unknown slug = %v (err %v), want empty", got, err)
	}
	if got, err := resolve(ctx, "ws1", "retired"); err != nil || len(got) != 0 {
		t.Errorf("soft-deleted crew = %v (err %v), want empty", got, err)
	}
	// Nil db / empty ids short-circuit without a query.
	if got, err := NewCrewAudienceResolver(nil)(ctx, "ws1", "ops"); err != nil || len(got) != 0 {
		t.Errorf("nil db = %v (err %v), want empty", got, err)
	}
	if got, err := resolve(ctx, "", "ops"); err != nil || len(got) != 0 {
		t.Errorf("empty workspace = %v (err %v), want empty", got, err)
	}
}

// TestRunNotifyStep_CrewFanoutEndToEnd drives the whole step against the real
// DB-backed resolver + inbox sink: crew members each get exactly one row, and
// a re-run of the same run id doesn't double-post.
func TestRunNotifyStep_CrewFanoutEndToEnd(t *testing.T) {
	db := newNotifyIntegrationDB(t)
	mustExec(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws1','ws','ws')`)
	mustExec(t, db, `INSERT INTO users (id, email) VALUES ('u_ann','ann@example.com')`)
	mustExec(t, db, `INSERT INTO users (id, email) VALUES ('u_bob','bob@example.com')`)
	mustExec(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('c1','ws1','Ops','ops')`)
	mustExec(t, db, `INSERT INTO crew_members (id, crew_id, user_id) VALUES ('cm1','c1','u_ann')`)
	mustExec(t, db, `INSERT INTO crew_members (id, crew_id, user_id) VALUES ('cm2','c1','u_bob')`)

	exec := NewExecutor(nil, nil, nil, nil).
		WithInboxNotifier(&sqlInboxNotifier{db: db, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}).
		WithCrewAudienceResolver(NewCrewAudienceResolver(db)).
		WithNoticeCounter(NewRunNoticeCounter(db))

	ctx := context.Background()
	for i := 0; i < 2; i++ { // run twice — same run id
		if _, _, _, err := exec.runNotifyStep(ctx, crewStep(), RenderContext{}, notifyRunInput("ws1", ""), "run_1"); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE kind = ? AND source_id LIKE 'run_1:%'`, inbox.KindMessage).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("want exactly 2 rows (one per crew member, idempotent across re-runs), got %d", n)
	}
}
