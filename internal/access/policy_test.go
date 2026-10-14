package access

import (
	"errors"
	"reflect"
	"testing"
)

func TestPolicySnapshotRequiresCurrentTrustedAdmin(t *testing.T) {
	s := fixture(t)
	want := []Right{{"agent", "a", "chat"}, {"project", "p1", "read"}}
	m := policy(t, s, "h1", want...)
	p, err := s.Policy(t.Context(), "owner", "h1", "w")
	if err != nil || p.Membership != m || !reflect.DeepEqual(p.Rights, want) {
		t.Fatalf("snapshot %+v: %v", p, err)
	}
	for _, actor := range []string{"h1", "h2", "missing"} {
		if _, err = s.Policy(t.Context(), actor, "h1", "w"); !errors.Is(err, ErrDenied) {
			t.Fatalf("%s read policy: %v", actor, err)
		}
	}
	if _, err = s.DB.ExecContext(t.Context(), `UPDATE workspace_members SET role='MEMBER' WHERE id='mo'`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Policy(t.Context(), "owner", "h1", "w"); !errors.Is(err, ErrDenied) {
		t.Fatalf("removed admin read policy: %v", err)
	}
}

func TestOwnPolicyRequiresLiveAccountAndMembership(t *testing.T) {
	s := fixture(t)
	want := []Right{{"agent", "a", "chat"}}
	policy(t, s, "h1", want...)
	p, err := s.OwnPolicy(t.Context(), "h1", "w")
	if err != nil || p.ID != "m1" || !reflect.DeepEqual(p.Rights, want) {
		t.Fatalf("own policy %+v: %v", p, err)
	}
	for _, workspace := range []string{"other", "missing"} {
		if _, err = s.OwnPolicy(t.Context(), "h1", workspace); !errors.Is(err, ErrDenied) {
			t.Fatalf("foreign workspace %s: %v", workspace, err)
		}
	}
	for _, statement := range []string{
		`UPDATE users SET suspended_at='' WHERE id='h1'`,
		`UPDATE users SET suspended_at=NULL WHERE id='h1'; DELETE FROM workspace_members WHERE id='m1'`,
	} {
		if _, err = s.DB.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
		if _, err = s.OwnPolicy(t.Context(), "h1", "w"); !errors.Is(err, ErrDenied) {
			t.Fatalf("removed/suspended account: %v", err)
		}
	}
}
