package api

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pagebuild"
	"github.com/crewship-ai/crewship/internal/pages"
	pageprofile "github.com/crewship-ai/crewship/tools/pages-build"
)

// Opt-in real compiler through the source/build/preview handlers and their stores.
// All rows and files belong to the test fixture, never a running installation.
func TestPageBuildDockerRoundTrip(t *testing.T) {
	image := os.Getenv("CREWSHIP_TEST_PAGE_BUILD_IMAGE")
	if image == "" {
		// SKIP-WAIVER(#2472): ordinary Go tests need no image; pages-apps CI requires this real Docker round trip.
		t.Skip("requires a pinned local Pages tools image")
	}
	h, _, _, ws, user := newPagesFixture(t)
	h.SetProjectStore(&pages.ProjectStore{Directory: t.TempDir()})
	h.SetBuildWorker(&pagebuild.DockerBuilder{Image: image}, &pagebuild.Store{Directory: t.TempDir()})
	t.Cleanup(func() {
		if !waitForBackgroundWork(160 * time.Second) {
			t.Error("Page build did not drain")
		}
	})
	pagesCreate(t, h, ws, user, "health")
	if w := projectPut(t, h, ws, user, "OWNER", "health", 0, pageprofile.Source()); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := buildRequest(t, h, "POST", "/", ws, user, "OWNER", `{"expected_revision":1}`); w.Code != 202 {
		t.Fatalf("build %d: %s", w.Code, w.Body.String())
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		w := buildRequest(t, h, "GET", "/", ws, user, "OWNER", "")
		if w.Code != 200 {
			t.Fatalf("preview %d: %s", w.Code, w.Body.String())
		}
		var result struct {
			Build    pageBuildRecord     `json:"build"`
			Artifact *pagebuild.Artifact `json:"artifact"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		switch result.Build.State {
		case "ready":
			if result.Artifact == nil || result.Artifact.Toolchain != image {
				t.Fatal("artifact missing or wrong toolchain")
			}
			_, digest, err := result.Artifact.Encode()
			if err != nil || digest != result.Build.ArtifactDigest {
				t.Fatalf("artifact digest mismatch: %v", err)
			}
			return
		case "failed", "interrupted":
			t.Fatalf("build %s: %s", result.Build.State, result.Build.Error)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("build did not complete within 30 seconds")
}
