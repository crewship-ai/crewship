package docker

// Startup scrub of legacy host-side secrets dirs: before /secrets became a
// tmpfs, cleartext credential files persisted under
// OutputBasePath/secrets/<crew-id>. The per-crew removal in EnsureCrewRuntime
// only reaches crews that get re-provisioned — deleted/dormant crews keep
// their cleartext forever without this sweep. Dirs still bind-mounted into an
// existing (legacy) container must be skipped: yanking a live mount source
// breaks running agents.

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
)

func sweepTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestProtectedLegacySecretsDirs(t *testing.T) {
	base := filepath.Join(string(filepath.Separator)+"var", "lib", "crewship", "secrets")
	containers := []container.Summary{
		{ // legacy container still bind-mounting its secrets dir
			Mounts: []container.MountPoint{
				{Type: mount.TypeBind, Source: filepath.Join(base, "crew-live"), Destination: "/secrets"},
			},
		},
		{ // nested source (e.g. /secrets/shared bound separately) protects the crew too
			Mounts: []container.MountPoint{
				{Type: mount.TypeBind, Source: filepath.Join(base, "crew-nested", "shared"), Destination: "/secrets/shared"},
			},
		},
		{ // new-style container: tmpfs /secrets must protect nothing
			Mounts: []container.MountPoint{
				{Type: mount.TypeTmpfs, Destination: "/secrets"},
			},
		},
		{ // unrelated bind mounts must protect nothing
			Mounts: []container.MountPoint{
				{Type: mount.TypeBind, Source: "/var/lib/crewship/crews/crew-x", Destination: "/crew"},
				{Type: mount.TypeBind, Source: "/etc/localtime", Destination: "/etc/localtime"},
			},
		},
	}

	protected := protectedLegacySecretsDirs(containers, base)
	want := map[string]bool{"crew-live": true, "crew-nested": true}
	if len(protected) != len(want) {
		t.Fatalf("protected = %v, want %v", protected, want)
	}
	for id := range want {
		if !protected[id] {
			t.Errorf("expected %q to be protected", id)
		}
	}
}

func TestSweepLegacySecretsBase(t *testing.T) {
	base := filepath.Join(t.TempDir(), "secrets")
	mk := func(crewID string) {
		dir := filepath.Join(base, crewID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "GH_TOKEN"), []byte("cleartext"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mk("crew-live")
	mk("crew-dormant")
	mk("crew-deleted")

	removed, skipped, failed := sweepLegacySecretsBase(base, map[string]bool{"crew-live": true}, sweepTestLogger(), os.RemoveAll, nil)

	if removed != 2 || skipped != 1 || failed != 0 {
		t.Fatalf("removed/skipped/failed = %d/%d/%d, want 2/1/0", removed, skipped, failed)
	}
	if _, err := os.Stat(filepath.Join(base, "crew-live", "GH_TOKEN")); err != nil {
		t.Error("still-mounted crew dir must survive the sweep")
	}
	for _, gone := range []string{"crew-dormant", "crew-deleted"} {
		if _, err := os.Stat(filepath.Join(base, gone)); !os.IsNotExist(err) {
			t.Errorf("%s should have been removed (err=%v)", gone, err)
		}
	}
}

func TestSweepLegacySecretsBase_MissingBaseIsNoop(t *testing.T) {
	removed, skipped, failed := sweepLegacySecretsBase(
		filepath.Join(t.TempDir(), "does-not-exist"), nil, sweepTestLogger(), os.RemoveAll, nil)
	if removed != 0 || skipped != 0 || failed != 0 {
		t.Fatalf("missing base must be a no-op, got %d/%d/%d", removed, skipped, failed)
	}
}

// TestSweepLegacySecretsBase_HelperContainerRecoversPermissionDeniedDir covers
// GitHub #1069: a dormant per-agent dir created by the container as UID 1001
// mode 0700 cannot be entered by the crewship server process (different UID,
// no CAP_DAC_OVERRIDE), so a plain os.RemoveAll fails with permission denied.
// The sweep must fall back to a UID-1001 helper-container removal path (see
// removeLegacySecretsDirViaHelper) and, once that succeeds, retry the plain
// removal to finish off the now-empty directory skeleton — and count the dir
// as removed.
//
// The permission-denied condition itself is simulated via an injected
// removeAll func rather than real chmod bits: a real 0700-dir-owned-by-
// another-uid scenario can't be reproduced portably in CI (tests commonly
// run as root, which bypasses all permission checks).
func TestSweepLegacySecretsBase_HelperContainerRecoversPermissionDeniedDir(t *testing.T) {
	base := filepath.Join(t.TempDir(), "secrets")
	if err := os.MkdirAll(filepath.Join(base, "crew-stuck"), 0o755); err != nil {
		t.Fatal(err)
	}

	var removeAllCalls []string
	firstAttemptDone := false
	fakeRemoveAll := func(path string) error {
		removeAllCalls = append(removeAllCalls, path)
		if filepath.Base(path) == "crew-stuck" && !firstAttemptDone {
			firstAttemptDone = true
			return fmt.Errorf("remove %s: permission denied", path)
		}
		return os.RemoveAll(path)
	}

	var helperCalls []string
	fakeHelper := func(path string) error {
		helperCalls = append(helperCalls, path)
		return nil // simulate a successful UID-1001 helper container run
	}

	removed, skipped, failed := sweepLegacySecretsBase(base, nil, sweepTestLogger(), fakeRemoveAll, fakeHelper)

	if removed != 1 || skipped != 0 || failed != 0 {
		t.Fatalf("removed/skipped/failed = %d/%d/%d, want 1/0/0", removed, skipped, failed)
	}
	if len(helperCalls) != 1 || helperCalls[0] != filepath.Join(base, "crew-stuck") {
		t.Fatalf("expected helper to be invoked once for crew-stuck, got %v", helperCalls)
	}
	if len(removeAllCalls) != 2 {
		t.Fatalf("expected 2 removeAll attempts (initial fail + post-helper retry), got %v", removeAllCalls)
	}
	if _, err := os.Stat(filepath.Join(base, "crew-stuck")); !os.IsNotExist(err) {
		t.Errorf("crew-stuck should have been removed after the helper ran (err=%v)", err)
	}
}

// TestSweepLegacySecretsBase_HelperContainerFailureCountsAsFailed covers the
// fallback: when even the UID-1001 helper container can't remove the
// dormant dir (e.g. Docker create/start/wait error), the sweep must WARN
// loudly and count the dir as failed — never crash and never lose the
// "cleartext may remain on disk" signal.
func TestSweepLegacySecretsBase_HelperContainerFailureCountsAsFailed(t *testing.T) {
	base := filepath.Join(t.TempDir(), "secrets")
	if err := os.MkdirAll(filepath.Join(base, "crew-stuck"), 0o755); err != nil {
		t.Fatal(err)
	}

	fakeRemoveAll := func(path string) error {
		return fmt.Errorf("remove %s: permission denied", path)
	}
	fakeHelper := func(path string) error {
		return fmt.Errorf("create removal helper: no such image")
	}

	removed, skipped, failed := sweepLegacySecretsBase(base, nil, sweepTestLogger(), fakeRemoveAll, fakeHelper)

	if failed != 1 || removed != 0 || skipped != 0 {
		t.Fatalf("removed/skipped/failed = %d/%d/%d, want 0/0/1", removed, skipped, failed)
	}
	if _, err := os.Stat(filepath.Join(base, "crew-stuck")); err != nil {
		t.Errorf("crew-stuck dir should still exist on disk after a failed helper (err=%v)", err)
	}
}

// TestRemoveLegacySecretsDirViaHelper_BindMountsAndRunsAsUID1001 exercises the
// real Docker API call sequence removeLegacySecretsDirViaHelper makes,
// against the same fake Docker daemon harness legacy_migration_test.go uses
// for the copy-helper (migrateLegacyCrewResources). Confirms the helper
// container is created with the security posture #1069 requires: runs as
// UID 1001 (the dirs' actual owner, mirroring writeCredentialFiles), no
// network, CapDrop ALL, and bind-mounts exactly the dormant dir at /legacy —
// then is torn down (ContainerRemove) regardless of outcome.
func TestRemoveLegacySecretsDirViaHelper_BindMountsAndRunsAsUID1001(t *testing.T) {
	f := &fakeDaemon{waitExit: 0}
	p, cleanup := newFakeDockerProvider(t, f.handler(t))
	defer cleanup()
	p.cfg.RuntimeImage = "crewship/agent-runtime:latest"

	dirPath := filepath.Join(t.TempDir(), "secrets", "crew-stuck")

	if err := p.removeLegacySecretsDirViaHelper(context.Background(), dirPath); err != nil {
		t.Fatalf("removeLegacySecretsDirViaHelper: %v", err)
	}

	if f.helperCreates != 1 {
		t.Fatalf("expected 1 helper container created, got %d", f.helperCreates)
	}
	if !contains(f.removedContainers, "helper-1") {
		t.Errorf("expected helper container to be removed after running, got %v", f.removedContainers)
	}
	if img, _ := f.lastCreateBody["Image"].(string); img != p.cfg.RuntimeImage {
		t.Errorf("helper Image = %q, want %q (reuses the configured runtime image, no bespoke image)", img, p.cfg.RuntimeImage)
	}
	if user, _ := f.lastCreateBody["User"].(string); user != "1001:1001" {
		t.Errorf("helper User = %q, want 1001:1001 (must own the dormant dir's contents)", user)
	}
	hc, ok := f.lastCreateBody["HostConfig"].(map[string]any)
	if !ok {
		t.Fatalf("create body missing HostConfig: %v", f.lastCreateBody)
	}
	if mode, _ := hc["NetworkMode"].(string); mode != "none" {
		t.Errorf("helper NetworkMode = %q, want none", mode)
	}
	if caps, ok := hc["CapDrop"].([]any); !ok || len(caps) != 1 || caps[0] != "ALL" {
		t.Errorf("helper CapDrop = %v, want [ALL]", hc["CapDrop"])
	}
	mounts := mountsFromCreateBody(t, f.lastCreateBody)
	if len(mounts) != 1 {
		t.Fatalf("expected exactly 1 mount, got %v", mounts)
	}
	if src, _ := mounts[0]["Source"].(string); src != dirPath {
		t.Errorf("helper mount Source = %q, want %q", src, dirPath)
	}
	if tgt, _ := mounts[0]["Target"].(string); tgt != "/legacy" {
		t.Errorf("helper mount Target = %q, want /legacy", tgt)
	}
}

// A non-zero helper exit is the EXPECTED common case (see doc comment on
// removeLegacySecretsDirViaHelper) and must not be surfaced as an error —
// only a Docker-level failure (create/start/wait) should be.
func TestRemoveLegacySecretsDirViaHelper_NonZeroExitIsNotAnError(t *testing.T) {
	f := &fakeDaemon{waitExit: 1}
	p, cleanup := newFakeDockerProvider(t, f.handler(t))
	defer cleanup()
	p.cfg.RuntimeImage = "crewship/agent-runtime:latest"

	if err := p.removeLegacySecretsDirViaHelper(context.Background(), filepath.Join(t.TempDir(), "crew-stuck")); err != nil {
		t.Fatalf("removeLegacySecretsDirViaHelper should ignore the helper's exit code, got: %v", err)
	}
}

func TestRemoveLegacySecretsDirViaHelper_NoRuntimeImageConfigured(t *testing.T) {
	f := &fakeDaemon{}
	p, cleanup := newFakeDockerProvider(t, f.handler(t))
	defer cleanup()
	// p.cfg.RuntimeImage left empty.

	err := p.removeLegacySecretsDirViaHelper(context.Background(), filepath.Join(t.TempDir(), "crew-stuck"))
	if err == nil {
		t.Fatal("expected an error when no runtime image is configured")
	}
	if f.helperCreates != 0 {
		t.Errorf("must not attempt ContainerCreate without a runtime image, got %d", f.helperCreates)
	}
}

func TestRemoveLegacySecretsDirViaHelper_CreateFailurePropagates(t *testing.T) {
	f := &fakeDaemon{createFails: true}
	p, cleanup := newFakeDockerProvider(t, f.handler(t))
	defer cleanup()
	p.cfg.RuntimeImage = "crewship/agent-runtime:latest"

	err := p.removeLegacySecretsDirViaHelper(context.Background(), filepath.Join(t.TempDir(), "crew-stuck"))
	if err == nil {
		t.Fatal("expected an error when the helper container fails to create")
	}
	if !strings.Contains(err.Error(), "create removal helper") {
		t.Errorf("error should mention 'create removal helper': %v", err)
	}
}
