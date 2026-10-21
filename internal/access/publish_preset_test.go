package access

import (
	"errors"
	"reflect"
	"testing"
)

func TestRoutineEditPublishPresetV2DoesNotImplyViewOrRun(t *testing.T) {
	d, ok := ResolvePreset("routine-levels", 2)
	ops, levelOK := d.Operations("edit_publish")
	if !ok || !levelOK || d.Hash() != "7691f35f09683b7412a79fd3e34da86ae87c488f053f5d63b5b12ca54116eb8f" || !reflect.DeepEqual(ops, []string{"edit", "publish"}) || !reflect.DeepEqual(d.ManagedOperations, ops) {
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
