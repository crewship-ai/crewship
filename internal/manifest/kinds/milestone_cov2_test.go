package kinds

// Second coverage pass for milestone.go (milestone_cov_test.go already
// exists). Pins the project-resolution transport failure and the Plan
// exec closures' error branches.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

func milestoneCov2Doc() *MilestoneDocument {
	return &MilestoneDocument{
		APIVersion: "crewship/v1",
		Kind:       "Milestone",
		Metadata:   internalapi.Metadata{Name: "Beta", Slug: "beta"},
		Spec:       MilestoneSpec{ProjectSlug: "roadmap"},
	}
}

func TestMilestoneCov2_ResolveProjectID_ListError(t *testing.T) {
	t.Parallel()

	c := newCovClient(map[string]covRoute{"GET /api/v1/projects": {err: errors.New("down")}})
	_, err := milestoneResolveProjectIDBySlug(context.Background(), c, "roadmap")
	if err == nil || !strings.Contains(err.Error(), "GET /api/v1/projects") {
		t.Fatalf("got %v", err)
	}
}

func TestMilestoneCov2_Plan_ExecErrors(t *testing.T) {
	t.Parallel()

	projects := covRoute{body: `[{"id":"p1","slug":"roadmap"}]`}

	t.Run("create post transport error", func(t *testing.T) {
		t.Parallel()
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/projects":                projects,
			"POST /api/v1/projects/p1/milestones": {err: errors.New("down")},
		})
		items, err := milestoneCov2Doc().Plan(context.Background(), c, nil)
		if err != nil || len(items) != 1 || items[0].Action != internalapi.ActionCreate {
			t.Fatalf("plan: items=%v err=%v", items, err)
		}
		if execErr := items[0].Exec(context.Background(), c); execErr == nil || !strings.Contains(execErr.Error(), "POST /api/v1/projects/p1/milestones") {
			t.Fatalf("exec: got %v", execErr)
		}
	})
	t.Run("update patch transport error", func(t *testing.T) {
		t.Parallel()
		desc := "old"
		remote := &MilestoneRemote{ID: "m1", ProjectID: "p1", Name: "Beta", Description: &desc}
		doc := milestoneCov2Doc()
		doc.Spec.Description = "new"
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/projects":        projects,
			"PATCH /api/v1/milestones/m1": {err: errors.New("down")},
		})
		items, err := doc.Plan(context.Background(), c, remote)
		if err != nil || len(items) != 1 || items[0].Action != internalapi.ActionUpdate {
			t.Fatalf("plan: items=%v err=%v", items, err)
		}
		if execErr := items[0].Exec(context.Background(), c); execErr == nil || !strings.Contains(execErr.Error(), "PATCH /api/v1/milestones/m1") {
			t.Fatalf("exec: got %v", execErr)
		}
	})
}
