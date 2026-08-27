package apple

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// linuxELFStub is the smallest thing verifySidecarIsLinuxELF accepts, so the
// staging tests can also drive buildCreateArgs without `make build:sidecar`.
var linuxELFStub = []byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 0, 'p', 'a', 'y', 'l', 'o', 'a', 'd'}

// homebrewShapedInstall writes a sidecar and an entrypoint into a directory
// that is deliberately NOT under the crew data dir — the /opt/homebrew shape
// that #1706 was reported against and #1724 is about here.
func homebrewShapedInstall(t *testing.T) (sidecar, entrypoint string, sidecarBytes, entrypointBytes []byte) {
	t.Helper()
	prefix := t.TempDir()
	sidecar = filepath.Join(prefix, "crewship-sidecar")
	entrypoint = filepath.Join(prefix, "entrypoint.sh")
	sidecarBytes = linuxELFStub
	entrypointBytes = []byte("#!/bin/bash\nexec sleep infinity\n")
	if err := os.WriteFile(sidecar, sidecarBytes, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entrypoint, entrypointBytes, 0o755); err != nil {
		t.Fatal(err)
	}
	return sidecar, entrypoint, sidecarBytes, entrypointBytes
}

// TestStageRuntimeArtifactsMovesBindSourcesUnderTheDataDir is #1724's core
// claim, and the apple twin of the docker test of the same name: after staging,
// the two mandatory bind sources are inside the same host subtree as the crew
// data dirs — the subtree the runtime already has to be able to see — and carry
// the same bytes as the install-location originals.
func TestStageRuntimeArtifactsMovesBindSourcesUnderTheDataDir(t *testing.T) {
	sidecarSrc, entrypointSrc, sidecarBytes, entrypointBytes := homebrewShapedInstall(t)

	dataDir := t.TempDir()
	got := stageRuntimeArtifacts(Config{
		OutputBasePath:    dataDir,
		SidecarBinaryPath: sidecarSrc,
		EntrypointPath:    entrypointSrc,
	}, quietLogger())

	for _, tc := range []struct {
		name string
		path string
		want []byte
	}{
		{"sidecar", got.SidecarBinaryPath, sidecarBytes},
		{"entrypoint", got.EntrypointPath, entrypointBytes},
	} {
		rel, err := filepath.Rel(dataDir, tc.path)
		if err != nil || strings.HasPrefix(rel, "..") {
			t.Errorf("%s bind source %q is not under the crew data dir %q — an Apple Containers VM that shares only the data dir still cannot see it (#1724)",
				tc.name, tc.path, dataDir)
			continue
		}
		content, err := os.ReadFile(tc.path)
		if err != nil {
			t.Errorf("%s staged copy unreadable: %v", tc.name, err)
			continue
		}
		if string(content) != string(tc.want) {
			t.Errorf("%s staged copy has content %q, want %q", tc.name, content, tc.want)
		}
	}
}

// TestStagedPathsReachTheCreateArgVector is the half a Config-level test cannot
// reach: buildCreateArgs is what actually renders `-v <host>:<target>:ro`, and
// staging is worth nothing if the rendered vector still names the install path.
func TestStagedPathsReachTheCreateArgVector(t *testing.T) {
	sidecarSrc, entrypointSrc, _, _ := homebrewShapedInstall(t)
	dataDir := t.TempDir()
	cfg := stageRuntimeArtifacts(Config{
		OutputBasePath:    dataDir,
		SidecarBinaryPath: sidecarSrc,
		EntrypointPath:    entrypointSrc,
	}, quietLogger())

	args, err := buildCreateArgs(createArgsInput{
		containerName:  "crewship-crew-x",
		image:          "docker.io/library/debian:bookworm-slim",
		cpus:           2,
		memoryMB:       1024,
		crewID:         "crew1",
		workspacePath:  filepath.Join(dataDir, "workspaces", "crew1"),
		outputPath:     filepath.Join(dataDir, "crew1"),
		crewPath:       filepath.Join(dataDir, "crews", "crew1"),
		sidecarPath:    cfg.SidecarBinaryPath,
		entrypointPath: cfg.EntrypointPath,
	})
	if err != nil {
		t.Fatalf("buildCreateArgs: %v", err)
	}

	binds := flagValues(args, "-v")
	for _, want := range []string{
		filepath.Join(dataDir, ".runtime", "crewship-sidecar") + ":" + sidecarTargetPath + ":ro",
		filepath.Join(dataDir, ".runtime", "entrypoint.sh") + ":" + entrypointTargetPath + ":ro",
	} {
		if !slices.Contains(binds, want) {
			t.Errorf("create arg vector is missing the staged bind %q\ngot -v: %v\nthe crew would bind from the install prefix, which an Apple Containers VM need not share (#1724)", want, binds)
		}
	}
	for _, bind := range binds {
		host, _, _ := strings.Cut(bind, ":")
		if rel, err := filepath.Rel(dataDir, host); err != nil || strings.HasPrefix(rel, "..") {
			t.Errorf("bind source %q is outside the crew data dir %q; every mandatory bind must live in the one subtree the runtime already has to share", host, dataDir)
		}
	}
}

// TestStageRuntimeArtifactsPreservesMtime pins the interaction with #1390's
// staleness check: the docker provider's assertSidecarFreshAtStartup decides a
// sidecar is stale by comparing its mtime against the server binary's, so a
// staged copy stamped "now" would report every deploy as fresh forever. Apple
// shares the staging implementation, so it has to keep the same promise —
// otherwise the shared helper stops being safe to share.
func TestStageRuntimeArtifactsPreservesMtime(t *testing.T) {
	installPrefix := t.TempDir()
	src := filepath.Join(installPrefix, "crewship-sidecar")
	if err := os.WriteFile(src, linuxELFStub, 0o755); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-30 * 24 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(src, old, old); err != nil {
		t.Fatal(err)
	}

	got := stageRuntimeArtifacts(Config{OutputBasePath: t.TempDir(), SidecarBinaryPath: src}, quietLogger())
	info, err := os.Stat(got.SidecarBinaryPath)
	if err != nil {
		t.Fatalf("stat staged sidecar: %v", err)
	}
	if !info.ModTime().Truncate(time.Second).Equal(old) {
		t.Errorf("staged sidecar mtime is %v, want the source's %v — a refreshed mtime silences the #1390 stale-sidecar check on every boot",
			info.ModTime(), old)
	}
}

// TestStageRuntimeArtifactsIsIdempotent covers the restart case: the second
// boot stages the already-staged file, which must not truncate it to zero.
func TestStageRuntimeArtifactsIsIdempotent(t *testing.T) {
	installPrefix := t.TempDir()
	src := filepath.Join(installPrefix, "crewship-sidecar")
	if err := os.WriteFile(src, linuxELFStub, 0o755); err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()

	first := stageRuntimeArtifacts(Config{OutputBasePath: dataDir, SidecarBinaryPath: src}, quietLogger())
	// Feed the staged path back in, exactly as an operator pinning
	// CREWSHIP_SIDECAR_PATH at it would.
	second := stageRuntimeArtifacts(Config{OutputBasePath: dataDir, SidecarBinaryPath: first.SidecarBinaryPath}, quietLogger())

	content, err := os.ReadFile(second.SidecarBinaryPath)
	if err != nil {
		t.Fatalf("read re-staged sidecar: %v", err)
	}
	if string(content) != string(linuxELFStub) {
		t.Errorf("re-staging produced %d bytes, want the original %d — staging a file onto itself truncated it", len(content), len(linuxELFStub))
	}
}

// TestStageRuntimeArtifactsRestagesAChangedArtifact is the half of the
// up-to-date check that can actually hurt: skipping a re-copy when the install
// HAS changed would leave every crew container bind-mounting the previous
// deploy's sidecar forever.
func TestStageRuntimeArtifactsRestagesAChangedArtifact(t *testing.T) {
	installPrefix := t.TempDir()
	src := filepath.Join(installPrefix, "crewship-sidecar")
	if err := os.WriteFile(src, []byte{0x7f, 'E', 'L', 'F', 1}, 0o755); err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	first := stageRuntimeArtifacts(Config{OutputBasePath: dataDir, SidecarBinaryPath: src}, quietLogger())

	// A new deploy: different bytes, different length, newer mtime.
	updated := []byte{0x7f, 'E', 'L', 'F', 2, 2, 2, 2}
	if err := os.WriteFile(src, updated, 0o755); err != nil {
		t.Fatal(err)
	}
	second := stageRuntimeArtifacts(Config{OutputBasePath: dataDir, SidecarBinaryPath: src}, quietLogger())
	if second.SidecarBinaryPath != first.SidecarBinaryPath {
		t.Fatalf("staged path moved between boots: %q then %q", first.SidecarBinaryPath, second.SidecarBinaryPath)
	}
	got, err := os.ReadFile(second.SidecarBinaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(updated) {
		t.Errorf("staged copy still holds %v, want the redeployed %v — crews would bind the previous deploy's sidecar", got, updated)
	}
}

// TestStageRuntimeArtifactsSkipsAnUnchangedArtifact: the same bytes at the same
// mtime must not be re-copied, so a restart does not swap the inode under every
// running crew's bind mount for nothing.
func TestStageRuntimeArtifactsSkipsAnUnchangedArtifact(t *testing.T) {
	installPrefix := t.TempDir()
	src := filepath.Join(installPrefix, "crewship-sidecar")
	if err := os.WriteFile(src, []byte{0x7f, 'E', 'L', 'F', 3}, 0o755); err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	first := stageRuntimeArtifacts(Config{OutputBasePath: dataDir, SidecarBinaryPath: src}, quietLogger())
	before, err := os.Stat(first.SidecarBinaryPath)
	if err != nil {
		t.Fatal(err)
	}

	stageRuntimeArtifacts(Config{OutputBasePath: dataDir, SidecarBinaryPath: src}, quietLogger())
	after, err := os.Stat(first.SidecarBinaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Error("an unchanged artifact was re-staged; the rename replaced the inode every running crew is bind-mounting")
	}
}

// TestStageRuntimeArtifactsWithoutDataDirLeavesConfigAlone: a provider built
// without a data dir (tests, embeddings) must keep its configured paths.
func TestStageRuntimeArtifactsWithoutDataDirLeavesConfigAlone(t *testing.T) {
	in := Config{SidecarBinaryPath: "/opt/homebrew/libexec/crewship-sidecar", EntrypointPath: "/opt/homebrew/libexec/entrypoint.sh"}
	got := stageRuntimeArtifacts(in, quietLogger())
	if got.SidecarBinaryPath != in.SidecarBinaryPath || got.EntrypointPath != in.EntrypointPath {
		t.Errorf("staging rewrote paths with no OutputBasePath: got %+v want %+v", got, in)
	}
}

// TestStageRuntimeArtifactsKeepsInstallPathsWhenTheCopyFails holds the
// best-effort contract: staging that cannot write must degrade to today's
// behaviour rather than blank the two MANDATORY paths, which buildCreateArgs
// refuses to render without.
func TestStageRuntimeArtifactsKeepsInstallPathsWhenTheCopyFails(t *testing.T) {
	sidecarSrc, entrypointSrc, _, _ := homebrewShapedInstall(t)
	// A data dir whose .runtime cannot be created: the name is already taken by
	// a regular file.
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, ".runtime"), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := stageRuntimeArtifacts(Config{
		OutputBasePath:    dataDir,
		SidecarBinaryPath: sidecarSrc,
		EntrypointPath:    entrypointSrc,
	}, quietLogger())
	if got.SidecarBinaryPath != sidecarSrc || got.EntrypointPath != entrypointSrc {
		t.Errorf("a failed staging changed the bind paths: got sidecar=%q entrypoint=%q, want the install paths %q and %q",
			got.SidecarBinaryPath, got.EntrypointPath, sidecarSrc, entrypointSrc)
	}
}
