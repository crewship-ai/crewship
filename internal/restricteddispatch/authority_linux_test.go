package restricteddispatch

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/testutil"
)

func fixture(t *testing.T) Authority {
	t.Helper()
	db := testutil.MigratedSQLDB(t)
	for _, query := range []string{
		`INSERT INTO users(id,email) VALUES ('owner','owner@dispatch.test'),('h1','h1@dispatch.test'),('h2','h2@dispatch.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES ('w','Workspace','dispatch-w')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES ('mo','w','owner','OWNER'),('m1','w','h1','MEMBER'),('m2','w','h2','MEMBER')`,
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES ('crew','w','Crew','dispatch-crew')`,
		`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role) VALUES ('a','w','crew','A','dispatch-a','AGENT')`,
		`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES ('c1','w','a','h1','private'),('c2','w','a','h2','private')`,
	} {
		if _, err := db.ExecContext(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	a := Authority{Store: access.Store{DB: db}}
	for _, user := range []string{"h1", "h2"} {
		setRights(t, a, user, []access.Right{{Kind: "agent", ID: "a", Operation: "run"}})
	}
	return a
}

func setRights(t *testing.T, a Authority, user string, rights []access.Right) {
	t.Helper()
	m, err := a.Store.Membership(t.Context(), user, "w")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Store.Replace(t.Context(), "owner", user, "w", "restricted", m, rights); err != nil {
		t.Fatal(err)
	}
}

func command(args ...string) BuildCommand {
	return func(context.Context, access.Attempt) ([]string, error) { return args, nil }
}

func TestDurableRuntimeAuthority(t *testing.T) {
	a := fixture(t)
	ctx := t.Context()
	args := []string{"/bin/sh", "-c", "printf own-context"}
	h1, first, err := a.Prepare(ctx, "h1", "w", "a", "c1", "", nil, command(args...))
	if err != nil {
		t.Fatal(err)
	}
	h2, second, err := a.Prepare(ctx, "h2", "w", "a", "c2", "", nil, command("/bin/true"))
	if err != nil {
		t.Fatal(err)
	}
	if first.Scope == second.Scope || first.ID == second.ID {
		t.Fatal("two humans share execution scope")
	}
	// Reconstruct the adapter: no in-memory allow snapshot/command is required.
	a = Authority{Store: a.Store}
	plan, err := a.Resolve(ctx, h1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.Command, args) || plan.Principal != "h1" || plan.Scope != first.Scope || plan.Attempt != first.ID || plan.OriginID != "c1" || plan.Mode != "restricted" || plan.Expires.After(time.Now().Add(15*time.Second)) {
		t.Fatalf("incorrect runtime authority: %+v", plan)
	}
	args[2] = "mutated-after-admission"
	if p, err := a.Resolve(ctx, h1); err != nil || p.Command[2] != "printf own-context" {
		t.Fatalf("mutable command: %+v %v", p, err)
	}
	if _, err := a.Store.DB.ExecContext(ctx, `UPDATE restricted_launches SET command_json='["/bin/false"]' WHERE attempt_id=?`, first.ID); err == nil {
		t.Fatal("persisted launch could be rewritten")
	}
	setRights(t, a, "h1", nil)
	if _, err := a.Resolve(ctx, h1); !errors.Is(err, access.ErrDenied) {
		t.Fatalf("revoked runtime lease: %v", err)
	}
	if _, err := a.Secrets(ctx, h1); !errors.Is(err, access.ErrDenied) {
		t.Fatalf("revoked secret admission: %v", err)
	}
	if _, err := a.Resolve(ctx, h2); err != nil {
		t.Fatalf("unrelated human denied: %v", err)
	}
	setRights(t, a, "h1", []access.Right{{Kind: "agent", ID: "a", Operation: "run"}})
	if _, err := a.Resolve(ctx, h1); !errors.Is(err, access.ErrDenied) {
		t.Fatalf("regrant revived old launch: %v", err)
	}
}

func TestAdmissionBeforeContextAndAfterBuild(t *testing.T) {
	a := fixture(t)
	ctx := t.Context()
	called := false
	build := func(context.Context, access.Attempt) ([]string, error) {
		called = true
		return []string{"/bin/true"}, nil
	}
	if _, _, err := a.Prepare(ctx, "h2", "w", "a", "c1", "", nil, build); !errors.Is(err, access.ErrDenied) || called {
		t.Fatalf("foreign context was built: called=%v err=%v", called, err)
	}
	var id string
	_, _, err := a.Prepare(ctx, "h1", "w", "a", "c1", "", nil, func(_ context.Context, attempt access.Attempt) ([]string, error) {
		id = attempt.ID
		setRights(t, a, "h1", nil)
		return []string{"/bin/true"}, nil
	})
	if !errors.Is(err, access.ErrDenied) {
		t.Fatalf("revoked during context construction: %v", err)
	}
	var launches int
	if err := a.Store.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM restricted_launches WHERE attempt_id=?`, id).Scan(&launches); err != nil || launches != 0 {
		t.Fatalf("failed build persisted executable payload: %d %v", launches, err)
	}
}

func TestUnpreparedAndCanceledAttemptsCannotLaunch(t *testing.T) {
	a := fixture(t)
	ctx := t.Context()
	handle, _, err := a.Store.Admit(ctx, "h1", "w", "a", "c1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Resolve(ctx, handle); err == nil {
		t.Fatal("attempt without execution payload admitted")
	}
	buildCtx, cancel := context.WithCancel(ctx)
	var id string
	_, _, err = a.Prepare(buildCtx, "h1", "w", "a", "c1", "", nil, func(_ context.Context, attempt access.Attempt) ([]string, error) {
		id = attempt.ID
		cancel()
		return nil, context.Canceled
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled build: %v", err)
	}
	var revoked bool
	if err := a.Store.DB.QueryRowContext(ctx, `SELECT revoked_at IS NOT NULL FROM access_attempts WHERE id=?`, id).Scan(&revoked); err != nil || !revoked {
		t.Fatalf("canceled preparation retained authority: %v %v", revoked, err)
	}
}
