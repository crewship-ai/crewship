package backup

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The restore checks are the promise the Recovery page makes before anything
// is written: "this target works". RestoreBackup is what then runs. When the
// two disagree the page approves a restore that fails, or refuses one that
// would have worked. This table pins both to the same answer for every
// archive kind × target × name × server state (#2990).
func TestRestoreTargetContract(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDBCov(t)
	if _, err := db.ExecContext(ctx, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_lab', 'Lab', 'lab'), ('ws_live', 'Live', 'taken')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO workspaces (id, name, slug, deleted_at) VALUES ('ws_gone', 'Gone', 'gone', datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	slugs := DBSlugLookup(db)
	exists := func(s string) bool {
		st, _ := slugs(ctx, s)
		return st == SlugLive || s == "ws_lab" || s == "ws_live"
	}

	payload := emptyPayloadTarZst(t)
	mk := func(scope Scope, kind string) (*Manifest, string) {
		m := &Manifest{
			FormatVersion:     FormatVersion,
			Scope:             scope,
			Kind:              kind,
			CompatibleTargets: []Target{TargetAnyInstance},
			CreatedAt:         time.Now().UTC(),
			CreatedBy:         Actor{UserID: "u_cov"},
		}
		switch scope {
		case ScopeWorkspace:
			m.Contents.Workspace = &WorkspaceSummary{ID: "ws_lab", Slug: "lab"}
		case ScopeCrew:
			m.Contents.Workspace = &WorkspaceSummary{ID: "ws_lab", Slug: "lab"}
			m.Contents.Crews = []CrewSummary{{ID: "c_ops", Slug: "ops"}}
		case ScopeInstance:
			m.Contents.Instance = &InstanceContents{}
		}
		if kind == KindCustom {
			m.Categories = []string{"memory"}
		}
		return m, writeRawBundle(t, t.TempDir(), m, payload, WriteBundleOptions{NoEncrypt: true}, "")
	}
	ws, wsPath := mk(ScopeWorkspace, "full")
	custom, customPath := mk(ScopeWorkspace, KindCustom)
	crew, crewPath := mk(ScopeCrew, "full")
	inst, instPath := mk(ScopeInstance, "full")

	cases := []struct {
		name   string
		m      *Manifest
		path   string
		target string
		as     string
		want   bool
	}{
		{"workspace archive replaces its live workspace", ws, wsPath, TargetReplace, "", true},
		{"custom archive never replaces", custom, customPath, TargetReplace, "", false},
		{"crew archive never replaces a workspace", crew, crewPath, TargetReplace, "", false},
		{"instance archive never replaces online", inst, instPath, TargetReplace, "", false},
		{"workspace archive under a free name", ws, wsPath, TargetNewWorkspace, "lab-restored", true},
		{"custom archive under a free name", custom, customPath, TargetNewWorkspace, "lab-memory", true},
		{"new name held by a live workspace", ws, wsPath, TargetNewWorkspace, "taken", false},
		{"new name held by a deleted workspace", ws, wsPath, TargetNewWorkspace, "gone", false},
		{"new name that is not a slug", ws, wsPath, TargetNewWorkspace, "Lab Restored!", false},
		{"crew target from a workspace archive", ws, wsPath, TargetCrew, "ops-2", false},
		{"crew archive under a free name", crew, crewPath, TargetCrew, "ops-2", true},
		{"crew archive under a name a workspace holds", crew, crewPath, TargetCrew, "taken", false},
		{"crew archive under a deleted workspace's name", crew, crewPath, TargetCrew, "gone", false},
		{"crew archive as a new workspace", crew, crewPath, TargetNewWorkspace, "ops-ws", false},
		{"instance archive onto an empty server", inst, instPath, TargetEmptyServer, "", true},
		{"workspace archive onto an empty server", ws, wsPath, TargetEmptyServer, "", false},
		{"workspace archive as an isolated drill", ws, wsPath, TargetIsolated, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := RestoreCheckInput{Manifest: tc.m, Target: tc.target, WorkspaceExists: exists, Slugs: slugs}
			opts := RestoreOptions{Path: tc.path, Actor: covAdminActor(), DryRun: true}
			switch tc.target {
			case TargetReplace:
				opts.Replace = true
			case TargetNewWorkspace:
				in.AsWorkspace, opts.AsWorkspace = tc.as, tc.as
			case TargetCrew:
				in.AsCrew, opts.AsCrew = tc.as, tc.as
			}
			got := EvaluateRestoreChecks(in).Conflicts
			if got.OK != tc.want {
				t.Errorf("checks: ok=%v (%s), want ok=%v", got.OK, got.Detail, tc.want)
			}
			// Offline targets never reach RestoreBackup: crewship recover and
			// backup drill run them.
			if tc.target == TargetEmptyServer || tc.target == TargetIsolated {
				return
			}
			_, err := RestoreBackup(ctx, db, opts)
			refused := errors.Is(err, ErrInvalidScope) || errors.Is(err, ErrRestoreNameTaken)
			if refused == tc.want {
				t.Errorf("restore: refused=%v (%v), want allowed=%v", refused, err, tc.want)
			}
		})
	}
}

// A new_workspace or crew target needs the name before it can be judged.
func TestRestoreChecksWantTheNewName(t *testing.T) {
	ws := &Manifest{FormatVersion: FormatVersion, Scope: ScopeWorkspace, Contents: Contents{Workspace: &WorkspaceSummary{ID: "w", Slug: "lab"}}}
	got := EvaluateRestoreChecks(RestoreCheckInput{Manifest: ws, Target: TargetNewWorkspace})
	if got.Conflicts.OK {
		t.Fatalf("checks accepted a new workspace with no name: %+v", got.Conflicts)
	}
}
