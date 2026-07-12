package docker

// Startup scrub of legacy host-side secrets dirs: before /secrets became a
// tmpfs, cleartext credential files persisted under
// OutputBasePath/secrets/<crew-id>. The per-crew removal in EnsureCrewRuntime
// only reaches crews that get re-provisioned — deleted/dormant crews keep
// their cleartext forever without this sweep. Dirs still bind-mounted into an
// existing (legacy) container must be skipped: yanking a live mount source
// breaks running agents.

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
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

	removed, skipped, failed := sweepLegacySecretsBase(base, map[string]bool{"crew-live": true}, sweepTestLogger())

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
		filepath.Join(t.TempDir(), "does-not-exist"), nil, sweepTestLogger())
	if removed != 0 || skipped != 0 || failed != 0 {
		t.Fatalf("missing base must be a no-op, got %d/%d/%d", removed, skipped, failed)
	}
}
