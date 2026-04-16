package docker

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/mount"
)

// These tests exercise PURE helpers in the docker provider package — they
// require neither a Docker daemon nor any subprocess. They are
// table-driven, parallel-safe, and run in well under 200ms each.

func TestCrewContainerName_Compose(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		prefix string
		slug   string
		want   string
	}{
		{
			name:   "default prefix when empty",
			prefix: "",
			slug:   "engineering",
			want:   "crewship-team-engineering",
		},
		{
			name:   "explicit default prefix",
			prefix: "crewship",
			slug:   "engineering",
			want:   "crewship-team-engineering",
		},
		{
			name:   "multi-instance prefix",
			prefix: "crewship-2",
			slug:   "engineering",
			want:   "crewship-2-team-engineering",
		},
		{
			name:   "single-letter slug",
			prefix: "",
			slug:   "x",
			want:   "crewship-team-x",
		},
		{
			name:   "slug with hyphens",
			prefix: "",
			slug:   "engineering-platform",
			want:   "crewship-team-engineering-platform",
		},
		{
			name:   "empty slug still composes (caller responsibility)",
			prefix: "",
			slug:   "",
			want:   "crewship-team-",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := &Provider{cfg: Config{ContainerPrefix: tt.prefix}}
			got := p.CrewContainerName(tt.slug)
			if got != tt.want {
				t.Errorf("CrewContainerName(prefix=%q, slug=%q) = %q, want %q",
					tt.prefix, tt.slug, got, tt.want)
			}
		})
	}
}

func TestVolumeNames_PrefixAware(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		prefix    string
		slug      string
		wantHome  string
		wantTools string
	}{
		{
			name:      "default prefix",
			prefix:    "",
			slug:      "alpha",
			wantHome:  "crewship-home-alpha",
			wantTools: "crewship-tools-alpha",
		},
		{
			name:      "instance 3 prefix",
			prefix:    "crewship-3",
			slug:      "alpha",
			wantHome:  "crewship-3-home-alpha",
			wantTools: "crewship-3-tools-alpha",
		},
		{
			name:      "custom org prefix",
			prefix:    "myorg",
			slug:      "rocket",
			wantHome:  "myorg-home-rocket",
			wantTools: "myorg-tools-rocket",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := &Provider{cfg: Config{ContainerPrefix: tt.prefix}}
			if got := p.homeVolumeName(tt.slug); got != tt.wantHome {
				t.Errorf("homeVolumeName(%q) = %q, want %q", tt.slug, got, tt.wantHome)
			}
			if got := p.toolsVolumeName(tt.slug); got != tt.wantTools {
				t.Errorf("toolsVolumeName(%q) = %q, want %q", tt.slug, got, tt.wantTools)
			}
		})
	}
}

// TestBuildMounts_FullLayout asserts every documented mount target/source
// combination in the canonical layout. Catches regressions where someone
// silently drops or reorders mounts.
func TestBuildMounts_FullLayout(t *testing.T) {
	t.Parallel()

	p := &Provider{cfg: Config{
		SidecarBinaryPath: "/h/sidecar",
		EntrypointPath:    "/h/entrypoint.sh",
		ContainerPrefix:   "crewship",
	}}

	mounts, err := p.buildMounts("eng", "/ws", "/out", "/crew", "/secrets")
	if err != nil {
		t.Fatalf("buildMounts: %v", err)
	}

	want := map[string]struct {
		source   string
		mtype    mount.Type
		readOnly bool
	}{
		"/workspace":                      {source: "/ws", mtype: mount.TypeBind, readOnly: false},
		"/output":                         {source: "/out", mtype: mount.TypeBind, readOnly: false},
		"/crew":                           {source: "/crew", mtype: mount.TypeBind, readOnly: false},
		"/secrets":                        {source: "/secrets", mtype: mount.TypeBind, readOnly: false},
		"/home/agent":                     {source: "crewship-home-eng", mtype: mount.TypeVolume, readOnly: false},
		"/opt/crew-tools":                 {source: "crewship-tools-eng", mtype: mount.TypeVolume, readOnly: false},
		"/usr/local/bin/crewship-sidecar": {source: "/h/sidecar", mtype: mount.TypeBind, readOnly: true},
		"/usr/local/bin/entrypoint.sh":    {source: "/h/entrypoint.sh", mtype: mount.TypeBind, readOnly: true},
	}

	if len(mounts) != len(want) {
		t.Fatalf("mount count = %d, want %d (mounts=%+v)", len(mounts), len(want), mounts)
	}

	got := map[string]bool{}
	for _, m := range mounts {
		exp, ok := want[m.Target]
		if !ok {
			t.Errorf("unexpected mount target %q (source=%q)", m.Target, m.Source)
			continue
		}
		if m.Source != exp.source {
			t.Errorf("target %q: source = %q, want %q", m.Target, m.Source, exp.source)
		}
		if m.Type != exp.mtype {
			t.Errorf("target %q: type = %q, want %q", m.Target, m.Type, exp.mtype)
		}
		if m.ReadOnly != exp.readOnly {
			t.Errorf("target %q: readOnly = %v, want %v", m.Target, m.ReadOnly, exp.readOnly)
		}
		got[m.Target] = true
	}
	for tgt := range want {
		if !got[tgt] {
			t.Errorf("missing mount target %q", tgt)
		}
	}
}

func TestBuildMounts_NoSlugSkipsHomeAndToolsVolumes(t *testing.T) {
	t.Parallel()

	p := &Provider{cfg: Config{
		SidecarBinaryPath: "/h/sidecar",
		EntrypointPath:    "/h/entrypoint.sh",
	}}

	mounts, err := p.buildMounts("", "/ws", "/out", "/crew", "/secrets")
	if err != nil {
		t.Fatalf("buildMounts: %v", err)
	}

	for _, m := range mounts {
		if m.Target == "/home/agent" || m.Target == "/opt/crew-tools" {
			t.Errorf("did not expect %q mount when slug is empty (got source=%q)",
				m.Target, m.Source)
		}
		if m.Type == mount.TypeVolume {
			t.Errorf("did not expect any volume mounts when slug is empty (got %+v)", m)
		}
	}
}

func TestBuildMounts_PrefixAppliedToVolumes(t *testing.T) {
	t.Parallel()

	p := &Provider{cfg: Config{
		SidecarBinaryPath: "/h/sidecar",
		EntrypointPath:    "/h/entrypoint.sh",
		ContainerPrefix:   "crewship-7",
	}}

	mounts, err := p.buildMounts("rocket", "/ws", "/out", "/crew", "/secrets")
	if err != nil {
		t.Fatalf("buildMounts: %v", err)
	}

	wantSources := map[string]string{
		"/home/agent":     "crewship-7-home-rocket",
		"/opt/crew-tools": "crewship-7-tools-rocket",
	}
	for _, m := range mounts {
		if expSrc, ok := wantSources[m.Target]; ok {
			if m.Source != expSrc {
				t.Errorf("target %q: source = %q, want %q", m.Target, m.Source, expSrc)
			}
		}
	}
}

func TestBuildMounts_SidecarAndEntrypointAreReadOnly(t *testing.T) {
	t.Parallel()

	p := &Provider{cfg: Config{
		SidecarBinaryPath: "/h/sidecar",
		EntrypointPath:    "/h/entrypoint.sh",
	}}

	mounts, err := p.buildMounts("rocket", "/ws", "/out", "/crew", "/secrets")
	if err != nil {
		t.Fatalf("buildMounts: %v", err)
	}

	roTargets := map[string]bool{
		"/usr/local/bin/crewship-sidecar": false,
		"/usr/local/bin/entrypoint.sh":    false,
	}
	for _, m := range mounts {
		if _, ok := roTargets[m.Target]; ok {
			if !m.ReadOnly {
				t.Errorf("expected %q to be read-only", m.Target)
			}
			roTargets[m.Target] = true
		}
	}
	for tgt, found := range roTargets {
		if !found {
			t.Errorf("expected %q in mounts", tgt)
		}
	}
}

func TestBuildMounts_MissingSidecarErrorMessage(t *testing.T) {
	t.Parallel()

	p := &Provider{cfg: Config{EntrypointPath: "/h/entrypoint.sh"}}
	_, err := p.buildMounts("eng", "/ws", "/out", "/crew", "/secrets")
	if err == nil {
		t.Fatal("expected error when SidecarBinaryPath is empty")
	}
	if !strings.Contains(err.Error(), "SidecarBinaryPath") {
		t.Errorf("error should mention SidecarBinaryPath: %v", err)
	}
	if !strings.Contains(err.Error(), "CREWSHIP_SIDECAR_PATH") {
		t.Errorf("error should hint at CREWSHIP_SIDECAR_PATH env var: %v", err)
	}
}

func TestBuildMounts_MissingEntrypointErrorMessage(t *testing.T) {
	t.Parallel()

	p := &Provider{cfg: Config{SidecarBinaryPath: "/h/sidecar"}}
	_, err := p.buildMounts("eng", "/ws", "/out", "/crew", "/secrets")
	if err == nil {
		t.Fatal("expected error when EntrypointPath is empty")
	}
	if !strings.Contains(err.Error(), "EntrypointPath") {
		t.Errorf("error should mention EntrypointPath: %v", err)
	}
	if !strings.Contains(err.Error(), "CREWSHIP_ENTRYPOINT_PATH") {
		t.Errorf("error should hint at CREWSHIP_ENTRYPOINT_PATH env var: %v", err)
	}
}

func TestShortID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", ""},
		{"shorter than 12", "abc", "abc"},
		{"exactly 12", "abcdefghijkl", "abcdefghijkl"},
		{"longer than 12", "abcdefghijklmnopqrstuvwxyz", "abcdefghijkl"},
		{
			"realistic 64-char container ID",
			"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			"0123456789ab",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := shortID(tt.input); got != tt.want {
				t.Errorf("shortID(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestBoolPtrIf(t *testing.T) {
	t.Parallel()

	if got := boolPtrIf(false); got != nil {
		t.Errorf("boolPtrIf(false) = %v, want nil", got)
	}
	got := boolPtrIf(true)
	if got == nil {
		t.Fatal("boolPtrIf(true) = nil, want pointer to true")
	}
	if *got != true {
		t.Errorf("*boolPtrIf(true) = %v, want true", *got)
	}
}

func TestCandidateSockets_NoDuplicates(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}
	for _, c := range candidateSockets() {
		if c.path == "" {
			t.Errorf("empty socket path in candidate %+v", c)
		}
		if c.runtime == "" {
			t.Errorf("empty runtime label for socket %q", c.path)
		}
		if !filepath.IsAbs(c.path) {
			t.Errorf("socket path is not absolute: %q", c.path)
		}
		if seen[c.path] {
			t.Errorf("duplicate socket path: %q", c.path)
		}
		seen[c.path] = true
	}
}

// TestCandidateSockets_KnownRuntimes pins the runtime label vocabulary so
// downstream telemetry consumers don't break if a label is renamed by accident.
func TestCandidateSockets_KnownRuntimes(t *testing.T) {
	t.Parallel()

	allowed := map[string]bool{
		"docker":   true,
		"podman":   true,
		"colima":   true,
		"orbstack": true,
		"rancher":  true,
		"nerdctl":  true,
	}
	for _, c := range candidateSockets() {
		if !allowed[c.runtime] {
			t.Errorf("unexpected runtime label %q for socket %q", c.runtime, c.path)
		}
	}
}

// TestCandidateSockets_DockerFirst — Docker sockets must come BEFORE
// Podman/nerdctl because in mixed environments the user probably installed
// Docker explicitly. Reordering would silently flip detection.
func TestCandidateSockets_DockerFirst(t *testing.T) {
	t.Parallel()

	candidates := candidateSockets()
	if len(candidates) == 0 {
		t.Fatal("candidateSockets returned empty list")
	}
	if candidates[0].runtime != "docker" {
		t.Errorf("first socket should be docker, got %q (path=%s)",
			candidates[0].runtime, candidates[0].path)
	}

	firstPodman := -1
	firstDocker := -1
	for i, c := range candidates {
		if firstDocker == -1 && c.runtime == "docker" {
			firstDocker = i
		}
		if firstPodman == -1 && c.runtime == "podman" {
			firstPodman = i
		}
	}
	if firstPodman != -1 && firstPodman < firstDocker {
		t.Errorf("podman socket appears before docker (podman idx=%d, docker idx=%d)",
			firstPodman, firstDocker)
	}
}

// TestProvider_Getters covers the pure getters that just expose stored
// fields. They have no daemon dependency.
func TestProvider_Getters(t *testing.T) {
	t.Parallel()

	det := DetectResult{Runtime: "docker", Socket: "/var/run/docker.sock", Version: "26.0.0"}
	p := &Provider{detected: det}

	if got := p.Detected(); got != det {
		t.Errorf("Detected() = %+v, want %+v", got, det)
	}
	if got := p.HostAddress(); got != "host.docker.internal" {
		t.Errorf("HostAddress() = %q, want host.docker.internal", got)
	}
	// DockerClient just returns the stored client (nil here is acceptable).
	if p.DockerClient() != nil {
		t.Error("DockerClient() should return the stored nil client unchanged")
	}
}

// TestRunPostStartCommands_EmptyShortCircuits ensures the empty-list early
// return does not panic on a nil-client Provider. We never reach a daemon
// call when cmds is empty.
func TestRunPostStartCommands_EmptyShortCircuits(t *testing.T) {
	t.Parallel()

	p := &Provider{} // nil client + nil logger would crash if we got past the early return

	// These must not panic and must not touch p.client / p.logger.
	p.runPostStartCommands(context.Background(), "container-id", nil)
	p.runPostStartCommands(context.Background(), "container-id", []string{})
}

// TestDetect_InvalidDockerHost exercises the DOCKER_HOST short-circuit
// branch. Pointing DOCKER_HOST at a path that cannot ping must return a
// wrapped error mentioning DOCKER_HOST so operators can debug it. Runs
// without a Docker daemon — we expect failure, and the failure path is
// what we cover.
func TestDetect_InvalidDockerHost(t *testing.T) {
	// NOT t.Parallel() — mutates process env.
	t.Setenv("DOCKER_HOST", "unix:///nonexistent/path/docker.sock")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err := Detect(ctx)
	if err == nil {
		t.Fatal("expected error for nonexistent DOCKER_HOST socket")
	}
	if !strings.Contains(err.Error(), "DOCKER_HOST") {
		t.Errorf("error should mention DOCKER_HOST: %v", err)
	}
}

func TestConfig_ZeroValuesAreUsable(t *testing.T) {
	t.Parallel()

	// A zero Config should not panic when used to build a Provider for
	// helper-only operations (CrewContainerName, volume names).
	p := &Provider{cfg: Config{}}

	// Defaults kick in via empty-string fallback inside helpers.
	if name := p.CrewContainerName("eng"); name != "crewship-team-eng" {
		t.Errorf("zero-config name = %q, want crewship-team-eng", name)
	}
	if name := p.homeVolumeName("eng"); name != "crewship-home-eng" {
		t.Errorf("zero-config home volume = %q, want crewship-home-eng", name)
	}
	if name := p.toolsVolumeName("eng"); name != "crewship-tools-eng" {
		t.Errorf("zero-config tools volume = %q, want crewship-tools-eng", name)
	}
}
