//go:build !windows

package docker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/crewstart"
	"github.com/crewship-ai/crewship/internal/provider"
)

type parityPathContainer struct {
	provider.ContainerProvider
	path string
}

func (p parityPathContainer) ContainerPath(context.Context, string) (string, error) {
	return p.path, nil
}

func TestNativePathRuntimeTerminalCollisionParity(t *testing.T) {
	// These are explicitly synthetic executables representing a crew with
	// competing npm-feature and mise installations, never real CLI versions.
	root := t.TempDir()
	locations := map[string]string{
		"/usr/local/share/npm-global/bin": "fixture-npm-1",
		"/opt/mise/data/shims":            "fixture-mise-1",
	}
	for dir, version := range locations {
		local := filepath.Join(root, dir)
		if err := os.MkdirAll(local, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(local, "claude"), []byte("#!/bin/sh\nprintf '"+version+"\\n'\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for _, input := range []string{
		"/opt/mise/data/shims:/usr/local/share/npm-global/bin:/usr/bin:/bin",
		"/usr/local/share/npm-global/bin:/opt/mise/data/shims:/usr/bin:/bin",
	} {
		runtime := envValue(applyAgentLoginPath([]string{"PATH=" + input}, "", nil), "PATH")
		env, err := crewstart.NativeToolExecEnv(context.Background(), parityPathContainer{path: input}, "fixture", nil)
		if err != nil {
			t.Fatal(err)
		}
		terminal := envValue(env, "PATH")
		if runtime != terminal {
			t.Errorf("same input produces different runtime/terminal PATH:\n%s\n%s", runtime, terminal)
		}
		var observed []string
		for _, path := range []string{runtime, terminal} {
			// Materialize every container directory under the fixture root,
			// including empty home/feature dirs. A host-installed executable
			// must never shadow the two synthetic collision candidates.
			parts := strings.Split(path, ":")
			for i, dir := range parts {
				parts[i] = filepath.Join(root, dir)
			}
			cmd := exec.Command("/bin/sh", "-c", "command -v claude && claude --version")
			cmd.Env = []string{"PATH=" + strings.Join(parts, ":")}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("fixture lookup: %v %s", err, out)
			}
			observed = append(observed, string(out))
		}
		if observed[0] != observed[1] {
			t.Errorf("runtime and terminal chose different fixture binaries:\n%s\n%s", observed[0], observed[1])
		}
		wantDir := "/opt/mise/data/shims"
		if strings.Index(input, "/usr/local/share/npm-global/bin") < strings.Index(input, wantDir) {
			wantDir = "/usr/local/share/npm-global/bin"
		}
		want := filepath.Join(root, wantDir, "claude") + "\n" + locations[wantDir] + "\n"
		for _, actual := range observed {
			if actual != want {
				t.Errorf("existing operator precedence changed: got %q, want %q", actual, want)
			}
		}
	}
}
