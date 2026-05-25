package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExampleManifests_ParseAndValidate walks examples/manifests/ and
// runs Load + the kind-validation pass on every *.yaml fixture. Caught
// in iter-7 of the bug-hunt loop: full-complete.yaml shipped with two
// invalid tool_profile values ("full" lowercase + "review" not in the
// enum) that anyone copying the example as a starter hit immediately
// at `crewship apply --dry-run`. Pinning a Load+Validate sweep here so
// the next drift surfaces in CI instead of at first user touch.
//
// We deliberately do NOT call Plan(): planning hits the live server
// for remote lookups, which a unit test can't provide. Load+Validate
// is the offline-checkable surface and covers the most common drift
// (renamed fields, value-enum violations, cross-kind reference typos).
func TestExampleManifests_ParseAndValidate(t *testing.T) {
	t.Parallel()
	// Repo layout: this file lives in internal/manifest/, the examples
	// in examples/manifests/. The relative path lands the right dir
	// under `go test ./...` from the repo root.
	dir := filepath.Join("..", "..", "examples", "manifests")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	yamls := []string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		yamls = append(yamls, e.Name())
	}
	if len(yamls) == 0 {
		t.Fatalf("no *.yaml files under %s", dir)
	}
	for _, name := range yamls {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(dir, name)
			b, err := LoadFile(path)
			if err != nil {
				t.Fatalf("LoadFile %s: %v", path, err)
			}
			// Two validation surfaces:
			//   1. Bundle.Validate() covers the legacy nested
			//      Crew/Workspace bundle (the wrapped form most of
			//      the examples use). This catches enum violations
			//      on tool_profile, agent_role, cli_adapter, etc.
			//   2. validateAllKinds covers the SPEC-2 top-level
			//      Skill/Crew/Agent/Integration/Issue docs.
			// Catching both is what surfaces drift like the
			// full-complete.yaml `tool_profile: full` (lowercase)
			// + `tool_profile: review` (not in enum) regression.
			if err := b.Validate(); err != nil {
				t.Errorf("Bundle.Validate %s: %v", path, err)
			}
			wsCtx := buildKindWorkspaceContext(b)
			if err := validateAllKinds(b, wsCtx); err != nil {
				t.Errorf("validateAllKinds %s: %v", path, err)
			}
		})
	}
}
