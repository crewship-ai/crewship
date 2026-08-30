package pipeline_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"
)

// Module paths that must not come back. Each one is a dead end that still
// builds, which is the whole reason a test has to say so.
//
// cel-go renamed itself: v0.31.0 declares `module github.com/google/cel-go`,
// v0.32.0 and later declare `module cel.dev/cel-go`. Requiring the old path
// therefore pins us to v0.31.0 forever — it compiles, it passes every test,
// and the dependency bot cannot report it because a rename needs a source
// edit, so it fails its whole go_modules job and says so only in a job log.
// The advisory channel goes quiet too: anything published against
// cel.dev/cel-go does not match a requirement naming github.com/google/cel-go.
// See #2067.
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

// goModOffenders returns a description of every go.mod directive naming path.
//
// go.mod is parsed rather than scanned line by line: `require` has both a
// block form (the path is the first token on the line) and a one-line form
// (`require <path> <version>`, where it is the second). A hand-rolled check
// that assumes the first token silently misses the one-line form, which is
// what `go mod edit -require=` writes — a guard that can be bypassed by the
// most obvious way of adding the requirement back is not a guard.
func goModOffenders(src []byte, path string) ([]string, error) {
	f, err := modfile.Parse("go.mod", src, nil)
	if err != nil {
		return nil, err
	}

	var found []string
	for _, r := range f.Require {
		if r.Mod.Path == path {
			found = append(found, "require "+r.Mod.Path+" "+r.Mod.Version)
		}
	}
	// A replace or exclude naming the path means it is still in play even if
	// the require has been renamed, so they are reported too.
	for _, r := range f.Replace {
		if r.Old.Path == path || r.New.Path == path {
			found = append(found, "replace "+r.Old.Path+" => "+r.New.Path)
		}
	}
	for _, e := range f.Exclude {
		if e.Mod.Path == path {
			found = append(found, "exclude "+e.Mod.Path+" "+e.Mod.Version)
		}
	}
	return found, nil
}

// TestGoModOffenders covers the parsing itself, including the one-line
// `require` form that the first version of this guard was blind to.
func TestGoModOffenders(t *testing.T) {
	const target = "github.com/google/cel-go"

	cases := []struct {
		name string
		src  string
		want int
	}{
		{
			name: "block form require",
			src:  "module m\n\ngo 1.26\n\nrequire (\n\tgithub.com/google/cel-go v0.31.0\n)\n",
			want: 1,
		},
		{
			name: "one-line require form",
			src:  "module m\n\ngo 1.26\n\nrequire github.com/google/cel-go v0.31.0\n",
			want: 1,
		},
		{
			name: "block form marked indirect",
			src:  "module m\n\ngo 1.26\n\nrequire (\n\tgithub.com/google/cel-go v0.31.0 // indirect\n)\n",
			want: 1,
		},
		{
			name: "replace directive",
			src:  "module m\n\ngo 1.26\n\nrequire cel.dev/cel-go v0.32.0\n\nreplace cel.dev/cel-go => github.com/google/cel-go v0.31.0\n",
			want: 1,
		},
		{
			name: "exclude directive",
			src:  "module m\n\ngo 1.26\n\nrequire cel.dev/cel-go v0.32.0\n\nexclude github.com/google/cel-go v0.31.0\n",
			want: 1,
		},
		{
			name: "the migrated path is not an offender",
			src:  "module m\n\ngo 1.26\n\nrequire cel.dev/cel-go v0.32.0\n",
			want: 0,
		},
		{
			name: "a longer path that merely shares the prefix",
			src:  "module m\n\ngo 1.26\n\nrequire github.com/google/cel-go-extras v1.0.0\n",
			want: 0,
		},
		{
			name: "named only in a comment",
			src:  "module m\n\ngo 1.26\n\n// github.com/google/cel-go was removed in #2067\nrequire cel.dev/cel-go v0.32.0\n",
			want: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := goModOffenders([]byte(tc.src), target)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(got) != tc.want {
				t.Errorf("offenders = %d %v, want %d", len(got), got, tc.want)
			}
		})
	}
}

// TestForbiddenModulePaths_NotInGoMod is the load-bearing half: go.mod is what
// decides which module is actually fetched, whatever the imports say.
func TestForbiddenModulePaths_NotInGoMod(t *testing.T) {
	root := moduleRoot(t)

	src, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}

	for _, tc := range forbiddenModulePaths {
		t.Run(tc.name, func(t *testing.T) {
			offenders, err := goModOffenders(src, tc.path)
			if err != nil {
				t.Fatalf("parse go.mod: %v", err)
			}
			for _, o := range offenders {
				t.Errorf("go.mod has %q — use %q instead: %s", o, tc.instead, tc.why)
			}
		})
	}
}

// TestForbiddenModulePaths_NotImported catches the case where someone adds the
// import back and `go mod tidy` obligingly restores the requirement.
func TestForbiddenModulePaths_NotImported(t *testing.T) {
	root := moduleRoot(t)
	self := filepath.Join(root, "internal", "pipeline", "cel_module_path_test.go")

	for _, tc := range forbiddenModulePaths {
		t.Run(tc.name, func(t *testing.T) {
			needle := `"` + tc.path + `/`

			err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					switch d.Name() {
					// `.claude` holds agent worktrees: whole copies of this tree. This
					// guard's assertion happens not to be keyed by path, so the
					// copies pass rather than fail — but it walks six trees to
					// answer for one, and stops being harmless the day it grows
					// a path-keyed exemption (#2188).
					case ".git", ".claude", "node_modules", "out", "vendor":
						return filepath.SkipDir
					}
					return nil
				}
				// This file names the forbidden path on purpose.
				if path == self || !strings.HasSuffix(path, ".go") {
					return nil
				}
				src, readErr := os.ReadFile(path)
				if readErr != nil {
					return readErr
				}
				if strings.Contains(string(src), needle) {
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
