package access

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestPresetPreviewEmptyBeforeSerializesAsArray(t *testing.T) {
	s := fixture(t)
	policy(t, s, "h1")
	p, err := planPreset(t, s, presetRequest(t, s, "project", "p1", "view"))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["before"]) != "[]" {
		t.Fatalf("empty before JSON=%s, want[]", fields["before"])
	}
	if p.Before == nil {
		t.Fatal("fingerprint source before is nil")
	}
	again, err := planPreset(t, s, presetRequest(t, s, "project", "p1", "view"))
	if err != nil || p.Digest != again.Digest {
		t.Fatalf("empty policy digest unstable:%v", err)
	}
}

func presetRequest(t *testing.T, s Store, kind, id, level string) PresetRequest {
	t.Helper()
	m, err := s.Membership(t.Context(), "h1", "w")
	if err != nil {
		t.Fatal(err)
	}
	return PresetRequest{m.ID, m.Revision, PresetResource{kind, id}, kind + "-levels", 1, level, []Right{}}
}
func planPreset(t *testing.T, s Store, req PresetRequest) (PresetPreview, error) {
	t.Helper()
	tx, err := s.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	return s.PlanPresetIn(t.Context(), tx, "owner", "h1", "w", req)
}
func previewWarning(p PresetPreview, code string) *PresetWarning {
	for _, w := range p.Warnings {
		if w.Code == code {
			return &w
		}
	}
	return nil
}

func TestPresetPreviewPreservesProvenanceAndNeverWrites(t *testing.T) {
	for _, level := range []string{"view", "contribute", "manage"} {
		t.Run(level, func(t *testing.T) {
			s := fixture(t)
			policy(t, s, "h1", Right{"project", "p1", "read"}, Right{"project", "p1", "delete_issues"}, Right{"project", "p1", "write"}, Right{"project", "p2", "read"})
			for _, q := range []string{
				`INSERT INTO users(id,email) VALUES('issuer','issuer@preview.test')`,
				`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('issuer-member','w','issuer','VIEWER')`,
				`UPDATE access_grants SET created_by='issuer',created_at='original' WHERE member_id='m1' AND operation='read' AND project_id='p1'`,
			} {
				if _, err := s.DB.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			insertPreset(t, s, "m1", "project", "p1", "owner")
			before, err := s.Policy(t.Context(), "owner", "h1", "w")
			if err != nil {
				t.Fatal(err)
			}
			var journals int
			if err := s.DB.QueryRow(`SELECT COUNT(*) FROM journal_entries`).Scan(&journals); err != nil {
				t.Fatal(err)
			}
			request := presetRequest(t, s, "project", "p1", level)
			preview, err := planPreset(t, s, request)
			if err != nil {
				t.Fatal(err)
			}
			if preview.CurrentSelection == nil || preview.CurrentSelection.State != "custom" {
				t.Fatal("saved intent was inferred from mismatching bits")
			}
			if preview.Digest == "" || previewWarning(preview, "FILE_ROLE_FLOOR") == nil {
				t.Fatalf("missing digest/floor: %+v", preview)
			}
			if len(preview.OrthogonalRights) != 3 {
				t.Fatalf("lost unrelated/destructive/file rights: %+v", preview)
			}
			for _, c := range preview.Changes {
				if c.Right.Operation == "read" && (c.Action != "retain" || c.IssuerID != "issuer" || c.CreatedAt != "original" || c.Live) {
					t.Fatalf("inert retained issuer rewritten: %+v", c)
				}
			}
			warning := previewWarning(preview, "RETAINED_DELETE_ISSUES")
			if warning == nil || *warning.AfterEligible {
				t.Fatal("inert Read fabricated destructive authority")
			}
			again, err := planPreset(t, s, request)
			if err != nil || again.Digest != preview.Digest {
				t.Fatalf("unstable snapshot: %v", err)
			}
			request.Reissue = []Right{{"project", "p1", "read"}}
			renew, err := planPreset(t, s, request)
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range renew.Changes {
				if c.Right.Operation == "read" && (c.Action != "reissue" || c.IssuerID != "owner" || c.CreatedAt != "" || !c.Live) {
					t.Fatalf("reissue missing explicit actor/no-future-time: %+v", c)
				}
			}
			if level == "manage" && !*previewWarning(renew, "RETAINED_DELETE_ISSUES").AfterEligible {
				t.Fatal("Read reissue/Manage eligibility omitted")
			}
			if renew.Digest == preview.Digest {
				t.Fatal("explicit reissue not bound")
			}
			after, err := s.Policy(t.Context(), "owner", "h1", "w")
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("preview wrote policy: %v", err)
			}
			var count, choices int
			if err := s.DB.QueryRow(`SELECT COUNT(*) FROM journal_entries`).Scan(&count); err != nil || count != journals {
				t.Fatal("preview wrote audit")
			}
			if err := s.DB.QueryRow(`SELECT COUNT(*) FROM access_preset_selections`).Scan(&choices); err != nil || choices != 1 {
				t.Fatal("preview wrote selection")
			}
		})
	}
}

func TestPresetPreviewDigestFencesSnapshotAndCurrentAuthority(t *testing.T) {
	for name, query := range map[string]string{
		"grant":             `UPDATE access_grants SET created_at='later' WHERE member_id='m1'`,
		"role":              `UPDATE workspace_members SET role='VIEWER' WHERE id='m1'`,
		"issuer":            `UPDATE workspace_members SET role='VIEWER' WHERE id='issuer-member'`,
		"resource":          `UPDATE projects SET updated_at='later' WHERE id='p1'`,
		"target-suspension": `UPDATE users SET suspended_at='later' WHERE id='h1'`,
		"actor-suspension":  `UPDATE users SET suspended_at='later' WHERE id='owner'`,
		"incarnation":       `DELETE FROM workspace_members WHERE id='m1';INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('rejoined','w','h1','MEMBER','restricted')`,
	} {
		t.Run(name, func(t *testing.T) {
			s := fixture(t)
			policy(t, s, "h1", Right{"project", "p1", "read"})
			for _, q := range []string{`INSERT INTO users(id,email) VALUES('issuer','issuer@preview.test')`, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('issuer-member','w','issuer','ADMIN')`, `UPDATE access_grants SET created_by='issuer' WHERE member_id='m1'`} {
				if _, err := s.DB.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			req := presetRequest(t, s, "project", "p1", "view")
			before, err := planPreset(t, s, req)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB.Exec(query); err != nil {
				t.Fatal(err)
			}
			after, err := planPreset(t, s, req)
			if err == nil && before.Digest == after.Digest {
				t.Fatal("changed authority/snapshot retained consent")
			}
		})
	}
	s := fixture(t)
	policy(t, s, "h1", Right{"project", "p1", "read"})
	req := presetRequest(t, s, "project", "p1", "view")
	before, err := planPreset(t, s, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`UPDATE users SET full_name='Display only' WHERE id='owner'`); err != nil {
		t.Fatal(err)
	}
	after, err := planPreset(t, s, req)
	if err != nil || before.Digest != after.Digest {
		t.Fatalf("display names changed authority digest: %v", err)
	}
	if _, err := s.PlanPresetIn(t.Context(), nil, "owner", "h1", "w", req); !errors.Is(err, ErrDenied) {
		t.Fatal("nil snapshot accepted")
	}
}

func TestPresetPreviewValidationAndRoutineWarnings(t *testing.T) {
	s := fixture(t)
	if _, err := s.DB.Exec(`INSERT INTO pipelines(id,workspace_id,name,slug,definition_json,definition_hash,status) VALUES('r1','w','R1','r1','{}','hash','active')`); err != nil {
		t.Fatal(err)
	}
	policy(t, s, "h1", Right{"routine", "r1", "results"})
	for _, level := range []string{"view", "run", "edit"} {
		p, err := planPreset(t, s, presetRequest(t, s, "routine", "r1", level))
		if err != nil {
			t.Fatal(err)
		}
		if p.CurrentSelection != nil || previewWarning(p, "ROUTINE_RESULTS_HISTORY") == nil || (level != "view" && previewWarning(p, "EXECUTION_GATES") == nil) || (level == "edit" && previewWarning(p, "DRAFT_ONLY") == nil) {
			t.Fatalf("missing independent warnings: %+v", p)
		}
		if len(p.AfterRights) != map[string]int{"view": 2, "run": 3, "edit": 4}[level] {
			t.Fatal("wrong immutable bundle")
		}
	}
	base := presetRequest(t, s, "routine", "r1", "view")
	for name, alter := range map[string]func(*PresetRequest){
		"version": func(r *PresetRequest) { r.Version = 2 }, "level": func(r *PresetRequest) { r.Level = "publish" }, "kind": func(r *PresetRequest) { r.Resource.Kind = "agent" },
		"new-reissue": func(r *PresetRequest) { r.Reissue = []Right{{"routine", "r1", "view"}} },
		"duplicate": func(r *PresetRequest) {
			r.Reissue = []Right{{"routine", "r1", "results"}, {"routine", "r1", "results"}}
		},
		"orthogonal": func(r *PresetRequest) { r.Reissue = []Right{{"project", "p1", "delete_issues"}} },
	} {
		t.Run(name, func(t *testing.T) {
			req := base
			alter(&req)
			if _, err := planPreset(t, s, req); !errors.Is(err, ErrPresetInvalid) {
				t.Fatalf("invalid accepted: %v", err)
			}
		})
	}
	foreign := base
	foreign.Resource = PresetResource{"project", "foreign"}
	foreign.PresetID = "project-levels"
	if _, err := planPreset(t, s, foreign); !errors.Is(err, ErrDenied) {
		t.Fatalf("foreign accepted: %v", err)
	}
	if _, err := s.DB.Exec(`UPDATE workspace_members SET access_mode='trusted' WHERE id='m1'`); err != nil {
		t.Fatal(err)
	}
	base = presetRequest(t, s, "routine", "r1", "view")
	if _, err := planPreset(t, s, base); !errors.Is(err, ErrPresetInvalid) {
		t.Fatalf("trusted mode converted: %v", err)
	}
}

func TestPresetPreviewMaxRightsAndDestructiveDowngrade(t *testing.T) {
	s := fixture(t)
	policy(t, s, "h1", Right{"project", "p1", "read"}, Right{"project", "p1", "manage"}, Right{"project", "p1", "delete_issues"})
	p, err := planPreset(t, s, presetRequest(t, s, "project", "p1", "view"))
	if err != nil {
		t.Fatal(err)
	}
	w := previewWarning(p, "RETAINED_DELETE_ISSUES")
	if w == nil || !*w.BeforeEligible || *w.AfterEligible {
		t.Fatal("destructive downgrade not shown")
	}
	// Fill only unrelated resources through canonical SQL, without changing
	// the requested resource or relying on an impossible request payload.
	if _, err := s.DB.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<254) INSERT INTO projects(id,workspace_id,name,slug) SELECT 'extra-'||x,'w','Extra','extra-'||x FROM n`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO access_grants(id,member_id,resource_kind,project_id,operation,created_by,created_at) SELECT id,'m1','project',id,'read','owner','original' FROM projects WHERE id LIKE 'extra-%'`); err != nil {
		t.Fatal(err)
	}
	if _, err := planPreset(t, s, presetRequest(t, s, "project", "p1", "manage")); !errors.Is(err, ErrPresetInvalid) {
		t.Fatalf("over256 accepted: %v", err)
	}
}
