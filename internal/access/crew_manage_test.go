package access

import (
	"errors"
	"testing"
)

func TestTrustedCrewManagePolicyPreservesAccountMode(t *testing.T) {
	s := fixture(t)
	m, err := s.Membership(t.Context(), "h1", "w")
	if err != nil {
		t.Fatal(err)
	}
	right := Right{"crew", "crew", "manage"}
	if _, err := s.Replace(t.Context(), "owner", "h1", "w", "trusted", m, []Right{right}); err != nil {
		t.Fatalf("explicit trusted crew Manage: %v", err)
	}
	p, err := s.Policy(t.Context(), "owner", "h1", "w")
	if err != nil || p.Mode != "trusted" || len(p.Rights) != 1 || p.Rights[0] != right {
		t.Fatalf("trusted policy: %+v, %v", p, err)
	}
	if err := s.CrewManage(t.Context(), "h1", "w", "crew"); err != nil {
		t.Fatal(err)
	}
	if err := s.CrewManage(t.Context(), "h2", "w", "crew"); !errors.Is(err, ErrDenied) {
		t.Fatal("unshared trusted member acquired Manage", err)
	}
	if _, err := s.Replace(t.Context(), "owner", "h1", "w", "restricted", p.Membership, nil); !errors.Is(err, ErrGuestCeiling) {
		t.Fatal("implicit removal during downshift", err)
	}
	if _, err := s.Replace(t.Context(), "owner", "h1", "w", "trusted", p.Membership, []Right{}); err != nil {
		t.Fatal(err)
	}
	m, _ = s.Membership(t.Context(), "h1", "w")
	if _, err := s.Replace(t.Context(), "owner", "h1", "w", "restricted", m, nil); err != nil {
		t.Fatal("explicit revoke then downshift", err)
	}
}

func TestTrustedCrewManageRecipientAndRevocationCeilings(t *testing.T) {
	for _, role := range []string{"MEMBER", "MANAGER", "VIEWER", "ADMIN", "OWNER"} {
		t.Run(role, func(t *testing.T) {
			s := fixture(t)
			if _, err := s.DB.Exec(`UPDATE workspace_members SET role=? WHERE id='m1'`, role); err != nil {
				t.Fatal(err)
			}
			m, _ := s.Membership(t.Context(), "h1", "w")
			_, err := s.Replace(t.Context(), "owner", "h1", "w", "trusted", m, []Right{{"crew", "crew", "manage"}})
			if (err == nil) != (role == "MEMBER" || role == "MANAGER") {
				t.Fatalf("recipient %s: %v", role, err)
			}
		})
	}
	for name, mutation := range map[string]string{
		"recipient downgrade": `UPDATE workspace_members SET role='VIEWER' WHERE id='m1'`,
		"issuer downgrade":    `UPDATE workspace_members SET role='MEMBER' WHERE id='mo'`,
		"recipient suspended": `UPDATE users SET suspended_at='now' WHERE id='h1'`,
		"issuer suspended":    `UPDATE users SET suspended_at='now' WHERE id='owner'`,
		"retired crew":        `UPDATE crews SET deleted_at='now' WHERE id='crew'`,
		"moved crew":          `UPDATE crews SET workspace_id='other' WHERE id='crew'`,
		"global Guest":        `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('guest-elsewhere','other','h1','MEMBER','restricted')`,
		"local Guest":         `UPDATE workspace_members SET access_mode='restricted' WHERE id='m1'`,
		"revoked":             `DELETE FROM access_grants WHERE member_id='m1'`,
	} {
		t.Run(name, func(t *testing.T) {
			s := fixture(t)
			m, _ := s.Membership(t.Context(), "h1", "w")
			if _, err := s.Replace(t.Context(), "owner", "h1", "w", "trusted", m, []Right{{"crew", "crew", "manage"}}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB.Exec(mutation); err != nil {
				t.Fatal(err)
			}
			if err := s.CrewManage(t.Context(), "h1", "w", "crew"); !errors.Is(err, ErrDenied) {
				t.Fatal("stale Manage authority", err)
			}
			var live int
			if err := s.DB.QueryRow(`SELECT count(*) FROM live_access_grants WHERE member_id='m1' AND operation='manage'`).Scan(&live); err != nil || live != 0 {
				t.Fatalf("inert Manage still live: %d, %v", live, err)
			}
		})
	}
}

func TestTrustedCrewManageNeverBecomesGuestOrAgentAuthority(t *testing.T) {
	s := fixture(t)
	m := policy(t, s, "h1")
	if _, err := s.Replace(t.Context(), "owner", "h1", "w", "restricted", m, []Right{{"crew", "crew", "manage"}}); !errors.Is(err, ErrGuestCeiling) {
		t.Fatal("Guest Manage accepted", err)
	}
	if err := ValidateRights(t.Context(), s.DB, "w", []Right{{"crew", "crew", "manage"}}); !errors.Is(err, ErrGuestCeiling) {
		t.Fatal("Guest invitation Manage accepted", err)
	}
	// A raw retained row is inert even if the application write gate is absent.
	if _, err := s.DB.Exec(`INSERT INTO access_grants(id,member_id,resource_kind,crew_id,operation,created_by,created_at) VALUES('forged','m1','crew','crew','manage','owner','now')`); err != nil {
		t.Fatal(err)
	}
	for _, r := range []Right{{"crew", "crew", "manage"}, {"agent", "a", "run"}, {"agent", "a", "delegate"}} {
		if err := s.Check(t.Context(), "h1", "w", r); !errors.Is(err, ErrDenied) {
			t.Fatalf("Guest inherited %+v: %v", r, err)
		}
	}
}

func TestTrustedCrewManageRetainedInertSharesCanBeRevokedSeparately(t *testing.T) {
	s := fixture(t)
	if _, err := s.DB.Exec(`INSERT INTO crews(id,workspace_id,name,slug) VALUES('second','w','Second','second'),('new','w','New','new')`); err != nil {
		t.Fatal(err)
	}
	m, _ := s.Membership(t.Context(), "h1", "w")
	first, second := Right{"crew", "crew", "manage"}, Right{"crew", "second", "manage"}
	if _, err := s.Replace(t.Context(), "owner", "h1", "w", "trusted", m, []Right{first, second}); err != nil {
		t.Fatal(err)
	}
	var id, stamp string
	if err := s.DB.QueryRow(`SELECT id,created_at FROM access_grants WHERE crew_id='second'`).Scan(&id, &stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`UPDATE workspace_members SET role='VIEWER' WHERE id='m1'`); err != nil {
		t.Fatal(err)
	}
	m, _ = s.Membership(t.Context(), "h1", "w")
	if _, err := s.Replace(t.Context(), "owner", "h1", "w", "trusted", m, []Right{second}); err != nil {
		t.Fatal("partial revoke of inert shares", err)
	}
	p, err := s.Policy(t.Context(), "owner", "h1", "w")
	if err != nil || len(p.GrantDetails) != 1 || p.GrantDetails[0].Live || p.GrantDetails[0].IssuerID != "owner" || p.GrantDetails[0].CreatedAt != stamp {
		t.Fatalf("retained inactive provenance: %+v %v", p, err)
	}
	var retainedID string
	if err := s.DB.QueryRow(`SELECT id FROM access_grants WHERE crew_id='second'`).Scan(&retainedID); err != nil || retainedID != id {
		t.Fatal("retained share reissued", retainedID, err)
	}
	for _, rights := range [][]Right{{second, {"crew", "new", "manage"}}, {second}} {
		var reissue []Right
		if len(rights) == 1 {
			reissue = []Right{second}
		}
		if _, err := s.Replace(t.Context(), "owner", "h1", "w", "trusted", p.Membership, rights, reissue...); !errors.Is(err, ErrDenied) {
			t.Fatal("ineligible recipient received new or reissued authority", err)
		}
	}
	if _, err := s.Replace(t.Context(), "owner", "h1", "w", "restricted", p.Membership, nil); !errors.Is(err, ErrGuestCeiling) {
		t.Fatal("inactive share escaped Guest ceiling", err)
	}
}
