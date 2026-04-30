package containerstate

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// stubContainer is a ContainerProvider that returns scripted stdout for
// each Exec call. Only the bits used by the snapshot probes are
// implemented; the rest panic so a test never accidentally exercises
// them.
type stubContainer struct {
	// scripted maps the exact joined cmd (sh -c "<script>") to the
	// stdout the probe should observe. Anything not in the map returns
	// an empty stream — mirrors the "binary not present" path.
	scripted map[string]string
	// execErr fires when set; lets a test assert error propagation.
	execErr error
	// calls captures every script the snapshot ran for assertions.
	calls []string
}

func (s *stubContainer) Exec(_ context.Context, cfg provider.ExecConfig) (*provider.ExecResult, error) {
	if s.execErr != nil {
		return nil, s.execErr
	}
	// The probes always shell out via `sh -c "<script>"` so we key the
	// scripted map on the script body for readability.
	script := ""
	if len(cfg.Cmd) >= 3 && cfg.Cmd[0] == "sh" && cfg.Cmd[1] == "-c" {
		script = cfg.Cmd[2]
	}
	s.calls = append(s.calls, script)
	out := s.scripted[script]
	return &provider.ExecResult{Reader: io.NopCloser(strings.NewReader(out))}, nil
}

func (s *stubContainer) EnsureCrewRuntime(_ context.Context, _ provider.CrewConfig) (string, error) {
	panic("not used")
}
func (s *stubContainer) StopCrewRuntime(_ context.Context, _ string) error   { panic("not used") }
func (s *stubContainer) RemoveCrewRuntime(_ context.Context, _ string) error { panic("not used") }
func (s *stubContainer) ContainerStatus(_ context.Context, _ string) (*provider.ContainerStatus, error) {
	panic("not used")
}
func (s *stubContainer) ContainerStats(_ context.Context, _ string) (*provider.ContainerMetrics, error) {
	panic("not used")
}
func (s *stubContainer) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	panic("not used")
}
func (s *stubContainer) CrewContainerName(slug string) string { return "test-" + slug }
func (s *stubContainer) CopyToContainer(_ context.Context, _, _ string, _ io.Reader) error {
	panic("not used")
}

const aptScript = "command -v dpkg-query >/dev/null 2>&1 && dpkg-query -W -f='${Package}\t${Version}\n' 2>/dev/null || true"
const pipScript = `if command -v pip >/dev/null 2>&1; then pip freeze 2>/dev/null; elif command -v pip3 >/dev/null 2>&1; then pip3 freeze 2>/dev/null; else true; fi`
const npmScript = `command -v npm >/dev/null 2>&1 && npm ls -g --depth=0 --json 2>/dev/null || true`
const osScript = `. /etc/os-release 2>/dev/null && printf '%s' "$PRETTY_NAME" || true`

func TestCapture_HappyPath(t *testing.T) {
	c := &stubContainer{
		scripted: map[string]string{
			aptScript: "git\t2.43.0-1\nphp\t8.3.1\nzlib1g\t1:1.3.dfsg-3.1ubuntu2\n",
			pipScript: "requests==2.31.0\nflask==3.0.0\n",
			npmScript: `{"dependencies":{"typescript":{"version":"5.4.5"},"prettier":{"version":"3.2.5"}}}`,
			osScript:  "Ubuntu 24.04 LTS",
		},
	}
	snap, err := Capture(context.Background(), c, "ctr-1")
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if got := len(snap.APT); got != 3 {
		t.Errorf("apt count: want 3, got %d (%+v)", got, snap.APT)
	}
	if snap.APT[0].Name != "git" || snap.APT[0].Version != "2.43.0-1" {
		t.Errorf("first apt entry wrong: %+v", snap.APT[0])
	}
	if got := len(snap.Pip); got != 2 {
		t.Errorf("pip count: want 2, got %d", got)
	}
	if got := len(snap.Npm); got != 2 {
		t.Errorf("npm count: want 2, got %d", got)
	}
	// Names sorted alphabetically inside each list.
	if snap.Npm[0].Name != "prettier" || snap.Npm[1].Name != "typescript" {
		t.Errorf("npm not sorted: %+v", snap.Npm)
	}
	if snap.OS != "Ubuntu 24.04 LTS" {
		t.Errorf("os: %q", snap.OS)
	}
}

func TestCapture_MissingProbesAreEmptyNotErrors(t *testing.T) {
	// Only OS probe answers; the package managers are absent (alpine /
	// distroless style image). Capture must succeed because at least one
	// probe came back with content.
	c := &stubContainer{
		scripted: map[string]string{
			osScript: "Alpine Linux v3.20",
		},
	}
	snap, err := Capture(context.Background(), c, "ctr-1")
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if len(snap.APT) != 0 || len(snap.Pip) != 0 || len(snap.Npm) != 0 {
		t.Errorf("expected empty package lists, got apt=%d pip=%d npm=%d", len(snap.APT), len(snap.Pip), len(snap.Npm))
	}
	if snap.OS != "Alpine Linux v3.20" {
		t.Errorf("os: %q", snap.OS)
	}
}

func TestCapture_AllSilentIsStillSuccess(t *testing.T) {
	// Every probe shell-script ran but emitted nothing — that's the
	// "scratch" / minimal image case. Capture treats empty stdout as a
	// successful probe, so it returns no error.
	c := &stubContainer{scripted: map[string]string{}}
	snap, err := Capture(context.Background(), c, "ctr-1")
	if err != nil {
		t.Fatalf("expected success on empty probes, got err: %v", err)
	}
	if len(snap.APT)+len(snap.Pip)+len(snap.Npm) != 0 {
		t.Errorf("expected empty snapshot, got %+v", snap)
	}
}

func TestHash_StableUnderPermutation(t *testing.T) {
	a := Snapshot{
		APT: []Package{{Name: "git", Version: "1"}, {Name: "php", Version: "2"}},
	}
	b := Snapshot{
		APT: []Package{{Name: "php", Version: "2"}, {Name: "git", Version: "1"}},
	}
	if a.Hash() != b.Hash() {
		t.Errorf("hash must ignore input ordering: %s vs %s", a.Hash(), b.Hash())
	}
}

func TestHash_ChangesOnVersionBump(t *testing.T) {
	a := Snapshot{APT: []Package{{Name: "git", Version: "1"}}}
	b := Snapshot{APT: []Package{{Name: "git", Version: "2"}}}
	if a.Hash() == b.Hash() {
		t.Error("hash must differ when a version changes")
	}
}

func TestPipParsesEditableInstall(t *testing.T) {
	c := &stubContainer{
		scripted: map[string]string{
			pipScript: "-e git+https://example.com/foo.git@abc#egg=foo\nrequests==2.31.0\n",
		},
	}
	snap, err := Capture(context.Background(), c, "ctr-1")
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	var sawFoo bool
	for _, p := range snap.Pip {
		if p.Name == "foo" {
			sawFoo = true
			if p.Version != "" {
				t.Errorf("editable install should leave version empty, got %q", p.Version)
			}
		}
	}
	if !sawFoo {
		t.Errorf("editable install dropped: %+v", snap.Pip)
	}
}
