//go:build linux

package docker

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"golang.org/x/sys/unix"

	"github.com/crewship-ai/crewship/internal/provider/runtimestage"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
)

func privateEnvFixture(t *testing.T) (*Provider, container.InspectResponse, string) {
	t.Helper()
	p := &Provider{cfg: Config{OutputBasePath: t.TempDir(), InstanceID: "installation"}}
	if err := os.Mkdir(filepath.Join(p.cfg.OutputBasePath, runtimestage.DirName), 0755); err != nil {
		t.Fatal(err)
	}
	c := container.InspectResponse{Config: &container.Config{Labels: map[string]string{resourcelifecycle.InstanceLabel: "installation", crewCrewIDLabel: "crew", stagedEnvLabel: newStagedEnvHandle()}}}
	c.ID = "runtime"
	c.Image = "immutable-image"
	if os.Geteuid() == 1001 || os.Geteuid() == 1002 {
		if !errors.Is(p.saveStagedEnv(c, []string{"TOKEN=synthetic"}), errStagedDenied) {
			t.Fatal("workload UID admitted")
		}
		t.Skip("host-private positive fixture requires qualified host UID; denial verified")
	}
	if err := p.saveStagedEnv(c, []string{"TOKEN=synthetic"}); err != nil {
		t.Fatal(err)
	}
	target, err := p.stagedEnvPath(c)
	if err != nil {
		t.Fatal(err)
	}
	return p, c, target
}

func TestPrivateEnvPublicationNeverOverwrites(t *testing.T) {
	p, c, target := privateEnvFixture(t)
	if err := p.saveStagedEnv(c, []string{"TOKEN=replacement"}); !errors.Is(err, errStagedDenied) {
		t.Fatalf("duplicate publication: %v", err)
	}
	got, err := p.loadStagedEnv(c)
	if err != nil || !reflect.DeepEqual(got, []string{"TOKEN=synthetic"}) {
		t.Fatalf("original changed: %v %v", got, err)
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private mode: %v %v", info, err)
	}
	if err := p.removeStagedEnv(c); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("owned material retained: %v", err)
	}
	if err := p.removeStagedEnv(c); err != nil {
		t.Fatalf("idempotent cleanup: %v", err)
	}
}

func TestPrivateEnvSpecialFilesAndScopedCleanup(t *testing.T) {
	for _, kind := range []string{"fifo", "file-symlink", "scope-symlink", "corrupt", "wrong-mode", "other-image", "other-installation", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			p, c, target := privateEnvFixture(t)
			other := c
			other.ID = "other-runtime"
			if err := p.saveStagedEnv(other, []string{"OTHER=retained"}); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(t.TempDir(), filepath.Base(target))
			if err := os.WriteFile(outside, []byte("unchanged"), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "fifo":
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
				if err := unix.Mkfifo(target, 0600); err != nil {
					t.Fatal(err)
				}
			case "file-symlink":
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, target); err != nil {
					t.Fatal(err)
				}
			case "scope-symlink":
				if err := os.Rename(filepath.Dir(target), filepath.Dir(target)+"-kept"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Dir(outside), filepath.Dir(target)); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				if err := os.WriteFile(target, []byte("invalid"), 0600); err != nil {
					t.Fatal(err)
				}
			case "wrong-mode":
				if err := os.Chmod(target, 0644); err != nil {
					t.Fatal(err)
				}
			case "other-image":
				c.Image = "foreign-image"
			case "other-installation":
				c.Config.Labels[resourcelifecycle.InstanceLabel] = "foreign-installation"
			case "oversize":
				if err := os.WriteFile(target, []byte(strings.Repeat("x", (1<<20)+1)), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := p.loadStagedEnv(c); !errors.Is(err, errStagedDenied) {
				t.Fatalf("load admitted %s: %v", kind, err)
			}
			err := p.removeStagedEnv(c)
			// A different image selects an absent scope: safe idempotent cleanup leaves original.
			if kind != "other-image" && !errors.Is(err, errStagedDenied) {
				t.Fatalf("cleanup admitted %s: %v", kind, err)
			}
			if _, err := os.Lstat(target); err != nil {
				t.Fatalf("untrusted material removed: %v", err)
			}
			if raw, err := os.ReadFile(outside); err != nil || string(raw) != "unchanged" {
				t.Fatalf("outside modified: %v", err)
			}
			// Restore the metadata shared by the shallow copy before checking other scope.
			c.Config.Labels[resourcelifecycle.InstanceLabel] = "installation"
			if env, err := p.loadStagedEnv(other); err != nil || !reflect.DeepEqual(env, []string{"OTHER=retained"}) {
				t.Fatalf("unrelated material changed: %v %v", env, err)
			}
		})
	}
}

func TestPrivateEnvRejectsSymlinkAncestor(t *testing.T) {
	p, c, _ := privateEnvFixture(t)
	real := p.cfg.OutputBasePath
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	p.cfg.OutputBasePath = alias
	if _, err := p.loadStagedEnv(c); !errors.Is(err, errStagedDenied) {
		t.Fatalf("aliased ancestor admitted: %v", err)
	}
	if err := p.removeStagedEnv(c); !errors.Is(err, errStagedDenied) {
		t.Fatalf("aliased cleanup admitted: %v", err)
	}
}

func TestKeeperEnvironmentBlanksImageValues(t *testing.T) {
	got := keeperEnv([]string{"TOKEN=private", "PATH=/home/agent/bin"}, map[string]string{"BASH_ENV": "/evil", "GODEBUG": "evil"})
	joined := strings.Join(got, "\n")
	if strings.Contains(joined, "private") || strings.Contains(joined, "/evil") || strings.Contains(joined, "/home/agent") || !strings.Contains(joined, "BASH_ENV=") {
		t.Fatalf("image values inherited: %v", got)
	}
}
