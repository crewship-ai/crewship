package seeddata

import (
	"bytes"
	"testing"

	pagesdemo "github.com/crewship-ai/crewship/examples/pages-apps"
	"github.com/crewship-ai/crewship/internal/pages"
	pageprofile "github.com/crewship-ai/crewship/tools/pages-build"
)

func TestSeedPageProjectsMatchInstalledCompilerProfile(t *testing.T) {
	bundle, err := pages.ParseProjectTransfer(bytes.NewReader(pagesdemo.Bundle))
	if err != nil {
		t.Fatal(err)
	}
	canonical := pageprofile.Source()
	check := func(t *testing.T, project *pages.SourceProject) {
		t.Helper()
		if project == nil {
			t.Fatal("seeded app has no source project")
		}
		if err := pageprofile.ValidateDependencies(project); err != nil {
			t.Errorf("demo source cannot build with the installed compiler: %v", err)
		}
		// The portable example must also retain the canonical package overrides,
		// not merely the dependency maps checked by the offline compiler.
		for _, required := range canonical.Files {
			if required.Path != "package.json" && required.Path != "pnpm-lock.yaml" {
				continue
			}
			found := false
			for _, file := range project.Files {
				if file.Path == required.Path {
					found = true
					if file.Content != required.Content || file.Encoding != required.Encoding {
						t.Errorf("%s differs from the canonical portable compiler profile", required.Path)
					}
				}
			}
			if !found {
				t.Errorf("missing %s", required.Path)
			}
		}
	}
	t.Run("portable-bundle", func(t *testing.T) { check(t, bundle.Project) })
	expected := map[string]bool{"demo-sales": false, "demo-finance": false, "demo-marketing": false,
		"demo-shipping": false, "custom-operations": false, "demo-live": false}
	for _, page := range Pages {
		if page.Project == nil {
			continue
		}
		if _, ok := expected[page.Slug]; ok {
			expected[page.Slug] = true
		}
		t.Run(page.Slug, func(t *testing.T) { check(t, page.Project) })
	}
	for slug, found := range expected {
		if !found {
			t.Errorf("required seeded app %s was not exercised", slug)
		}
	}
}
