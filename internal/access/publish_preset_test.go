package access

import (
	"errors"
	"reflect"
	"testing"
)

func TestRoutineEditPublishPresetV2DoesNotImplyViewOrRun(t *testing.T) {
	d, ok := ResolvePreset("routine-levels", 2)
	ops, levelOK := d.Operations("edit_publish")
	none, noneOK := d.Operations("none")
	if !ok || !levelOK || !noneOK || len(none) != 0 || d.Hash() != "ef75be8f428158725b6ee75d7781b76cdae23f00e3b166627b00cecba91d3ffe" || !reflect.DeepEqual(ops, []string{"edit", "publish"}) || !reflect.DeepEqual(d.ManagedOperations, ops) {
		t.Fatalf("missing explicit Edit + Publish v2: %+v", d)
	}
	for _, keep := range []bool{false, true} {
		t.Run(map[bool]string{false: "no_implicit_shares", true: "preserve_explicit_shares"}[keep], func(t *testing.T) {
			s := fixture(t)
			publicationExec(t, s, `INSERT INTO pipelines(id,workspace_id,name,slug,definition_json,definition_hash,status) VALUES('publication','w','Publication','publication','{}','hash','active')`)
			rights := []Right{}
			if keep {
				rights = append(rights, Right{"routine", "publication", "view"}, Right{"routine", "publication", "run"})
			}
			policy(t, s, "h1", rights...)
			req := presetRequest(t, s, "routine", "publication", "edit_publish")
			req.Version = 2
			preview, err := planPreset(t, s, req)
			if err != nil || previewWarning(preview, "PUBLISH_GATES") == nil || len(preview.OrthogonalRights) != len(rights) {
				t.Fatalf("missing explicit publication consequences: %+v %v", preview, err)
			}
			if len(preview.AfterRights) != len(rights)+2 {
				t.Fatal("preset implied another operation", preview.AfterRights)
			}
			policy(t, s, "h1", preview.AfterRights...)
			for _, op := range []string{"view", "run"} {
				err := s.Check(t.Context(), "h1", "w", Right{"routine", "publication", op})
				if (err == nil) != keep || !keep && !errors.Is(err, ErrDenied) {
					t.Fatalf("explicit %s authority changed: %v", op, err)
				}
			}
		})
	}
}

// applyPreset mirrors the apply handler's writer: plan, replace, record, commit.
func applyPreset(t *testing.T, s Store, version int, level string) {
	t.Helper()
	req := presetRequest(t, s, "routine", "publication", level)
	req.Version = version
	tx, err := s.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	after, err := s.ReplacePresetIn(t.Context(), tx, "owner", "h1", "w", req)
	if err != nil {
		t.Fatalf("apply v%d %s: %v", version, level, err)
	}
	if err = s.RecordPresetSelectionIn(t.Context(), tx, "owner", "h1", "w", after, req); err != nil {
		t.Fatalf("record v%d %s: %v", version, level, err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestRoutinePresetVersionsKeepTruthfulRecordsAndRevokePublish(t *testing.T) {
	s := fixture(t)
	publicationExec(t, s, `INSERT INTO pipelines(id,workspace_id,name,slug,definition_json,definition_hash,status) VALUES('publication','w','Publication','publication','{}','hash','active')`)
	policy(t, s, "h1")
	records := func() map[int]string {
		got := map[int]string{}
		for _, row := range selections(t, s) {
			if row.Kind == "routine" && row.ResourceID == "publication" {
				got[row.Version] = row.Level + ":" + row.State
			}
		}
		return got
	}
	allowed := func(op string) bool {
		return s.Check(t.Context(), "h1", "w", Right{"routine", "publication", op}) == nil
	}
	steps := []struct {
		version int
		level   string
		want    map[int]string
		ops     map[string]bool
	}{
		{1, "edit", map[int]string{1: "edit:selected"}, map[string]bool{"view": true, "edit": true, "publish": false}},
		// v2 records beside v1: the v1 Edit choice remains true and visible.
		{2, "edit_publish", map[int]string{1: "edit:selected", 2: "edit_publish:selected"}, map[string]bool{"view": true, "edit": true, "publish": true}},
		// Revoking only Publish keeps Edit and the v1 record intact.
		{2, "edit", map[int]string{1: "edit:selected", 2: "edit:selected"}, map[string]bool{"view": true, "edit": true, "publish": false}},
		{2, "edit_publish", map[int]string{1: "edit:selected", 2: "edit_publish:selected"}, map[string]bool{"edit": true, "publish": true}},
		// Removing Edit invalidates the v1 record instead of leaving it false.
		{2, "none", map[int]string{2: "none:selected"}, map[string]bool{"view": true, "results": true, "run": true, "edit": false, "publish": false}},
	}
	for _, step := range steps {
		applyPreset(t, s, step.version, step.level)
		if got := records(); !reflect.DeepEqual(got, step.want) {
			t.Fatalf("after v%d %s records %v, want %v", step.version, step.level, got, step.want)
		}
		for op, want := range step.ops {
			if allowed(op) != want {
				t.Fatalf("after v%d %s: %s allowed=%v", step.version, step.level, op, !want)
			}
		}
	}
}

func TestRoutinePresetPreviewShowsPublishRevocationAndRefusesOrphan(t *testing.T) {
	s := fixture(t)
	publicationExec(t, s, `INSERT INTO pipelines(id,workspace_id,name,slug,definition_json,definition_hash,status) VALUES('publication','w','Publication','publication','{}','hash','active')`)
	policy(t, s, "h1", Right{"routine", "publication", "view"}, Right{"routine", "publication", "edit"}, Right{"routine", "publication", "publish"})
	for _, level := range []string{"none", "edit"} {
		req := presetRequest(t, s, "routine", "publication", level)
		req.Version = 2
		preview, err := planPreset(t, s, req)
		if err != nil || previewWarning(preview, "PUBLISH_REVOKED") == nil {
			t.Fatalf("%s preview hides Publish removal: %+v %v", level, preview, err)
		}
		removed := false
		for _, change := range preview.Changes {
			removed = removed || change.Right.Operation == "publish" && change.Action == "remove"
		}
		if !removed || len(preview.OrthogonalRights) != 1 {
			t.Fatalf("%s preview changes: %+v", level, preview.Changes)
		}
	}
	// A v1 level without Edit cannot leave Publish orphaned; revoke it first.
	if _, err := planPreset(t, s, presetRequest(t, s, "routine", "publication", "run")); !errors.Is(err, ErrPublishRequiresEdit) {
		t.Fatalf("v1 Run after Publish: %v", err)
	}
}
