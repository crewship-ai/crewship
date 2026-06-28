package kinds

// Coverage-focused tests for milestone.go: list helper error branches,
// export error propagation, the remote lookup, and the diff helpers.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

func milestoneCovStrPtr(s string) *string { return &s }

func TestMilestoneCov_ListProjects(t *testing.T) {
	t.Parallel()
	path := "/api/v1/projects"

	t.Run("transport error", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {err: errors.New("down")}})
		if _, err := milestoneListProjects(context.Background(), c); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("non-2xx", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {status: 500, body: "x"}})
		if _, err := milestoneListProjects(context.Background(), c); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("bad body", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {badBody: true}})
		if _, err := milestoneListProjects(context.Background(), c); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("empty body", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {body: ""}})
		out, err := milestoneListProjects(context.Background(), c)
		if err != nil || out != nil {
			t.Fatalf("out=%v err=%v", out, err)
		}
	})
	t.Run("decode error", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {body: "not json"}})
		if _, err := milestoneListProjects(context.Background(), c); err == nil || !strings.Contains(err.Error(), "decode") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestMilestoneCov_ListForProject(t *testing.T) {
	t.Parallel()
	path := "/api/v1/projects/p1/milestones"

	t.Run("transport error", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {err: errors.New("down")}})
		if _, err := milestoneListForProject(context.Background(), c, "p1"); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("non-2xx", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {status: 403, body: "no"}})
		if _, err := milestoneListForProject(context.Background(), c, "p1"); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("bad body", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {badBody: true}})
		if _, err := milestoneListForProject(context.Background(), c, "p1"); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("empty body", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {body: ""}})
		out, err := milestoneListForProject(context.Background(), c, "p1")
		if err != nil || out != nil {
			t.Fatalf("out=%v err=%v", out, err)
		}
	})
	t.Run("decode error", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {body: "not json"}})
		if _, err := milestoneListForProject(context.Background(), c, "p1"); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("success", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {body: `[{"id":"m1","project_id":"p1","name":"v1","status":"planned"}]`}})
		out, err := milestoneListForProject(context.Background(), c, "p1")
		if err != nil || len(out) != 1 || out[0].Name != "v1" {
			t.Fatalf("out=%v err=%v", out, err)
		}
	})
}

func TestMilestoneCov_ResolveProjectIDBySlug(t *testing.T) {
	t.Parallel()
	c := newCovClient(map[string]covRoute{"GET /api/v1/projects": {body: `[{"id":"p1","slug":"apollo"}]`}})
	id, err := milestoneResolveProjectIDBySlug(context.Background(), c, "apollo")
	if err != nil || id != "p1" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if _, err := milestoneResolveProjectIDBySlug(context.Background(), c, "ghost"); err == nil || !strings.Contains(err.Error(), `"ghost" not found`) {
		t.Fatalf("got %v", err)
	}
}

func TestMilestoneCov_ExportMilestones(t *testing.T) {
	t.Parallel()

	t.Run("projects list error", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET /api/v1/projects": {status: 500, body: "x"}})
		if _, err := ExportMilestones(context.Background(), c); err == nil || !strings.Contains(err.Error(), "list projects") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("per-project list error", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/projects":               {body: `[{"id":"p1","slug":"apollo"}]`},
			"GET /api/v1/projects/p1/milestones": {status: 500, body: "x"},
		})
		if _, err := ExportMilestones(context.Background(), c); err == nil || !strings.Contains(err.Error(), `export milestones for project "apollo"`) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("namespaced slugs", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/projects": {body: `[{"id":"p1","slug":"apollo"}]`},
			"GET /api/v1/projects/p1/milestones": {body: `[{
				"id":"m1","project_id":"p1","name":"V1 Launch!",
				"description":"first cut","target_date":"2026-07-01","status":"active"
			}]`},
		})
		docs, err := ExportMilestones(context.Background(), c)
		if err != nil || len(docs) != 1 {
			t.Fatalf("docs=%v err=%v", docs, err)
		}
		d := docs[0]
		if d.Metadata.Slug != "apollo--v1-launch" {
			t.Errorf("slug = %q", d.Metadata.Slug)
		}
		if d.Spec.ProjectSlug != "apollo" || d.Spec.TargetDate != "2026-07-01" || d.Spec.Status != "active" {
			t.Errorf("spec = %+v", d.Spec)
		}
	})
}

func TestMilestoneCov_LookupMilestoneRemote(t *testing.T) {
	t.Parallel()
	doc := &MilestoneDocument{
		Metadata: internalapi.Metadata{Name: "v1", Slug: "v1"},
		Spec:     MilestoneSpec{ProjectSlug: "apollo"},
	}

	t.Run("project missing", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET /api/v1/projects": {body: `[]`}})
		if _, err := LookupMilestoneRemote(context.Background(), c, doc); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("milestone list error", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/projects":               {body: `[{"id":"p1","slug":"apollo"}]`},
			"GET /api/v1/projects/p1/milestones": {status: 500, body: "x"},
		})
		if _, err := LookupMilestoneRemote(context.Background(), c, doc); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("found and missing", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{
			"GET /api/v1/projects":               {body: `[{"id":"p1","slug":"apollo"}]`},
			"GET /api/v1/projects/p1/milestones": {body: `[{"id":"m1","name":"v1","status":"planned"}]`},
		})
		got, err := LookupMilestoneRemote(context.Background(), c, doc)
		if err != nil || got == nil || got.ID != "m1" {
			t.Fatalf("got=%+v err=%v", got, err)
		}
		other := *doc
		other.Metadata.Name = "v2"
		got, err = LookupMilestoneRemote(context.Background(), c, &other)
		if err != nil || got != nil {
			t.Fatalf("got=%+v err=%v", got, err)
		}
	})
}

func TestMilestoneCov_DiffPatch(t *testing.T) {
	t.Parallel()
	d := &MilestoneDocument{
		Metadata: internalapi.Metadata{Name: "v1", Slug: "v1"},
		Spec: MilestoneSpec{
			ProjectSlug: "apollo",
			Description: "new",
			TargetDate:  "2026-07-01",
			Status:      "active",
		},
	}
	remote := &MilestoneRemote{
		ID: "m1", Name: "old",
		Description: milestoneCovStrPtr("old"),
		TargetDate:  milestoneCovStrPtr("2026-01-01"),
		Status:      "planned",
	}
	patch := d.diffPatch(remote)
	for _, k := range []string{"name", "description", "target_date", "status"} {
		if _, ok := patch[k]; !ok {
			t.Errorf("patch missing %q: %v", k, patch)
		}
	}

	same := &MilestoneRemote{
		ID: "m1", Name: "v1",
		Description: milestoneCovStrPtr("new"),
		TargetDate:  milestoneCovStrPtr("2026-07-01"),
		Status:      "active",
	}
	if p := d.diffPatch(same); len(p) != 0 {
		t.Errorf("matching remote should yield empty patch, got %v", p)
	}

	// Status omitted in manifest → status never patched.
	noStatus := *d
	noStatus.Spec.Status = ""
	p := noStatus.diffPatch(remote)
	if _, ok := p["status"]; ok {
		t.Errorf("status should be skipped when undeclared: %v", p)
	}
}

func TestMilestoneCov_SmallHelpers(t *testing.T) {
	t.Parallel()
	if err := milestoneCheckStatus(nil, "op"); err == nil || !strings.Contains(err.Error(), "nil response") {
		t.Errorf("nil resp: %v", err)
	}
	if got := milestoneSlugFromName("___"); got != "milestone" {
		t.Errorf("degenerate name slug = %q", got)
	}
	if got := milestoneSlugFromName("V1.0 Launch_pad"); got != "v1-0-launch-pad" {
		t.Errorf("slug = %q", got)
	}
	if b, err := readAll(nil); b != nil || err != nil {
		t.Errorf("readAll(nil) = %v, %v", b, err)
	}
}
