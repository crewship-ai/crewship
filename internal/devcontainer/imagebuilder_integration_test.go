//go:build integration

package devcontainer

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestBuildKitProvision_RealWorld exercises the full BuildKit feature path
// against a real Docker daemon and real OCI features from ghcr.io: download →
// SortFeatures → GenerateDockerfile → stageBuildContext → docker build, then
// runs the resulting image and asserts the agent user (UID 1001) exists and a
// dependent tool installed. This is the exact regression that motivated the
// work (claude-code/github-cli failing because the agent user didn't exist),
// now validated through the BuildKit pipeline.
//
// Run with: go test -tags integration -run TestBuildKitProvision_RealWorld ./internal/devcontainer/
func TestBuildKitProvision_RealWorld(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}

	const baseImage = "mcr.microsoft.com/devcontainers/base:bookworm"
	const commonUtils = "ghcr.io/devcontainers/features/common-utils:2"
	const githubCLI = "ghcr.io/devcontainers/features/github-cli:1"

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	dl := NewFeatureDownloader(t.TempDir(), testLogger())

	// github-cli intentionally listed first to prove ordering is corrected:
	// SortFeatures must put common-utils first (it creates the agent user that
	// github-cli's install.sh relies on).
	cfg := map[string]map[string]any{
		githubCLI: nil,
		commonUtils: {
			"username": "agent",
			"uid":      "1001",
			"gid":      "1001",
		},
	}

	var features []*ResolvedFeature
	for ref, opts := range cfg {
		f, err := dl.Download(ctx, ref, opts)
		if err != nil {
			t.Fatalf("download %s: %v", ref, err)
		}
		features = append(features, f)
	}
	sorted := SortFeatures(features)
	if sorted[0].Metadata.ID != "common-utils" {
		t.Fatalf("common-utils must sort first, got %s", featureIDs(sorted))
	}

	ctxDir, _, tag, err := stageBuildContext(baseImage, sorted, cfg)
	if err != nil {
		t.Fatalf("stageBuildContext: %v", err)
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "rmi", "-f", tag).Run()
	})

	builder := NewDockerBuildKitBuilder(testLogger())
	if !builder.Available() {
		t.Skip("no docker CLI for BuildKit")
	}
	var logs strings.Builder
	if err := builder.Build(ctx, ctxDir, tag, func(line string) { logs.WriteString(line + "\n") }); err != nil {
		t.Fatalf("BuildKit build failed: %v\n--- build log ---\n%s", err, logs.String())
	}

	// Agent user must exist (UID 1001).
	out, err := exec.Command("docker", "run", "--rm", "--entrypoint", "sh", tag, "-c", "id -u agent").CombinedOutput()
	if err != nil {
		t.Fatalf("id agent failed (user not created): %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "1001" {
		t.Fatalf("agent uid = %q, want 1001", got)
	}

	// Dependent tool (gh) must be installed — proves github-cli installed after
	// common-utils successfully.
	out, err = exec.Command("docker", "run", "--rm", "--entrypoint", "sh", tag, "-c", "command -v gh").CombinedOutput()
	if err != nil {
		t.Fatalf("gh not installed: %v\n%s", err, out)
	}
	t.Logf("agent uid=1001 ✓, gh at %s", strings.TrimSpace(string(out)))
}
