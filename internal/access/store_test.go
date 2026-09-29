package access

import (
	"context"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/testutil"
)

func fixture(t *testing.T) Store {
	t.Helper()
	db := testutil.MigratedSQLDB(t)
	statements := []string{
		`INSERT INTO users(id,email) VALUES ('owner','owner@access.test'),('h1','h1@access.test'),('h2','h2@access.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES ('w','Workspace','access-w'),('other','Other','access-other')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES ('mo','w','owner','OWNER'),('m1','w','h1','MEMBER'),('m2','w','h2','MEMBER')`,
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES ('crew','w','Crew','access-crew')`,
		`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role) VALUES ('a','w','crew','A','access-a','AGENT'),('b','w','crew','B','access-b','AGENT')`,
		`INSERT INTO projects(id,workspace_id,name,slug) VALUES ('p1','w','P1','p1'),('p2','w','P2','p2'),('foreign','other','Foreign','foreign')`,
		`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES ('c1','w','a','h1','private'),('c2','w','a','h2','private')`,
	}
	for _, q := range statements {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return Store{db}
}

func policy(t *testing.T, s Store, user string, rights ...Right) Membership {
	t.Helper()
	m, err := s.Membership(t.Context(), user, "w")
	if err != nil {
		t.Fatal(err)
	}
	m, err = s.Replace(t.Context(), "owner", user, "w", "restricted", m, rights)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestExactResourceOperationsAndRevocation(t *testing.T) {
	s := fixture(t)
	rights := []Right{{"agent", "a", "chat"}, {"project", "p1", "read"}}
	m := policy(t, s, "h1", rights...)
	policy(t, s, "h2", Right{"agent", "b", "chat"}, Right{"project", "p2", "read"})
	for _, tc := range []struct {
		user    string
		r       Right
		allowed bool
	}{
		{"h1", rights[0], true}, {"h1", rights[1], true},
		{"h1", Right{"agent", "a", "run"}, false}, {"h1", Right{"agent", "b", "chat"}, false},
		{"h1", Right{"project", "p1", "write"}, false}, {"h1", Right{"project", "p2", "read"}, false},
		{"h2", Right{"agent", "b", "chat"}, true}, {"h2", Right{"project", "p1", "read"}, false},
		{"h1", Right{"project", "foreign", "read"}, false}, {"h1", Right{"agent", "a", "anything"}, false},
	} {
		err := s.Check(t.Context(), tc.user, "w", tc.r)
		if (err == nil) != tc.allowed {
			t.Errorf("%s %+v: %v", tc.user, tc.r, err)
		}
	}
	if _, err := s.Replace(t.Context(), "h2", "h1", "w", "trusted", m, nil); !errors.Is(err, ErrDenied) {
		t.Fatalf("self escalation: %v", err)
	}
	if _, err := s.Replace(t.Context(), "owner", "h1", "w", "restricted", m, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Replace(t.Context(), "owner", "h1", "w", "restricted", m, rights); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale restore: %v", err)
	}
	if err := s.Check(t.Context(), "h1", "w", rights[0]); !errors.Is(err, ErrDenied) {
		t.Fatalf("revoked: %v", err)
	}
}

func TestAttemptsBindTwoClientsCurrentGrantsAndRetry(t *testing.T) {
	s := fixture(t)
	right := Right{"agent", "a", "run"}
	policy(t, s, "h1", right)
	policy(t, s, "h2", right)
	h1, a1, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	h2, a2, err := s.Admit(t.Context(), "h2", "w", "a", "c2", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if a1.Scope == a2.Scope || h1 == h2 {
		t.Fatal("clients share authority/data scope")
	}
	if _, _, err = s.Admit(t.Context(), "h2", "w", "a", "c1", "", nil); !errors.Is(err, ErrDenied) {
		t.Fatalf("foreign conversation admitted: %v", err)
	}
	var persisted string
	if err = s.DB.QueryRow(`SELECT handle_hash FROM access_attempts WHERE id=?`, a1.ID).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if persisted == h1 {
		t.Fatal("raw handle persisted")
	}
	if _, err = s.Resolve(t.Context(), a1.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("public attempt id is a capability: %v", err)
	}
	// A separate store after recovery must use durable authority.
	recovered := Store{s.DB}
	if _, err = recovered.Resolve(t.Context(), h1); err != nil {
		t.Fatal(err)
	}
	policy(t, s, "h1")
	policy(t, s, "h1", right)
	if _, err = s.Resolve(t.Context(), h1); !errors.Is(err, ErrDenied) {
		t.Fatalf("old attempt revived: %v", err)
	}
	if _, err = s.Resolve(t.Context(), h2); err != nil {
		t.Fatalf("unrelated client revoked: %v", err)
	}
	retry, a3, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if a3.Scope == a1.Scope || a3.Generation <= a1.Generation || retry == h1 {
		t.Fatal("retry reused revoked authority/state")
	}
	if _, err = s.DB.Exec(`DELETE FROM workspace_members WHERE id='m1'`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES ('m1new','w','h1','MEMBER')`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Resolve(t.Context(), retry); !errors.Is(err, ErrDenied) {
		t.Fatalf("rejoin revived attempt: %v", err)
	}
}

func TestDelegationOnlyNarrowsAndParentRevokes(t *testing.T) {
	s := fixture(t)
	rights := []Right{{"agent", "a", "run"}, {"agent", "b", "run"}, {"agent", "b", "delegate"}, {"project", "p1", "read"}, {"project", "p2", "read"}}
	policy(t, s, "h1", rights...)
	parent, _, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", rights[:4])
	if err != nil {
		t.Fatal(err)
	}
	child, _, err := s.Admit(t.Context(), "h1", "w", "b", "c1", parent, []Right{rights[3]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Resolve(t.Context(), child); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.Admit(t.Context(), "h1", "w", "b", "c1", parent, []Right{rights[4]}); !errors.Is(err, ErrDenied) {
		t.Fatalf("child widened rights: %v", err)
	}
	if err = s.RevokeAttempt(t.Context(), parent); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Resolve(t.Context(), child); !errors.Is(err, ErrDenied) {
		t.Fatalf("child survived parent revoke: %v", err)
	}
}

func TestFailuresAndForeignResourcesDeny(t *testing.T) {
	s := fixture(t)
	m, err := s.Membership(t.Context(), "h1", "w")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Replace(t.Context(), "owner", "h1", "w", "restricted", m, []Right{{"project", "foreign", "read"}}); !errors.Is(err, ErrDenied) {
		t.Fatalf("cross workspace grant: %v", err)
	}
	policy(t, s, "h1", Right{"agent", "a", "run"})
	handle, _, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = s.Resolve(ctx, handle); err == nil {
		t.Fatal("canceled lookup allowed")
	}
	if _, err = s.DB.Exec(`UPDATE agents SET deleted_at='2026-09-29' WHERE id='a'`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Resolve(t.Context(), handle); !errors.Is(err, ErrDenied) {
		t.Fatalf("deleted agent: %v", err)
	}
}

func TestAudienceRemovalAndRegrantCannotRestoreOldState(t *testing.T) {
	s := fixture(t)
	if _, err := s.DB.Exec(`UPDATE chats SET visibility='group' WHERE id='c1'; INSERT INTO chat_participants(chat_id,user_id,role) VALUES('c1','h2','member')`); err != nil {
		t.Fatal(err)
	}
	policy(t, s, "h2", Right{"agent", "a", "run"})
	handle, before, err := s.Admit(t.Context(), "h2", "w", "a", "c1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(`DELETE FROM chat_participants WHERE chat_id='c1' AND user_id='h2'; INSERT INTO chat_participants(chat_id,user_id,role) VALUES('c1','h2','member')`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Resolve(t.Context(), handle); !errors.Is(err, ErrDenied) {
		t.Fatalf("removed participant's attempt revived: %v", err)
	}
	_, after, err := s.Admit(t.Context(), "h2", "w", "a", "c1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if before.Scope == after.Scope {
		t.Fatal("participant rejoin restored old derived state")
	}
}

func TestDeletedResourceCannotRegainOldGrant(t *testing.T) {
	s := fixture(t)
	right := Right{"agent", "a", "run"}
	policy(t, s, "h1", right)
	if _, err := s.DB.Exec(`UPDATE agents SET deleted_at='2026-09-29' WHERE id='a'; UPDATE agents SET deleted_at=NULL WHERE id='a'`); err != nil {
		t.Fatal(err)
	}
	if err := s.Check(t.Context(), "h1", "w", right); !errors.Is(err, ErrDenied) {
		t.Fatalf("restored resource regained grant: %v", err)
	}
}

func TestStalePolicyCannotGrantToRejoinedMembership(t *testing.T) {
	s := fixture(t)
	old, err := s.Membership(t.Context(), "h1", "w")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(`DELETE FROM workspace_members WHERE id='m1'; INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('new-m1','w','h1','MEMBER')`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Replace(t.Context(), "owner", "h1", "w", "restricted", old, []Right{{"agent", "a", "run"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale policy applied to a new membership: %v", err)
	}
}

func TestDeletingParentAttemptDeletesDelegatedDescendants(t *testing.T) {
	s := fixture(t)
	rights := []Right{{"agent", "a", "run"}, {"agent", "b", "run"}, {"agent", "b", "delegate"}}
	policy(t, s, "h1", rights...)
	parent, attempt, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", rights)
	if err != nil {
		t.Fatal(err)
	}
	child, _, err := s.Admit(t.Context(), "h1", "w", "b", "c1", parent, rights[1:])
	if err != nil {
		t.Fatal(err)
	}
	grandchild, _, err := s.Admit(t.Context(), "h1", "w", "b", "c1", child, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(`DELETE FROM access_attempts WHERE id=?`, attempt.ID); err != nil {
		t.Fatal(err)
	}
	for _, handle := range []string{parent, child, grandchild} {
		if _, err = s.Resolve(t.Context(), handle); !errors.Is(err, ErrDenied) {
			t.Fatalf("descendant survived parent deletion: %v", err)
		}
	}
	var remaining int
	if err = s.DB.QueryRow(`SELECT count(*) FROM access_attempts`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("remaining=%d error=%v", remaining, err)
	}
}
