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

	wantNextTo := filepath.Join(binDir, "crewship-sidecar")           // tar.gz / installer
	wantLibexec := filepath.Clean(filepath.Join(binDir, "..", "libexec", "crewship-sidecar")) // brew
	assertContainsPath(t, got, wantNextTo, "tar.gz sibling")
	assertContainsPath(t, got, wantLibexec, "homebrew libexec")
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
	wantScripts := filepath.Join(cwd, "scripts", "entrypoint.sh")
	assertContainsPath(t, got, wantNextTo, "tar.gz sibling")
	assertContainsPath(t, got, wantLibexec, "homebrew libexec")
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

// TestAutodetectSidecarPaths_ErrorGuidesReleasedInstall pins issue #919: the
// not-found message must guide users who installed a release binary (brew,
// installer, tarball) — not send them to `make build:sidecar`, a target only a
// source checkout has.
func TestAutodetectSidecarPaths_ErrorGuidesReleasedInstall(t *testing.T) {
	t.Setenv("CREWSHIP_SKIP_SIDECAR", "")
	t.Setenv("CREWSHIP_SIDECAR_PATH", "")
	t.Setenv("CREWSHIP_ENTRYPOINT_PATH", "")
	t.Chdir(t.TempDir())

	cfg := Default()
	err := autodetectSidecarPaths(cfg)
	if err == nil {
		t.Fatal("expected error when sidecar cannot be autodetected")
	}
	msg := err.Error()

	for _, want := range []string{
		"brew reinstall", // Homebrew channel
		"install.sh",     // installer channel
		"re-extract",     // tar.gz channel
		"CREWSHIP_SIDECAR_PATH",
		"CREWSHIP_SKIP_SIDECAR",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("sidecar-not-found message missing %q; got:\n%s", want, msg)
		}
	}
	// The old message led with a dead-end Makefile target and mislabelled the
	// skip env as a "test bypass". Neither should headline the new message.
	if strings.Contains(msg, "run 'make build:sidecar'") {
		t.Errorf("message still leads with 'run make build:sidecar' (dead end for released installs):\n%s", msg)
	}
	if strings.Contains(msg, "bypass in tests") {
		t.Errorf("message still calls CREWSHIP_SKIP_SIDECAR a test-only bypass:\n%s", msg)
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
