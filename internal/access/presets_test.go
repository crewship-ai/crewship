package access

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
)

func TestPresetCatalogV1IsImmutableAndExplicit(t *testing.T) {
	for id, hash := range map[string]string{"routine-levels": "b3eceaba248f07636c1a0cfa7e0cf72fd14de63093d1d978a4d6fb2f35b6d993", "project-levels": "1995963881853678aabd181b35e10b1fb6c0477be502aa1dd810e78e2d63a264"} {
		d, ok := ResolvePreset(id, 1)
		if !ok || d.Hash() != hash {
			t.Fatalf("immutable v1 definition changed: %s %+v", id, d)
		}
		d.ManagedOperations[0], d.Levels[0].Operations[0] = "publish", "delete_issues"
		fresh, _ := ResolvePreset(id, 1)
		if fresh.Hash() != hash {
			t.Fatal("consumer mutated canonical preset")
		}
		if _, ok := fresh.Operations("publish"); ok {
			t.Fatal("v1 includes publication")
		}
	}
	if _, ok := ResolvePreset("routine-levels", 2); ok {
		t.Fatal("unknown version silently resolved")
	}
}

func insertPreset(t *testing.T, s Store, member, kind, resource, selector string) {
	t.Helper()
	d, _ := ResolvePreset(kind+"-levels", 1)
	managed, _ := json.Marshal(d.ManagedOperations)
	var project, routine any
	if kind == "project" {
		project = resource
	} else {
		routine = resource
	}
	_, err := s.DB.Exec(`INSERT INTO access_preset_selections(id,workspace_id,member_id,resource_kind,project_id,routine_id,preset_id,preset_version,level,definition_hash,managed_operations,selected_by,selected_at) VALUES(?, 'w', ?, ?, ?, ?, ?,1,'view',?,?,?,'2026-10-16T00:00:00Z')`, member+resource, member, kind, project, routine, d.ID, d.Hash(), string(managed), selector)
	if err != nil {
		t.Fatal(err)
	}
}
func selections(t *testing.T, s Store) []PresetSelection {
	t.Helper()
	tx, err := s.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	rows, err := s.PresetSelectionsIn(t.Context(), tx, "owner", "h1", "w")
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestPresetLabelsTrackOnlyManagedGrantChanges(t *testing.T) {
	for name, query := range map[string]string{
		"unrelated":  `INSERT INTO access_grants(id,member_id,resource_kind,project_id,operation,created_by,created_at) VALUES('extra','m1','project','p2','read','owner','now')`,
		"orthogonal": `INSERT INTO access_grants(id,member_id,resource_kind,project_id,operation,created_by,created_at) VALUES('extra','m1','project','p1','delete_issues','owner','now')`,
		"no-op":      `UPDATE access_grants SET created_at=created_at WHERE member_id='m1'`,
		"add":        `INSERT INTO access_grants(id,member_id,resource_kind,project_id,operation,created_by,created_at) VALUES('extra','m1','project','p1','contribute','owner','now')`,
		"remove":     `DELETE FROM access_grants WHERE member_id='m1' AND operation='read'`,
		"reissue":    `UPDATE access_grants SET created_at='later' WHERE member_id='m1' AND operation='read'`,
	} {
		t.Run(name, func(t *testing.T) {
			s := fixture(t)
			policy(t, s, "h1", Right{"project", "p1", "list"}, Right{"project", "p1", "read"})
			insertPreset(t, s, "m1", "project", "p1", "owner")
			if _, err := s.DB.Exec(query); err != nil {
				t.Fatal(err)
			}
			want := 0
			if name == "unrelated" || name == "orthogonal" || name == "no-op" {
				want = 1
			}
			if got := selections(t, s); len(got) != want || want == 1 && got[0].State != "selected" {
				t.Fatalf("selection invalidation: %+v want %d", got, want)
			}
		})
	}
}

func TestPresetIntentSurvivesInactiveIssuerAndHistoricalSelector(t *testing.T) {
	s := fixture(t)
	policy(t, s, "h1", Right{"project", "p1", "list"}, Right{"project", "p1", "read"})
	for _, query := range []string{
		`INSERT INTO users(id,email) VALUES('issuer','issuer@preset.test'),('selector','selector@preset.test')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('mi','w','issuer','ADMIN')`,
		`UPDATE access_grants SET created_by='issuer',created_at='original' WHERE member_id='m1' AND operation='read'`,
	} {
		if _, err := s.DB.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	insertPreset(t, s, "m1", "project", "p1", "selector")
	if _, err := s.DB.Exec(`UPDATE workspace_members SET role='VIEWER' WHERE id='mi'`); err != nil {
		t.Fatal(err)
	}
	if rows := selections(t, s); len(rows) != 1 || rows[0].State != "selected" {
		t.Fatalf("inert intent lost: %+v", rows)
	}
	if err := s.Check(t.Context(), "h1", "w", Right{"project", "p1", "read"}); !errors.Is(err, ErrIssuerDenied) {
		t.Fatalf("intent granted authority: %v", err)
	}
	var issuer, created string
	if err := s.DB.QueryRow(`SELECT created_by,created_at FROM access_grants WHERE member_id='m1' AND operation='read'`).Scan(&issuer, &created); err != nil || issuer != "issuer" || created != "original" {
		t.Fatalf("provenance rewritten: %s %s %v", issuer, created, err)
	}
	if _, err := s.DB.Exec(`DELETE FROM users WHERE id='selector'`); err != nil {
		t.Fatal(err)
	}
	if rows := selections(t, s); len(rows) != 1 || rows[0].SelectedBy != nil {
		t.Fatalf("nullable historical actor: %+v", rows)
	}
	if _, err := s.DB.Exec(`UPDATE access_preset_selections SET definition_hash='unknown'`); err != nil {
		t.Fatal(err)
	}
	if rows := selections(t, s); rows[0].State != "custom" {
		t.Fatal("unsupported metadata treated as selected")
	}
}

func TestPresetBindingAndUpgradePreserveCanonicalGrants(t *testing.T) {
	s := fixture(t)
	policy(t, s, "h1", Right{"project", "p1", "read"})
	before, err := s.Policy(t.Context(), "owner", "h1", "w")
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{`DROP TRIGGER access_preset_grant_insert`, `DROP TRIGGER access_preset_grant_delete`, `DROP TRIGGER access_preset_grant_update`, `DROP TABLE access_preset_selections`} {
		if _, err := s.DB.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	migration, err := os.ReadFile("../database/migrations/20261017074413_access_preset_selections.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	after, err := s.Policy(t.Context(), "owner", "h1", "w")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("upgrade changed policy: %v", err)
	}
	insertPreset(t, s, "m1", "project", "p1", "owner")
	if rows := selections(t, s); rows[0].State != "custom" {
		t.Fatal("mismatched bits fabricated a selected level")
	}
	for _, query := range []string{`UPDATE access_preset_selections SET project_id='foreign'`, `UPDATE access_preset_selections SET workspace_id='other'`, `UPDATE access_preset_selections SET resource_kind='routine'`} {
		if _, err := s.DB.Exec(query); err == nil {
			t.Fatalf("invalid binding accepted: %s", query)
		}
	}
	if _, err := s.DB.Exec(`DELETE FROM workspace_members WHERE id='m1'`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM access_preset_selections`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("departed member selection retained: %d %v", count, err)
	}
}

func TestRoutinePresetInvalidatesBothChangedGrantTargets(t *testing.T) {
	s := fixture(t)
	if _, err := s.DB.Exec(`INSERT INTO pipelines(id,workspace_id,name,slug,definition_json,definition_hash,status) VALUES('r1','w','R1','r1','{}','hash','active'),('r2','w','R2','r2','{}','hash','active')`); err != nil {
		t.Fatal(err)
	}
	policy(t, s, "h1", Right{"routine", "r1", "view"}, Right{"routine", "r1", "results"})
	policy(t, s, "h2", Right{"routine", "r2", "view"}, Right{"routine", "r2", "results"})
	insertPreset(t, s, "m1", "routine", "r1", "owner")
	insertPreset(t, s, "m2", "routine", "r2", "owner")
	if rows := selections(t, s); len(rows) != 1 || rows[0].State != "selected" {
		t.Fatalf("routine View intent missing: %+v", rows)
	}
	// Future publication stays orthogonal to immutable v1; it confers no current operation.
	if _, err := s.DB.Exec(`INSERT INTO access_grants(id,member_id,resource_kind,routine_id,operation,created_by,created_at) VALUES('future','m1','routine','r1','publish','owner','now')`); err != nil {
		t.Fatal(err)
	}
	if rows := selections(t, s); len(rows) != 1 {
		t.Fatal("orthogonal publication invalidated v1")
	}
	if _, err := s.DB.Exec(`UPDATE access_grants SET member_id='m2',routine_id='r2',operation='run' WHERE member_id='m1' AND operation='view'`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM access_preset_selections`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("old/new changed targets retained: %d %v", count, err)
	}
	tx, err := s.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := s.PresetSelectionsIn(t.Context(), tx, "h2", "h1", "w"); !errors.Is(err, ErrDenied) {
		t.Fatalf("non-admin read selection provenance: %v", err)
	}
}
