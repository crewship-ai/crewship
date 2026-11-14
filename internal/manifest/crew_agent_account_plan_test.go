package manifest

import (
	"strings"
	"testing"
)

// #5337, legacy crews: manifest path. The body a bare-image manifest builds
// must carry the agent account the server injects at create, and it must
// compare equal to that stored config, so a re-apply plans `=` instead of a
// PATCH that strips the account.
func TestBuildCrewBody_DevcontainerAgentAccount(t *testing.T) {
	const stored = `{"features":{"ghcr.io/devcontainers/features/common-utils:2":{"configureZshAsDefaultShell":"false","installZsh":"false","upgradePackages":"false","userGid":"1001","userUid":"1001","username":"agent"}},"image":"debian:bookworm-slim"}`
	body, err := buildCrewBody("Slim", "slim", &CrewSpec{Devcontainer: &Devcontainer{Image: "debian:bookworm-slim"}})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := body["devcontainer_config"].(string)
	flat := strings.ReplaceAll(got, `"`, "")
	for _, want := range []string{"common-utils:2", "username:agent", "userUid:1001", "userGid:1001"} {
		if !strings.Contains(flat, want) {
			t.Fatalf("body lacks %s: %q", want, got)
		}
	}
	name, img := "Slim", "debian:bookworm-slim"
	existing := &CrewResponse{ID: "c1", Name: name, Slug: "slim", RuntimeImage: &img, DevcontainerConfig: strPtrPlan(stored)}
	if crewBodyDiffers(existing, body) {
		t.Fatalf("a re-apply of the manifest that created the crew must plan no update; body %q vs stored %q", got, stored)
	}
	bare := `{"image":"debian:bookworm-slim"}`
	existing.DevcontainerConfig = &bare
	if !crewBodyDiffers(existing, body) {
		t.Fatal("a crew whose stored config lost the account must be updated")
	}
}

func strPtrPlan(s string) *string { return &s }
