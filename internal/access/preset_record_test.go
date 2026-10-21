package access

import (
	"errors"
	"testing"
)

func TestPresetRecordSelectionRequiresCanonicalState(t *testing.T) {
	s := fixture(t)
	policy(t, s, "h1", Right{"project", "p1", "list"}, Right{"project", "p1", "read"}, Right{"project", "p1", "delete_issues"}, Right{"project", "p2", "read"})
	m, err := s.Membership(t.Context(), "h1", "w")
	if err != nil {
		t.Fatal(err)
	}
	req := PresetRequest{MembershipID: m.ID, Revision: m.Revision, Resource: PresetResource{Kind: "project", ID: "p1"}, PresetID: "project-levels", Version: 1, Level: "view"}
	tx, err := s.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPresetSelectionIn(t.Context(), tx, "owner", "h1", "w", m, req); err != nil {
		tx.Rollback()
		t.Fatalf("record explicit supported choice after matching canonical grants:%v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rows := selections(t, s)
	if len(rows) != 1 || rows[0].State != "selected" || rows[0].Level != "view" || rows[0].SelectedBy == nil || *rows[0].SelectedBy != "owner" || rows[0].SelectedAt == "" {
		t.Fatalf("choice not authenticated server state:%+v", rows)
	}
	if err := s.RecordPresetSelectionIn(t.Context(), nil, "owner", "h1", "w", m, req); !errors.Is(err, ErrDenied) {
		t.Fatalf("nil transaction accepted:%v", err)
	}
	for name, change := range map[string]func(*Membership, *PresetRequest){
		"stale":            func(m *Membership, _ *PresetRequest) { m.Revision-- },
		"incarnation":      func(m *Membership, _ *PresetRequest) { m.ID = "replacement" },
		"different-bits":   func(_ *Membership, r *PresetRequest) { r.Level = "manage" },
		"unknown-version":  func(_ *Membership, r *PresetRequest) { r.Version = 2 },
		"wrong-kind":       func(_ *Membership, r *PresetRequest) { r.Resource.Kind = "routine" },
		"foreign-resource": func(_ *Membership, r *PresetRequest) { r.Resource.ID = "foreign" },
	} {
		t.Run(name, func(t *testing.T) {
			expected, input := m, req
			change(&expected, &input)
			tx, err := s.DB.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err := s.RecordPresetSelectionIn(t.Context(), tx, "owner", "h1", "w", expected, input); err == nil {
				t.Fatal("recorded unsupported or stale choice")
			}
		})
	}
	tx, err = s.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := s.RecordPresetSelectionIn(t.Context(), tx, "h1", "h1", "w", m, req); !errors.Is(err, ErrDenied) {
		t.Fatalf("restricted selector accepted:%v", err)
	}
}

func TestPresetRecordRollbackAndReadOnlyCanonicalRows(t *testing.T) {
	s := fixture(t)
	policy(t, s, "h1", Right{"project", "p1", "list"}, Right{"project", "p1", "read"}, Right{"project", "p2", "read"})
	before, err := s.Policy(t.Context(), "owner", "h1", "w")
	if err != nil {
		t.Fatal(err)
	}
	req := PresetRequest{MembershipID: before.ID, Revision: before.Revision, Resource: PresetResource{Kind: "project", ID: "p1"}, PresetID: "project-levels", Version: 1, Level: "view"}
	tx, err := s.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPresetSelectionIn(t.Context(), tx, "owner", "h1", "w", before.Membership, req); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if rows := selections(t, s); len(rows) != 0 {
		t.Fatalf("record escaped caller rollback:%+v", rows)
	}
	after, err := s.Policy(t.Context(), "owner", "h1", "w")
	if err != nil || after.Revision != before.Revision || len(after.GrantDetails) != len(before.GrantDetails) {
		t.Fatalf("record mutated canonical policy:%+v %v", after, err)
	}
	for i, g := range before.GrantDetails {
		if after.GrantDetails[i] != g {
			t.Fatalf("record rewrote canonical grant:%+v→%+v", g, after.GrantDetails[i])
		}
	}
}

func TestPresetRecordRechecksLiveActorInTransaction(t *testing.T) {
	for name, mutation := range map[string]string{"demoted": `UPDATE workspace_members SET role='MEMBER' WHERE id='mo'`, "suspended": `UPDATE users SET suspended_at='2026-10-17T00:00:00Z' WHERE id='owner'`} {
		t.Run(name, func(t *testing.T) {
			s := fixture(t)
			m := policy(t, s, "h1", Right{"project", "p1", "list"}, Right{"project", "p1", "read"})
			tx, err := s.DB.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := tx.ExecContext(t.Context(), mutation); err != nil {
				t.Fatal(err)
			}
			req := PresetRequest{MembershipID: m.ID, Revision: m.Revision, Resource: PresetResource{Kind: "project", ID: "p1"}, PresetID: "project-levels", Version: 1, Level: "view"}
			if err := s.RecordPresetSelectionIn(t.Context(), tx, "owner", "h1", "w", m, req); !errors.Is(err, ErrDenied) {
				t.Fatalf("cached actor authority accepted:%v", err)
			}
			var count int
			if err := tx.QueryRowContext(t.Context(), `SELECT count(*) FROM access_preset_selections`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("denied actor wrote intent:%d %v", count, err)
			}
		})
	}
}
