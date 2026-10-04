//go:build linux

package docker

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"

	"github.com/crewship-ai/crewship/internal/provider/runtimestage"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
)

func TestRuntimeRemovalCleansOnlyAuthenticatedScopeAfterSuccess(t *testing.T) {
	for _, mode := range []string{"success", "remove-failure", "foreign-scope", "unknown-after-restart", "recovered-scope", "alias", "scope-symlink", "file-symlink", "mutated-labels"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			p, close := newFakeDockerProvider(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodDelete {
					t.Errorf("new inspect introduced: %s %s", r.Method, r.URL.Path)
				}
				if mode == "remove-failure" {
					w.WriteHeader(http.StatusConflict)
					w.Write([]byte(`{"message":"runtime still needed"}`))
				} else {
					w.WriteHeader(http.StatusNoContent)
				}
			})
			defer close()
			p.cfg.OutputBasePath = t.TempDir()
			p.cfg.InstanceID = "installation"
			if err := os.Mkdir(filepath.Join(p.cfg.OutputBasePath, runtimestage.DirName), 0755); err != nil {
				t.Fatal(err)
			}
			c := container.InspectResponse{Config: &container.Config{Labels: map[string]string{resourcelifecycle.InstanceLabel: "installation", crewCrewIDLabel: "crew", stagedEnvLabel: newStagedEnvHandle()}}}
			c.ID = strings.Repeat("a", 64)
			c.Image = "image"
			if os.Geteuid() == 1001 || os.Geteuid() == 1002 {
				if !errors.Is(p.saveStagedEnv(c, nil), errStagedDenied) {
					t.Fatal("unqualified UID accepted")
				}
				t.Skip("positive cleanup fixture needs qualified host UID")
			}
			if err := p.saveStagedEnv(c, []string{"TOKEN=synthetic"}); err != nil {
				t.Fatal(err)
			}
			target, err := p.stagedEnvPath(c)
			if err != nil {
				t.Fatal(err)
			}
			other := c
			other.Config = &container.Config{Labels: map[string]string{
				resourcelifecycle.InstanceLabel: c.Config.Labels[resourcelifecycle.InstanceLabel],
				crewCrewIDLabel:                 c.Config.Labels[crewCrewIDLabel],
				stagedEnvLabel:                  c.Config.Labels[stagedEnvLabel],
			}}
			other.ID = strings.Repeat("b", 64)
			if err := p.saveStagedEnv(other, []string{"OTHER=retained"}); err != nil {
				t.Fatal(err)
			}
			id := c.ID
			outside := filepath.Join(t.TempDir(), filepath.Base(target))
			if err := os.WriteFile(outside, []byte("preserve"), 0600); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "foreign-scope":
				p.stagedEnvScopes.Delete(c.ID)
				foreign := c
				foreign.Config = &container.Config{Labels: map[string]string{resourcelifecycle.InstanceLabel: "foreign", crewCrewIDLabel: "crew", stagedEnvLabel: c.Config.Labels[stagedEnvLabel]}}
				if err := p.registerStagedEnvScope(foreign); !errors.Is(err, errStagedDenied) {
					t.Fatal("foreign cleanup authority registered")
				}
			case "unknown-after-restart", "recovered-scope":
				p.stagedEnvScopes.Delete(c.ID)
				if mode == "recovered-scope" {
					if err := p.registerStagedEnvScope(c); err != nil {
						t.Fatal(err)
					}
				}
			case "alias":
				id = c.ID[:12]
			case "file-symlink":
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, target); err != nil {
					t.Fatal(err)
				}
			case "scope-symlink":
				if err := os.Rename(filepath.Dir(target), filepath.Dir(target)+"-preserved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Dir(outside), filepath.Dir(target)); err != nil {
					t.Fatal(err)
				}
			case "mutated-labels":
				c.Config.Labels[stagedEnvLabel] = newStagedEnvHandle() // Registry snapshot must not alias caller metadata.
			}
			err = p.RemoveCrewRuntime(context.Background(), id)
			fails := mode == "remove-failure" || mode == "scope-symlink" || mode == "file-symlink"
			if (err != nil) != fails {
				t.Fatalf("removal result: %v", err)
			}
			retained := fails || mode == "foreign-scope" || mode == "unknown-after-restart" || mode == "alias"
			_, err = os.Lstat(target)
			if retained && err != nil {
				t.Fatalf("required/untrusted material removed: %v", err)
			}
			if !retained && !os.IsNotExist(err) {
				t.Fatalf("owned material retained: %v", err)
			}
			if calls != 1 {
				t.Fatalf("Docker calls=%d want one delete", calls)
			}
			if raw, err := os.ReadFile(outside); err != nil || string(raw) != "preserve" {
				t.Fatalf("outside modified: %v", err)
			}
			if _, err := p.loadStagedEnv(other); err != nil {
				t.Fatalf("other runtime material modified: %v", err)
			}
		})
	}
}
