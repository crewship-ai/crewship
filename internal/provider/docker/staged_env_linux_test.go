//go:build linux

package docker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider/runtimestage"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
	"github.com/moby/moby/api/types/container"
)

func TestStagedEnvironmentScopeAndConfidentialMaterial(t *testing.T) {
	p := &Provider{cfg: Config{OutputBasePath: t.TempDir(), InstanceID: "installation"}}
	if e := os.Mkdir(filepath.Join(p.cfg.OutputBasePath, runtimestage.DirName), 0755); e != nil {
		t.Fatal(e)
	}
	c := container.InspectResponse{Config: &container.Config{Labels: map[string]string{resourcelifecycle.InstanceLabel: p.cfg.InstanceID, crewCrewIDLabel: "crew", stagedEnvLabel: newStagedEnvHandle()}}}
	c.ID = "container"
	c.Image = "image"
	original := []string{"TOKEN=confidential", "PATH=/usr/bin:/bin"}
	if os.Geteuid() == 1001 || os.Geteuid() == 1002 {
		if e := p.saveStagedEnv(c, original); e == nil {
			t.Fatal("unqualified server UID accepted private material")
		}
		t.Log("unqualified server UID: admission denial verified")
		return
	}

	if e := p.saveStagedEnv(c, original); e != nil {
		t.Fatal(e)
	}
	target, e := p.stagedEnvPath(c)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(target)
	if e != nil {
		t.Fatal(e)
	}
	for _, scenario := range []string{"valid", "wrong-container", "wrong-image", "wrong-crew", "wrong-installation", "invalid-handle", "missing", "corrupt", "permissions", "symlink"} {
		t.Run(scenario, func(t *testing.T) {
			copy := c
			copy.Config = &container.Config{Labels: map[string]string{resourcelifecycle.InstanceLabel: p.cfg.InstanceID, crewCrewIDLabel: "crew", stagedEnvLabel: c.Config.Labels[stagedEnvLabel]}}
			switch scenario {
			case "wrong-container":
				copy.ID = "other"
			case "wrong-image":
				copy.Image = "other"
			case "wrong-crew":
				copy.Config.Labels[crewCrewIDLabel] = "other"
			case "wrong-installation":
				copy.Config.Labels[resourcelifecycle.InstanceLabel] = "other"
			case "invalid-handle":
				copy.Config.Labels[stagedEnvLabel] = "../../outside"
			case "missing":
				os.Remove(target)
			case "corrupt":
				os.WriteFile(target, []byte("broken"), 0600)
			case "permissions":
				os.Chmod(target, 0644)
			case "symlink":
				os.Remove(target)
				os.Symlink("outside", target)
			}
			env, e := p.loadStagedEnv(copy)
			if scenario == "valid" {
				if e != nil || strings.Join(env, "\n") != strings.Join(original, "\n") {
					t.Fatalf("valid material: %v", e)
				}
			} else if e == nil {
				t.Fatal("invalid runtime material admitted")
			}
			if e != nil && strings.Contains(e.Error(), "confidential") {
				t.Fatal("secret-bearing diagnostic")
			}
			os.Remove(target)
			if e := os.WriteFile(target, raw, 0600); e != nil {
				t.Fatal(e)
			}
		})
	}
}
