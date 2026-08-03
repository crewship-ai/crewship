package docker

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/mount"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestStageRuntimeArtifactsMovesBindSourcesUnderTheDataDir is #1706's core
// claim: after staging, the two mandatory bind sources are inside the same host
// subtree as the crew data dirs — the subtree the daemon already has to be able
// to see — and carry the same bytes as the install-location originals.
func TestStageRuntimeArtifactsMovesBindSourcesUnderTheDataDir(t *testing.T) {
	// The install prefix a Homebrew install lands in: outside $HOME, and so
	// outside a default Colima's share set.
	installPrefix := t.TempDir()
	sidecarSrc := filepath.Join(installPrefix, "crewship-sidecar")
	entrypointSrc := filepath.Join(installPrefix, "entrypoint.sh")
	sidecarBytes := []byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 0, 'p', 'a', 'y', 'l', 'o', 'a', 'd'}
	entrypointBytes := []byte("#!/bin/bash\nexec sleep infinity\n")
	if err := os.WriteFile(sidecarSrc, sidecarBytes, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entrypointSrc, entrypointBytes, 0o755); err != nil {
		t.Fatal(err)
	}

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
			t.Errorf("%s bind source %q is not under the crew data dir %q — a VM-backed daemon that shares only the data dir still cannot see it (#1706)",
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

// TestStageRuntimeArtifactsPreservesMtime pins the interaction with #1390's
// staleness check: assertSidecarFreshAtStartup decides a sidecar is stale by
// comparing its mtime against the server binary's, so a staged copy stamped
// "now" would report every deploy as fresh forever.
func TestStageRuntimeArtifactsPreservesMtime(t *testing.T) {
	installPrefix := t.TempDir()
	src := filepath.Join(installPrefix, "crewship-sidecar")
	if err := os.WriteFile(src, []byte{0x7f, 'E', 'L', 'F'}, 0o755); err != nil {
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
	payload := []byte{0x7f, 'E', 'L', 'F', 9, 9, 9}
	if err := os.WriteFile(src, payload, 0o755); err != nil {
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
	if string(content) != string(payload) {
		t.Errorf("re-staging produced %d bytes, want the original %d — staging a file onto itself truncated it", len(content), len(payload))
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

func TestUnreachableBindSourcePath(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{
			"moby",
			errors.New(`Error response from daemon: invalid mount config for type "bind": bind source path does not exist: /opt/homebrew/Cellar/crewship/1.0/libexec/crewship-sidecar`),
			"/opt/homebrew/Cellar/crewship/1.0/libexec/crewship-sidecar",
		},
		{
			"podman",
			errors.New(`statfs /usr/local/bin/crewship-sidecar: no such file or directory`),
			"/usr/local/bin/crewship-sidecar",
		},
		{
			// Caught live on Colima: the repo sits on a volume whose name has
			// spaces, and a whitespace-terminated capture truncated the path to
			// "/Volumes/SSD" — which does not exist, so explainBindFailure
			// concluded "genuinely missing" and passed the unhelpful daemon
			// error straight through.
			"path with spaces",
			errors.New(`Error response from daemon: invalid mount config for type "bind": bind source path does not exist: /Volumes/SSD 990 PRO/Development/crewship_1/crewship-sidecar`),
			"/Volumes/SSD 990 PRO/Development/crewship_1/crewship-sidecar",
		},
		{"unrelated", errors.New("No such image: debian:bookworm-slim"), ""},
		{"nil", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := unreachableBindSourcePath(tc.err); got != tc.want {
				t.Errorf("unreachableBindSourcePath = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestExplainBindFailureNamesTheShareProblem is the "if you cannot make it
// work everywhere, make it say exactly what is wrong" half of #1706. The
// message has to carry the runtime, the path, and a remedy that adds it to the
// VM's share set — an operator reading only this error must not go looking for
// a missing file.
func TestExplainBindFailureNamesTheShareProblem(t *testing.T) {
	// A path that exists HERE — that is the whole discriminator.
	existing := filepath.Join(t.TempDir(), "crewship-sidecar")
	if err := os.WriteFile(existing, []byte{0x7f}, 0o755); err != nil {
		t.Fatal(err)
	}
	p := &Provider{
		logger:   quietLogger(),
		detected: DetectResult{Runtime: "colima", Socket: "/Users/x/.colima/default/docker.sock"},
		cfg:      Config{OutputBasePath: "/Users/x/.crewship/output"},
	}
	got := p.explainBindFailure(errors.New(`invalid mount config for type "bind": bind source path does not exist: ` + existing)).Error()

	for _, want := range []string{
		"colima",                    // which runtime
		existing,                    // which path
		"colima start --mount",      // the remedy that actually fixes it
		"/Users/x/.crewship/output", // where Crewship stages instead
		"exists on this machine",    // rules out "the file is missing"
	} {
		if !strings.Contains(got, want) {
			t.Errorf("explained error is missing %q\ngot: %s", want, got)
		}
	}
}

// TestExplainBindFailureLeavesAGenuinelyMissingFileAlone: when the path is
// absent on this host too, the daemon's own error is already right and must
// not be replaced with a lecture about VM shares.
func TestExplainBindFailureLeavesAGenuinelyMissingFileAlone(t *testing.T) {
	p := &Provider{logger: quietLogger(), detected: DetectResult{Runtime: "colima"}}
	orig := errors.New(`bind source path does not exist: /definitely/not/here/crewship-sidecar`)
	if got := p.explainBindFailure(orig); got.Error() != orig.Error() {
		t.Errorf("a genuinely missing file was re-explained as a VM share problem:\n%s", got)
	}
}

// TestExplainBindFailurePassesThroughOtherErrors keeps the wrapper honest on
// every non-bind failure that flows through the same create path.
func TestExplainBindFailurePassesThroughOtherErrors(t *testing.T) {
	p := &Provider{logger: quietLogger(), detected: DetectResult{Runtime: "colima"}}
	orig := errors.New("Conflict. The container name is already in use")
	if got := p.explainBindFailure(orig); got.Error() != orig.Error() {
		t.Errorf("unrelated error rewritten: %s", got)
	}
}

func TestVMShareRemedyIsRuntimeSpecific(t *testing.T) {
	for runtime, want := range map[string]string{
		"colima":  "colima start --mount /opt/homebrew/libexec:w",
		"rancher": "Rancher Desktop",
		"podman":  "podman machine set --volume /opt/homebrew/libexec",
		"docker":  "File sharing",
	} {
		if got := vmShareRemedy(runtime, "/opt/homebrew/libexec/crewship-sidecar"); !strings.Contains(got, want) {
			t.Errorf("vmShareRemedy(%q) = %q, want it to contain %q", runtime, got, want)
		}
	}
}

// TestPreflightProbesOnlyTheReadOnlyBinds pins what the boot-time probe sends:
// the crew data dirs do not exist yet at boot, so probing them would report a
// failure that is not real.
func TestPreflightProbesOnlyTheReadOnlyBinds(t *testing.T) {
	p := &Provider{
		logger: quietLogger(),
		cfg:    Config{SidecarBinaryPath: "/h/crewship-sidecar", EntrypointPath: "/h/entrypoint.sh"},
	}
	mounts, err := p.buildMounts("preflight", "", "", "", "")
	if err != nil {
		t.Fatalf("buildMounts: %v", err)
	}
	var sources []string
	for _, m := range mounts {
		if m.Type == mount.TypeBind {
			sources = append(sources, m.Source)
		}
	}
	if len(sources) != 2 || sources[0] != "/h/crewship-sidecar" || sources[1] != "/h/entrypoint.sh" {
		t.Errorf("preflight would probe %v, want exactly the sidecar and entrypoint sources", sources)
	}
}
