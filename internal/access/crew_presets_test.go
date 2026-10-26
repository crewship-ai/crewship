package access

import (
	"database/sql"
	"reflect"
	"slices"
	"testing"
)

func TestCrewPresetKeepsLegacyMetadataReaderSafe(t *testing.T) {
	s := fixture(t)
	member := policy(t, s, "h1", Right{"crew", "crew", "view"}, Right{"project", "p1", "list"}, Right{"project", "p1", "read"})
	insertPreset(t, s, "m1", "project", "p1", "owner")
	req := presetRequest(t, s, "crew", "crew", "view")
	tx, err := s.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordPresetSelectionIn(t.Context(), tx, "owner", "h1", "w", member, req); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// Exact baseline reader shape: an old binary must still read its known
	// project/routine choices after this version has stored a crew choice.
	rows, err := s.DB.Query(`SELECT id,resource_kind,COALESCE(project_id,routine_id),preset_id,preset_version,level,definition_hash,managed_operations,selected_by,selected_at FROM access_preset_selections WHERE workspace_id=? AND member_id=? ORDER BY resource_kind,COALESCE(project_id,routine_id)`, "w", "m1")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for rows.Next() {
		var id, kind, resource, preset, level, hash, managed, at string
		var version int
		var selector sql.NullString
		if err := rows.Scan(&id, &kind, &resource, &preset, &version, &level, &hash, &managed, &selector, &at); err != nil {
			rows.Close()
			t.Fatalf("legacy policy reader broken: %v", err)
		}
		if id != "m1p1" || kind != "project" || resource != "p1" || at != "2026-10-16T00:00:00Z" {
			rows.Close()
			t.Fatal("old choice changed")
		}
		count++
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	if count != 1 {
		t.Fatalf("legacy choices: %d", count)
	}
	p, err := s.Policy(t.Context(), "owner", "h1", "w")
	if err != nil {
		t.Fatal(err)
	}
	if len(*p.PresetSelections) != 2 {
		t.Fatalf("unified current choices: %+v", p.PresetSelections)
	}
}

func TestCrewPresetV1IsImmutableAndWithinGuestCeiling(t *testing.T) {
	d, ok := ResolvePreset("crew-levels", 1)
	const hash = "a9a300aa0eb9764a245650bee7348b6f21c1781a9b55d0fef024aeb02da7eb85"
	if !ok || d.Hash() != hash {
		t.Fatalf("immutable crew v1 changed: %+v", d)
	}
	for _, level := range []string{"manage", "delegate", "publish"} {
		if _, ok := d.Operations(level); ok {
			t.Fatalf("Guest preset includes %s", level)
		}
	}
	d.ManagedOperations[0] = "manage"
	d.Levels[1].Operations[0] = "delegate"
	fresh, _ := ResolvePreset("crew-levels", 1)
	if fresh.Hash() != hash {
		t.Fatal("consumer mutated canonical crew preset")
	}
}

func TestCrewPresetViewUsePreservesIndependentGrants(t *testing.T) {
	s := fixture(t)
	independent := Right{"agent", "a", "delegate"}
	policy(t, s, "h1", independent)
	for _, level := range []string{"use", "view", "none"} {
		req := presetRequest(t, s, "crew", "crew", level)
		preview, err := planPreset(t, s, req)
		if err != nil {
			t.Fatalf("crew %s preview: %v", level, err)
		}
		if previewWarning(preview, "CREW_DYNAMIC_AUDIENCE") == nil {
			t.Fatal("preview omitted consent to the dynamic crew audience")
		}
		tx, err := s.DB.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		member, err := s.ReplacePresetIn(t.Context(), tx, "owner", "h1", "w", req)
		if err == nil {
			err = s.RecordPresetSelectionIn(t.Context(), tx, "owner", "h1", "w", member, req)
		}
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		p, err := s.Policy(t.Context(), "owner", "h1", "w")
		if err != nil {
			t.Fatal(err)
		}
		want := []Right{independent}
		if level != "none" {
			want = append(want, Right{"crew", "crew", "view"})
		}
		if level == "use" {
			want = append(want, Right{"crew", "crew", "use"})
		}
		slices.SortFunc(want, func(a, b Right) int {
			if a.Kind < b.Kind {
				return -1
			}
			if a.Kind > b.Kind {
				return 1
			}
			if a.Operation < b.Operation {
				return -1
			}
			if a.Operation > b.Operation {
				return 1
			}
			return 0
		})
		if !reflect.DeepEqual(p.Rights, want) {
			t.Fatalf("%s: %+v != %+v", level, p.Rights, want)
		}
		choices := *p.PresetSelections
		if len(choices) != 1 || choices[0].Kind != "crew" || choices[0].ResourceID != "crew" || choices[0].Level != level || choices[0].State != "selected" {
			t.Fatalf("explicit choice: %+v", choices)
		}
		for _, op := range []string{"discover", "chat", "run"} {
			allowed := s.Check(t.Context(), "h1", "w", Right{"agent", "b", op}) == nil
			if allowed != (level == "use" || (level == "view" && op == "discover")) {
				t.Fatalf("%s inherited %s: %v", level, op, allowed)
			}
		}
		if err := s.Check(t.Context(), "h1", "w", Right{"agent", "b", "delegate"}); err == nil {
			t.Fatal("crew preset granted Delegate")
		}
	}
}

func TestCrewPresetMetadataBindingAndInvalidation(t *testing.T) {
	for name, mutation := range map[string]string{
		"orthogonal":  `INSERT INTO access_grants(id,member_id,resource_kind,agent_id,operation,created_by,created_at) VALUES('other','m1','agent','a','discover','owner','now')`,
		"issuer-loss": `UPDATE workspace_members SET role='MEMBER' WHERE id='mo'`,
		"remove":      `DELETE FROM access_grants WHERE member_id='m1' AND crew_id='crew'`,
		"reissue":     `UPDATE access_grants SET created_at='renewed' WHERE member_id='m1' AND crew_id='crew'`,
	} {
		t.Run(name, func(t *testing.T) {
			s := fixture(t)
			member := policy(t, s, "h1", Right{"crew", "crew", "view"})
			req := presetRequest(t, s, "crew", "crew", "view")
			tx, err := s.DB.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.RecordPresetSelectionIn(t.Context(), tx, "owner", "h1", "w", member, req); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			for _, bad := range []string{
				`UPDATE crew_access_preset_selections SET workspace_id='other'`,
				`UPDATE crew_access_preset_selections SET crew_id=NULL`,
				`UPDATE crew_access_preset_selections SET resource_kind='project'`,
				`UPDATE crew_access_preset_selections SET crew_id='missing'`,
				`INSERT INTO crew_access_preset_selections SELECT * FROM crew_access_preset_selections`,
			} {
				if _, err := s.DB.Exec(bad); err == nil {
					t.Fatalf("invalid choice accepted: %s", bad)
				}
			}
			if _, err := s.DB.Exec(mutation); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := s.DB.QueryRow(`SELECT count(*) FROM crew_access_preset_selections`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			want := 0
			if name == "orthogonal" || name == "issuer-loss" {
				want = 1
			}
			if count != want {
				t.Fatalf("choice rows %d != %d", count, want)
			}
			if name == "issuer-loss" && s.Check(t.Context(), "h1", "w", Right{"crew", "crew", "view"}) == nil {
				t.Fatal("saved intent restored issuer authority")
			}
		})
	}
}
