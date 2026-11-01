package pipeline

import (
	"os"
	"path/filepath"
	"testing"
)

// Harness prerequisites must remain executable after the demo catalogue changes.
func TestHarnessRoutineFixtures(t *testing.T) {
	paths, err := filepath.Glob("../../scripts/test-harness/fixtures/*.json")
	if err != nil || len(paths) != 6 {
		t.Fatalf("fixture inventory: %v (%d)", err, len(paths))
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			dsl, err := Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			if err := Validate(dsl, map[string]struct{}{"sam": {}, "robin": {}}, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}
