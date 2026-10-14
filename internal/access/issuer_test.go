package access

import (
	"errors"
	"testing"
)

func TestGrantIssuerMustRetainLiveWorkspaceAuthority(t *testing.T) {
	for _, tc := range []struct {
		name, revoke, restore string
	}{
		{"demoted", `UPDATE workspace_members SET role='MEMBER' WHERE id='mo'`, `UPDATE workspace_members SET role='OWNER' WHERE id='mo'`},
		{"removed", `DELETE FROM workspace_members WHERE id='mo'`, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('mo-new','w','owner','OWNER')`},
		{"suspended", `UPDATE users SET suspended_at='' WHERE id='owner'`, `UPDATE users SET suspended_at=NULL WHERE id='owner'`},
		{"restricted elsewhere", `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('mo-other','other','owner','VIEWER','restricted')`, `DELETE FROM workspace_members WHERE id='mo-other'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fixture(t)
			right := Right{"agent", "a", "run"}
			before := policy(t, s, "h1", right)
			handle, _, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.DB.ExecContext(t.Context(), tc.revoke); err != nil {
				t.Fatal(err)
			}
			if err = s.Check(t.Context(), "h1", "w", right); !errors.Is(err, ErrIssuerDenied) || !errors.Is(err, ErrDenied) {
				t.Fatalf("grant issued by unavailable administrator remains usable: %v", err)
			}
			after, err := s.Membership(t.Context(), "h1", "w")
			if err != nil || after.Revision <= before.Revision {
				t.Fatalf("issuer revocation did not fence recipient revision: before=%+v after=%+v err=%v", before, after, err)
			}
			if _, err = s.Replace(t.Context(), "owner", "h1", "w", "restricted", after, []Right{right}); !errors.Is(err, ErrDenied) {
				t.Fatalf("unavailable issuer created a replacement share: %v", err)
			}
			if _, err = s.DB.ExecContext(t.Context(), tc.restore); err != nil {
				t.Fatal(err)
			}
			if err = s.Check(t.Context(), "h1", "w", right); err != nil {
				t.Fatalf("restored issuer cannot authorize a new use of the preserved share: %v", err)
			}
			if _, err = s.Resolve(t.Context(), handle); !errors.Is(err, ErrDenied) {
				t.Fatalf("administrator restoration revived admitted work: %v", err)
			}
			if _, _, err = s.Admit(t.Context(), "h1", "w", "a", "c1", "", nil); err != nil {
				t.Fatalf("restored authority cannot admit fresh work: %v", err)
			}
		})
	}
}

func TestGrantIssuerRevocationPreservesOtherAdministratorsShares(t *testing.T) {
	s := fixture(t)
	for _, sql := range []string{
		`UPDATE workspace_members SET role='ADMIN' WHERE id='m2'`,
		`INSERT INTO users(id,email) VALUES('h3','h3@access.test')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('m3','w','h3','VIEWER')`,
	} {
		if _, err := s.DB.ExecContext(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
	right := Right{"agent", "a", "chat"}
	policy(t, s, "h1", right)
	m, err := s.Membership(t.Context(), "h3", "w")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Replace(t.Context(), "h2", "h3", "w", "restricted", m, []Right{right}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.ExecContext(t.Context(), `UPDATE workspace_members SET role='MEMBER' WHERE id='mo'`); err != nil {
		t.Fatal(err)
	}
	if err = s.Check(t.Context(), "h3", "w", right); err != nil {
		t.Fatalf("another issuer's share revoked: %v", err)
	}
}

func TestGrantIssuerRoleInOtherWorkspaceDoesNotRevokeLocalShare(t *testing.T) {
	s := fixture(t)
	right := Right{"agent", "a", "chat"}
	before := policy(t, s, "h1", right)
	for _, sql := range []string{
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('mo-other','other','owner','ADMIN')`,
		`UPDATE workspace_members SET role='VIEWER' WHERE id='mo-other'`,
		`DELETE FROM workspace_members WHERE id='mo-other'`,
	} {
		if _, err := s.DB.ExecContext(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
		if err := s.Check(t.Context(), "h1", "w", right); err != nil {
			t.Fatalf("unrelated workspace membership revoked local grant: %v", err)
		}
		if after, err := s.Membership(t.Context(), "h1", "w"); err != nil || after != before {
			t.Fatalf("unrelated workspace change invalidated local attempts: %+v %v", after, err)
		}
	}
}
