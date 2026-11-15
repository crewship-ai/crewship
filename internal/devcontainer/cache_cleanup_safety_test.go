package devcontainer

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCacheCleanupSkipsSymlinkAndEmptiesRealDirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := filepath.Join(root, "installed tools")
	cache := filepath.Join(root, "cache")
	link := filepath.Join(root, "cache-link")
	for _, dir := range []string{target, cache} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"tool", ".hidden", "..hidden"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("keep installed bytes"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	// Execute the production deletion body only against this test's paths.
	script := "set -eu\nfor path in " + shellQuote(link) + " " + shellQuote(cache) + "; do\n" + emptyCacheDirectoryScript + "\ndone"
	if output, err := exec.CommandContext(t.Context(), "bash", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("cleanup: %v: %s", err, output)
	}
	for _, tc := range []struct {
		dir  string
		want int
	}{{target, 3}, {cache, 0}} {
		entries, err := os.ReadDir(tc.dir)
		if err != nil || len(entries) != tc.want {
			t.Fatalf("%s: entries=%d, want=%d, error=%v", tc.dir, len(entries), tc.want, err)
		}
	}
}

func TestBuildOnlyCleansMiseCacheInInstallationLayer(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		env  map[string]string
		keep bool
	}{
		{"default", nil, false},
		{"runtime descendant", map[string]string{"MISE_CACHE_DIR": miseBuildCacheDir + "/runtime"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := NewBuildOnlyProvisioner(nil, nil, slog.Default())
			rec := &dockerfileRecorder{}
			if err := p.recordProvisionSteps(context.Background(), rec, nil, &Config{}, `{"tools":{"node":"22"}}`, tc.env, nil); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, group := range rec.groups {
				script := strings.Join(group.cmds, "\n")
				install := strings.Index(script, "mise install")
				if install < 0 {
					continue
				}
				found = true
				cleanup := strings.Index(script, "for path in")
				if group.user != "1001:1001" || (!tc.keep && cleanup <= install) || (tc.keep && cleanup >= 0) {
					t.Fatalf("installation layer does not respect cache policy: user=%s\n%s", group.user, script)
				}
			}
			if !found {
				t.Fatal("mise installation layer missing")
			}
		})
	}
}

func TestBuildOnlyEmptyMiseDoesNotAddInstallationLayer(t *testing.T) {
	t.Parallel()
	for _, config := range []string{`{}`, `{"tools":{}}`, `{"ai_cli_check":"record"}`, `{"env":{"NODE_ENV":"test"}}`} {
		p := NewBuildOnlyProvisioner(nil, nil, slog.Default())
		rec := &dockerfileRecorder{}
		if err := p.recordProvisionSteps(t.Context(), rec, nil, &Config{}, config, nil, nil); err != nil {
			t.Fatal(err)
		}
		for _, group := range rec.groups {
			if group.user == "1001:1001" {
				t.Fatalf("empty mise config %s added an agent RUN: %v", config, group.cmds)
			}
		}
	}
}
