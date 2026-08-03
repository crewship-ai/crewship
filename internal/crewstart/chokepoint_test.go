package crewstart_test

// The guard on the defect class.
//
// #1708 and #1717 were not three bugs, they were thirteen call sites that each
// decided for itself what "start a crew" means — and two features (sidecars,
// the provisioned image) that were therefore wired at one or ten of them. The
// fix moves the contract into crewstart.Starter.Start; this test is what keeps
// it there.
//
// It fails on a FOURTEENTH direct caller, which is the direction that matters:
// a test that merely counted the sites would have gone green the day someone
// added one. Nothing here asserts behaviour — the behaviour tests live beside
// each path — this asserts only that there is exactly one door.
//
// Adding a legitimate exception means adding it to allowedDirect below WITH a
// reason, which is a line a reviewer sees.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// allowedDirect maps a repo-relative file to why it may call
// ContainerProvider.EnsureCrewRuntime without going through this package.
var allowedDirect = map[string]string{
	"internal/provider/container.go":               "the interface declaration itself",
	"internal/provider/docker/docker_container.go": "the docker implementation",
	"internal/provider/apple/apple_runtime.go":     "the apple-container implementation",
	"internal/crewstart/crewstart.go":              "the chokepoint",
	"internal/orchestrator/preflight_batch.go": "a pure ContainerProvider delegation wrapper — it forwards the call, " +
		"it does not decide to start a crew",
}

func TestEnsureCrewRuntimeHasExactlyOneCaller(t *testing.T) {
	root := repoRoot(t)

	var offenders []string
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			rel = filepath.ToSlash(rel)
			if _, ok := allowedDirect[rel]; ok {
				return nil
			}
			body, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			// ".EnsureCrewRuntime(" is a call on a value; the bare identifier
			// also appears in prose comments across the repo and must not trip
			// the guard.
			if strings.Contains(stripComments(string(body)), ".EnsureCrewRuntime(") {
				offenders = append(offenders, rel)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}

	if len(offenders) > 0 {
		t.Errorf("these files call ContainerProvider.EnsureCrewRuntime directly:\n  %s\n\n"+
			"Starting a crew means: resolve the crew's provisioned image and declared sidecar\n"+
			"services, create/reuse the runtime container, THEN bring the sidecars up. That is\n"+
			"crewstart.Starter.Start. A direct EnsureCrewRuntime call gets the middle step only,\n"+
			"which is how #1708 (headless paths ran database-less) and #1717 (the terminal ran the\n"+
			"wrong image) each survived across ten-plus call sites. Use the Starter, or add the file\n"+
			"to allowedDirect with a reason.", strings.Join(offenders, "\n  "))
	}
}

// stripComments removes // and /* */ comments so the guard reads code only.
// Deliberately simple: it does not understand string literals containing
// comment markers, which would only ever cause a false NEGATIVE on a file that
// has no reason to hold such a literal next to a crew start.
func stripComments(src string) string {
	var out strings.Builder
	out.Grow(len(src))
	for i := 0; i < len(src); {
		switch {
		case strings.HasPrefix(src[i:], "//"):
			nl := strings.IndexByte(src[i:], '\n')
			if nl < 0 {
				return out.String()
			}
			i += nl
		case strings.HasPrefix(src[i:], "/*"):
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return out.String()
			}
			i += end + 4
		default:
			out.WriteByte(src[i])
			i++
		}
	}
	return out.String()
}

// repoRoot walks up from the test's working directory to the module root.
func repoRoot(t *testing.T) string {
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
