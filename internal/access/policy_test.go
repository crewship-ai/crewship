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
