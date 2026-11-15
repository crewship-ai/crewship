package kinds

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// #5337: crew create stores a bare-image devcontainer with common-utils
// injected. The planner compared the bare manifest with that stored form,
// planned an update every time, and the PATCH replaced the injected config,
// so the next build had no `agent` account at UID 1001.
const storedSlimConfig = `{"features":{"ghcr.io/devcontainers/features/common-utils:2":{"configureZshAsDefaultShell":"false","installZsh":"false","upgradePackages":"false","userGid":"1001","userUid":"1001","username":"agent"}},"image":"debian:bookworm-slim"}`

func TestUpdatePatch_DevcontainerAgentAccount(t *testing.T) {
	slim := func(mem int) *CrewDocument {
		return &CrewDocument{
			Metadata: internalapi.Metadata{Name: "n", Slug: "eng"},
			Spec:     CrewSpec{Devcontainer: &Devcontainer{Image: "debian:bookworm-slim", MemoryMB: mem}},
		}
	}
	t.Run("converged bare-image manifest plans nothing", func(t *testing.T) {
		patch, err := slim(0).updatePatch(&CrewRemote{Name: "n", DevcontainerConfig: crewStr(storedSlimConfig)})
		if err != nil {
			t.Fatal(err)
		}
		if len(patch) != 0 {
			t.Fatalf("a re-apply of the manifest that created the crew must not plan an update, got %v", patch)
		}
	})
	t.Run("keys outside the model converge too", func(t *testing.T) {
		stored := strings.Replace(storedSlimConfig, `"image"`, `"hostRequirements":{"memory":"4096mb"},"image"`, 1)
		patch, err := slim(4096).updatePatch(&CrewRemote{Name: "n", DevcontainerConfig: crewStr(stored), ContainerMemoryMB: 4096})
		if err != nil {
			t.Fatal(err)
		}
		if len(patch) != 0 {
			t.Fatalf("want empty patch, got %v", patch)
		}
	})
	t.Run("a crew that lost the account gets it back", func(t *testing.T) {
		patch, err := slim(0).updatePatch(&CrewRemote{Name: "n", DevcontainerConfig: crewStr(`{"image":"debian:bookworm-slim"}`)})
		if err != nil {
			t.Fatal(err)
		}
		got, _ := patch["devcontainer_config"].(string)
		flat := strings.ReplaceAll(got, `"`, "")
		for _, want := range []string{"common-utils:2", "username:agent", "userUid:1001", "userGid:1001"} {
			if !strings.Contains(flat, want) {
				t.Fatalf("patch must restore the agent account (%s), got %q", want, got)
			}
		}
	})
}
