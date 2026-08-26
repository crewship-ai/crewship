package pipeline_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Module paths that must not come back. Each one is a dead end that still
// builds, which is the whole reason a test has to say so.
//
// cel-go renamed itself: v0.31.0 declares `module github.com/google/cel-go`,
// v0.32.0 and later declare `module cel.dev/cel-go`. Requiring the old path
// therefore pins us to v0.31.0 forever — it compiles, it passes every test,
// and Dependabot cannot report it because a rename needs a source edit, so it
// fails its whole go_modules job instead and says so only in a job log. The
// advisory channel goes quiet too: anything published against cel.dev/cel-go
// does not match a requirement naming github.com/google/cel-go. See #2067.
var forbiddenModulePaths = []struct {
	name    string
	path    string
	instead string
	why     string
}{
	{
		name:    "cel-go renamed to cel.dev in v0.32.0",
		path:    "github.com/google/cel-go",
		instead: "cel.dev/cel-go",
		why:     "the old path is frozen at v0.31.0 and receives no further releases or advisories (#2067)",
	},
}

// TestForbiddenModulePaths_NotInGoMod is the load-bearing half: go.mod is what
// decides which module is actually fetched, whatever the imports say.
func TestForbiddenModulePaths_NotInGoMod(t *testing.T) {
	root := moduleRoot(t)

	goMod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}

	for _, tc := range forbiddenModulePaths {
		t.Run(tc.name, func(t *testing.T) {
			for _, line := range strings.Split(string(goMod), "\n") {
				// Match the require entry, not a longer path that merely
				// starts with it.
				fields := strings.Fields(strings.TrimSpace(line))
				if len(fields) > 0 && fields[0] == tc.path {
					t.Errorf("go.mod requires %q — use %q instead: %s", tc.path, tc.instead, tc.why)
				}
			}
		})
	}
}

// TestForbiddenModulePaths_NotImported catches the case where someone adds the
// import back and `go mod tidy` obligingly restores the requirement.
func TestForbiddenModulePaths_NotImported(t *testing.T) {
	root := moduleRoot(t)

	for _, tc := range forbiddenModulePaths {
		t.Run(tc.name, func(t *testing.T) {
			needle := []byte(`"` + tc.path + `/`)

			err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					switch d.Name() {
					case ".git", "node_modules", "out", "vendor":
						return filepath.SkipDir
					}
					return nil
				}
				if !strings.HasSuffix(path, ".go") {
					return nil
				}
				// This file names the forbidden path on purpose.
				if path == thisFile(t, root) {
					return nil
				}
				src, readErr := os.ReadFile(path)
				if readErr != nil {
					return readErr
				}
				if strings.Contains(string(src), string(needle)) {
					rel, relErr := filepath.Rel(root, path)
					if relErr != nil {
						rel = path
					}
					t.Errorf("%s imports %q — use %q instead: %s", rel, tc.path, tc.instead, tc.why)
				}
				return nil
			})
			if err != nil {
				t.Fatalf("walk repo: %v", err)
			}
		})
	}
}

// thisFile is the guard's own path, which legitimately contains the string it
// forbids everywhere else.
func thisFile(t *testing.T, root string) string {
	t.Helper()
	return filepath.Join(root, "internal", "pipeline", "cel_module_path_test.go")
}

// moduleRoot walks up from the test's working directory to the module root.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatal("could not locate the module root (no go.mod above the test's working directory)")
	return ""
}
