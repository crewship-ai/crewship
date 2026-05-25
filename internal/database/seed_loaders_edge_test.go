package database

import (
	"strings"
	"testing"
	"testing/fstest"
)

// ── Workflow templates loader edge cases ─────────────────────────────────
//
// These tests inject adversarial fixtures via testing/fstest.MapFS so the
// loader's validation can be exercised without touching the real
// builtin/workflow-templates/ directory (which must keep shipping valid
// YAML for production seed). Each test pins one failure mode.

func TestLoadWorkflowTemplatesFromFS_EmptyDir(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{}
	_, err := loadWorkflowTemplatesFromFS(fsys, "x")
	if err == nil {
		t.Fatal("expected error for missing dir, got nil")
	}
}

func TestLoadWorkflowTemplatesFromFS_DirExistsButZeroYAMLs(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"x/README.txt": &fstest.MapFile{Data: []byte("not yaml")},
	}
	_, err := loadWorkflowTemplatesFromFS(fsys, "x")
	if err == nil || !strings.Contains(err.Error(), "zero templates loaded") {
		t.Fatalf("expected zero-templates error, got %v", err)
	}
}

func TestLoadWorkflowTemplatesFromFS_MalformedYAML(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"x/broken.yaml": &fstest.MapFile{Data: []byte("name: foo\n  bad_indent:\nicon")},
	}
	_, err := loadWorkflowTemplatesFromFS(fsys, "x")
	if err == nil || !strings.Contains(err.Error(), "parse embedded broken.yaml") {
		t.Fatalf("expected parse error, got %v", err)
	}
}

func TestLoadWorkflowTemplatesFromFS_MissingRequiredField(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		yaml    string
		wantSub string
	}{
		{"no name", "description: d\nicon: i\ncolor: c\ntemplate:\n  steps:\n    - id: a\n      title: t\n", "name is required"},
		{"no description", "name: x\nicon: i\ncolor: c\ntemplate:\n  steps:\n    - id: a\n      title: t\n", "description is required"},
		{"no icon", "name: x\ndescription: d\ncolor: c\ntemplate:\n  steps:\n    - id: a\n      title: t\n", "icon is required"},
		{"no color", "name: x\ndescription: d\nicon: i\ntemplate:\n  steps:\n    - id: a\n      title: t\n", "color is required"},
		{"zero steps", "name: x\ndescription: d\nicon: i\ncolor: c\ntemplate:\n  steps: []\n", "steps is required"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fsys := fstest.MapFS{
				"x/foo.yaml": &fstest.MapFile{Data: []byte(tc.yaml)},
			}
			_, err := loadWorkflowTemplatesFromFS(fsys, "x")
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("want error containing %q, got %v", tc.wantSub, err)
			}
		})
	}
}

func TestLoadWorkflowTemplatesFromFS_DuplicateName(t *testing.T) {
	t.Parallel()
	body := "name: dup\ndescription: d\nicon: i\ncolor: c\ntemplate:\n  steps:\n    - id: a\n      title: t\n"
	fsys := fstest.MapFS{
		"x/a.yaml": &fstest.MapFile{Data: []byte(body)},
		"x/b.yaml": &fstest.MapFile{Data: []byte(body)},
	}
	_, err := loadWorkflowTemplatesFromFS(fsys, "x")
	if err == nil || !strings.Contains(err.Error(), "duplicate template name") {
		t.Fatalf("want duplicate-name error, got %v", err)
	}
}

func TestLoadWorkflowTemplatesFromFS_IgnoresNonYAML(t *testing.T) {
	t.Parallel()
	body := "name: ok\ndescription: d\nicon: i\ncolor: c\ntemplate:\n  steps:\n    - id: a\n      title: t\n"
	fsys := fstest.MapFS{
		"x/good.yaml":     &fstest.MapFile{Data: []byte(body)},
		"x/README.md":     &fstest.MapFile{Data: []byte("# docs")},
		"x/subdir/nested": &fstest.MapFile{Data: []byte("nope")},
		"x/.hidden.yml":   &fstest.MapFile{Data: []byte("filtered by .yaml suffix")},
	}
	docs, err := loadWorkflowTemplatesFromFS(fsys, "x")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(docs) != 1 {
		t.Errorf("want 1 doc, got %d", len(docs))
	}
}

// ── Crew templates loader edge cases ─────────────────────────────────────

func TestLoadCrewTemplatesFromFS_EmptyDir(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{}
	_, err := loadCrewTemplatesFromFS(fsys, "x")
	if err == nil {
		t.Fatal("expected error for missing dir, got nil")
	}
}

func TestLoadCrewTemplatesFromFS_DirExistsButZeroYAMLs(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"x/README.txt": &fstest.MapFile{Data: []byte("not yaml")},
	}
	_, err := loadCrewTemplatesFromFS(fsys, "x")
	if err == nil || !strings.Contains(err.Error(), "zero templates loaded") {
		t.Fatalf("expected zero-templates error, got %v", err)
	}
}

func TestLoadCrewTemplatesFromFS_MalformedYAML(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"x/broken.yaml": &fstest.MapFile{Data: []byte("name: foo\n  bad: -")},
	}
	_, err := loadCrewTemplatesFromFS(fsys, "x")
	if err == nil || !strings.Contains(err.Error(), "parse embedded broken.yaml") {
		t.Fatalf("expected parse error, got %v", err)
	}
}

func TestLoadCrewTemplatesFromFS_MissingRequiredField(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		yaml    string
		wantSub string
	}{
		{
			"no slug",
			"name: x\ndescription: d\nicon: i\ncolor: c\ncategory: ENG\nagents:\n  - slug: a\n    name: A\n",
			"slug is required",
		},
		{
			"no name",
			"slug: x\ndescription: d\nicon: i\ncolor: c\ncategory: ENG\nagents:\n  - slug: a\n    name: A\n",
			"name is required",
		},
		{
			"no description",
			"slug: x\nname: X\nicon: i\ncolor: c\ncategory: ENG\nagents:\n  - slug: a\n    name: A\n",
			"description is required",
		},
		{
			"no icon",
			"slug: x\nname: X\ndescription: d\ncolor: c\ncategory: ENG\nagents:\n  - slug: a\n    name: A\n",
			"icon is required",
		},
		{
			"no color",
			"slug: x\nname: X\ndescription: d\nicon: i\ncategory: ENG\nagents:\n  - slug: a\n    name: A\n",
			"color is required",
		},
		{
			"no category",
			"slug: x\nname: X\ndescription: d\nicon: i\ncolor: c\nagents:\n  - slug: a\n    name: A\n",
			"category is required",
		},
		{
			"zero agents",
			"slug: x\nname: X\ndescription: d\nicon: i\ncolor: c\ncategory: ENG\nagents: []\n",
			"agents is required",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fsys := fstest.MapFS{
				"x/foo.yaml": &fstest.MapFile{Data: []byte(tc.yaml)},
			}
			_, err := loadCrewTemplatesFromFS(fsys, "x")
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("want error containing %q, got %v", tc.wantSub, err)
			}
		})
	}
}

func TestLoadCrewTemplatesFromFS_DuplicateSlug(t *testing.T) {
	t.Parallel()
	body := "slug: dup\nname: X\ndescription: d\nicon: i\ncolor: c\ncategory: ENG\nagents:\n  - slug: a\n    name: A\n"
	fsys := fstest.MapFS{
		"x/a.yaml": &fstest.MapFile{Data: []byte(body)},
		"x/b.yaml": &fstest.MapFile{Data: []byte(body)},
	}
	_, err := loadCrewTemplatesFromFS(fsys, "x")
	if err == nil || !strings.Contains(err.Error(), "duplicate template slug") {
		t.Fatalf("want duplicate-slug error, got %v", err)
	}
}

func TestLoadCrewTemplatesFromFS_HappyPath(t *testing.T) {
	t.Parallel()
	body := "slug: ok\nname: OK\ndescription: d\nicon: i\ncolor: c\ncategory: ENG\nagents:\n  - slug: a\n    name: A\n"
	fsys := fstest.MapFS{
		"x/foo.yaml": &fstest.MapFile{Data: []byte(body)},
	}
	docs, err := loadCrewTemplatesFromFS(fsys, "x")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(docs) != 1 || docs[0].Slug != "ok" {
		t.Errorf("got %+v, want 1 doc with slug=ok", docs)
	}
}
