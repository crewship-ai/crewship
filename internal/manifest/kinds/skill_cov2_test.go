package kinds

// Second coverage pass for skill.go (skill_cov_test.go already exists).
// Pins the Plan import-body failure, the Update upsert exec, and the
// ExportSkills list failure.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

func TestSkillCov2_Plan_BuildImportBodyFails(t *testing.T) {
	t.Parallel()

	// No Spec.Source and no resolved content → buildImportBody's
	// default branch errors and Plan must wrap it.
	doc := &SkillDocument{Metadata: internalapi.Metadata{Name: "Helper", Slug: "helper"}}
	_, err := doc.Plan(context.Background(), newCovClient(nil), nil)
	if err == nil || !strings.Contains(err.Error(), "build import body") {
		t.Fatalf("got %v", err)
	}
}

func TestSkillCov2_Plan_UpdateUpsert(t *testing.T) {
	t.Parallel()

	doc := &SkillDocument{
		Metadata: internalapi.Metadata{Name: "Helper", Slug: "helper"},
		Spec:     SkillSpec{Source: "https://example.com/skill.md"},
	}
	remote := &SkillRemote{Slug: "helper", Source: "IMPORTED"}

	t.Run("update posts to import endpoint", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{
			"POST /api/v1/workspaces/ws_cov/skills/import": {status: 200, body: "{}"},
		})
		items, err := doc.Plan(context.Background(), c, remote)
		if err != nil || len(items) != 1 || items[0].Action != internalapi.ActionUpdate {
			t.Fatalf("plan: items=%v err=%v", items, err)
		}
		if execErr := items[0].Exec(context.Background(), c); execErr != nil {
			t.Fatalf("exec: %v", execErr)
		}
		if !c.sawCall("POST /api/v1/workspaces/ws_cov/skills/import") {
			t.Fatalf("calls = %v", c.calls)
		}
	})
	t.Run("update exec transport error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{
			"POST /api/v1/workspaces/ws_cov/skills/import": {err: errors.New("down")},
		})
		items, err := doc.Plan(context.Background(), c, remote)
		if err != nil {
			t.Fatalf("plan: %v", err)
		}
		if execErr := items[0].Exec(context.Background(), c); execErr == nil || !strings.Contains(execErr.Error(), "POST /api/v1/workspaces/ws_cov/skills/import") {
			t.Fatalf("exec: got %v", execErr)
		}
	})
}

func TestSkillCov2_ExportSkills_ListFails(t *testing.T) {
	t.Parallel()

	c := newCovClient(map[string]covRoute{
		"GET /api/v1/skills": {status: 500, body: "boom"},
	})
	_, err := ExportSkills(context.Background(), c)
	if err == nil || !strings.Contains(err.Error(), "export skills") {
		t.Fatalf("got %v", err)
	}
}
