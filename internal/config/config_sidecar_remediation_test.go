package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSidecarBinaryCandidates_IncludesLibexec pins issue #920: the Homebrew
// formula installs crewship-sidecar into <prefix>/libexec (not global bin),
// so autodetect must probe the libexec sibling of the binary directory in
// addition to the tar.gz layout (companion next to the binary).
func TestSidecarBinaryCandidates_IncludesLibexec(t *testing.T) {
	binDir := "/opt/homebrew/Cellar/crewship/1.2.3/bin"
	got := sidecarBinaryCandidates(binDir)

	wantNextTo := filepath.Join(binDir, "crewship-sidecar")                                           // tar.gz / installer
	wantLibexec := filepath.Clean(filepath.Join(binDir, "..", "libexec", "crewship-sidecar"))         // brew
	wantFHS := filepath.Clean(filepath.Join(binDir, "..", "libexec", "crewship", "crewship-sidecar")) // deb/rpm FHS
	assertContainsPath(t, got, wantNextTo, "tar.gz sibling")
	assertContainsPath(t, got, wantLibexec, "homebrew libexec")
	assertContainsPath(t, got, wantFHS, "FHS package libexec/crewship")
}

// TestEntrypointCandidates_IncludesLibexec pins the entrypoint.sh half of
// issue #920: it moves from global bin to <prefix>/libexec, so autodetect
// must find it there while keeping the source-checkout (scripts/) and
// tar.gz-sibling layouts working.
func TestEntrypointCandidates_IncludesLibexec(t *testing.T) {
	binDir := "/opt/homebrew/Cellar/crewship/1.2.3/bin"
	cwd := "/home/dev/crewship"
	got := entrypointCandidates(binDir, cwd)

	wantNextTo := filepath.Join(binDir, "entrypoint.sh")
	wantLibexec := filepath.Clean(filepath.Join(binDir, "..", "libexec", "entrypoint.sh"))
	wantFHS := filepath.Clean(filepath.Join(binDir, "..", "libexec", "crewship", "entrypoint.sh"))
	wantScripts := filepath.Join(cwd, "scripts", "entrypoint.sh")
	assertContainsPath(t, got, wantNextTo, "tar.gz sibling")
	assertContainsPath(t, got, wantLibexec, "homebrew libexec")
	assertContainsPath(t, got, wantFHS, "FHS package libexec/crewship")
	assertContainsPath(t, got, wantScripts, "source checkout scripts/")
}

// TestAutodetectSidecarPaths_LibexecLayout drives the real autodetect against
// a simulated Homebrew tree: crewship in bin/, companions in libexec/. It must
// resolve both without any explicit CREWSHIP_*_PATH override (issue #920).
func TestAutodetectSidecarPaths_LibexecLayout(t *testing.T) {
	t.Setenv("CREWSHIP_SKIP_SIDECAR", "")
	t.Setenv("CREWSHIP_SIDECAR_PATH", "")
	t.Setenv("CREWSHIP_ENTRYPOINT_PATH", "")

	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	libexec := filepath.Join(root, "libexec")
	for _, d := range []string{binDir, libexec} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sidecar := filepath.Join(libexec, "crewship-sidecar")
	entry := filepath.Join(libexec, "entrypoint.sh")
	for _, p := range []string{sidecar, entry} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	cfg := Default()
	if err := resolveSidecarPaths(cfg, binDir, root); err != nil {
		t.Fatalf("resolveSidecarPaths against a libexec layout: %v", err)
	}
	if cfg.Container.SidecarBinaryPath != sidecar {
		t.Errorf("SidecarBinaryPath = %q, want libexec %q", cfg.Container.SidecarBinaryPath, sidecar)
	}
	if cfg.Container.EntrypointPath != entry {
		t.Errorf("EntrypointPath = %q, want libexec %q", cfg.Container.EntrypointPath, entry)
	}
}

// TestAutodetectSidecarPaths_FHSPackageLayout drives autodetect against the
// deb/rpm layout (#858 phase 4): crewship + crewship-sidecar in /usr/bin,
// entrypoint.sh under /usr/libexec/crewship. `crewship start` must boot from a
// package install without any CREWSHIP_*_PATH override.
func TestAutodetectSidecarPaths_FHSPackageLayout(t *testing.T) {
	t.Setenv("CREWSHIP_SKIP_SIDECAR", "")
	t.Setenv("CREWSHIP_SIDECAR_PATH", "")
	t.Setenv("CREWSHIP_ENTRYPOINT_PATH", "")

	root := t.TempDir() // stands in for /usr
	binDir := filepath.Join(root, "bin")
	libexecPkg := filepath.Join(root, "libexec", "crewship")
	for _, d := range []string{binDir, libexecPkg} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sidecar := filepath.Join(binDir, "crewship-sidecar") // real binary stays in bin
	entry := filepath.Join(libexecPkg, "entrypoint.sh")  // script moved out of bin
	for _, p := range []string{sidecar, entry} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	cfg := Default()
	if err := resolveSidecarPaths(cfg, binDir, root); err != nil {
		t.Fatalf("resolveSidecarPaths against an FHS package layout: %v", err)
	}
	if cfg.Container.SidecarBinaryPath != sidecar {
		t.Errorf("SidecarBinaryPath = %q, want %q", cfg.Container.SidecarBinaryPath, sidecar)
	}
	if cfg.Container.EntrypointPath != entry {
		t.Errorf("EntrypointPath = %q, want FHS libexec %q", cfg.Container.EntrypointPath, entry)
	}
}

// TestSidecarReinstallHint_GuidesReleasedInstall pins issue #919's remediation
// text. It tests the pure sidecarReinstallHint helper directly rather than
// forcing autodetectSidecarPaths to error — the latter probes absolute paths
// like /usr/local/bin/crewship-sidecar, so on a machine that actually has
// crewship installed the "expected an error" assertion is environment-sensitive
// and flaky. The message content is what #919 is about, and it lives in a pure
// function, so assert it there deterministically.
func TestSidecarReinstallHint_GuidesReleasedInstall(t *testing.T) {
	for _, pathEnv := range []string{"CREWSHIP_SIDECAR_PATH", "CREWSHIP_ENTRYPOINT_PATH"} {
		msg := sidecarReinstallHint(pathEnv)
		for _, want := range []string{
			"brew reinstall", // Homebrew channel
			"install.sh",     // installer channel
			"re-extract",     // tar.gz channel
			pathEnv,          // the per-file override escape hatch
			"CREWSHIP_SKIP_SIDECAR",
		} {
			if !strings.Contains(msg, want) {
				t.Errorf("sidecarReinstallHint(%q) missing %q; got:\n%s", pathEnv, want, msg)
			}
		}
		// The old message led with a dead-end Makefile target and mislabelled
		// the skip env as a "test bypass". Neither should headline the new one.
		if strings.Contains(msg, "run 'make build:sidecar'") {
			t.Errorf("hint still leads with 'run make build:sidecar' (dead end for released installs):\n%s", msg)
		}
		if strings.Contains(msg, "bypass in tests") {
			t.Errorf("hint still calls CREWSHIP_SKIP_SIDECAR a test-only bypass:\n%s", msg)
		}
	}
}

// TestResolveSidecarPaths_ErrorUsesReinstallHint confirms the not-found path
// actually wires sidecarReinstallHint. It drives resolveSidecarPaths with
// isolated empty dirs; because the resolver also probes absolute /usr/local
// paths, a host that genuinely has a system-wide sidecar install would resolve
// successfully — so the assertion only fires when the not-found path is
// actually exercised, keeping the test deterministic everywhere.
func TestResolveSidecarPaths_ErrorUsesReinstallHint(t *testing.T) {
	t.Setenv("CREWSHIP_SKIP_SIDECAR", "")
	empty := t.TempDir()
	cfg := Default()
	err := resolveSidecarPaths(cfg, empty, empty)
	if err == nil {
		t.Skip("host has a system-wide sidecar install; not-found path not exercised (message covered by TestSidecarReinstallHint_GuidesReleasedInstall)")
	}
	if !strings.Contains(err.Error(), "brew reinstall") {
		t.Errorf("not-found error should carry the reinstall hint; got: %v", err)
	}
}

func assertContainsPath(t *testing.T, candidates []string, want, label string) {
	t.Helper()
	for _, c := range candidates {
		if filepath.Clean(c) == filepath.Clean(want) {
			return
		}
	}
	t.Errorf("candidates missing %s path %q; got %v", label, want, candidates)
}
