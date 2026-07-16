package preflight

import (
	"errors"
	"strings"
	"testing"
)

// fakeProbe builds a hostProbe whose PATH lookups succeed only for the
// given binaries and whose filesystem checks succeed only for the given
// paths — no real host state leaks into the tables below.
func fakeProbe(goos string, binaries []string, paths []string) hostProbe {
	binSet := map[string]bool{}
	for _, b := range binaries {
		binSet[b] = true
	}
	pathSet := map[string]bool{}
	for _, p := range paths {
		pathSet[p] = true
	}
	return hostProbe{
		goos: goos,
		lookPath: func(file string) (string, error) {
			if binSet[file] {
				return "/fake/bin/" + file, nil
			}
			return "", errors.New("not found")
		},
		pathExists: func(path string) bool { return pathSet[path] },
	}
}

func names(rts []InstalledRuntime) []string {
	out := make([]string, 0, len(rts))
	for _, rt := range rts {
		out = append(out, rt.Name)
	}
	return out
}

func TestDetectInstalled(t *testing.T) {
	tests := []struct {
		name      string
		probe     hostProbe
		wantNames []string
	}{
		{
			name:      "darwin nothing installed",
			probe:     fakeProbe("darwin", nil, nil),
			wantNames: []string{},
		},
		{
			name:      "darwin docker desktop app bundle",
			probe:     fakeProbe("darwin", nil, []string{"/Applications/Docker.app"}),
			wantNames: []string{"Docker Desktop"},
		},
		{
			name:      "darwin orbstack app bundle",
			probe:     fakeProbe("darwin", nil, []string{"/Applications/OrbStack.app"}),
			wantNames: []string{"OrbStack"},
		},
		{
			name:      "darwin rancher desktop app bundle",
			probe:     fakeProbe("darwin", nil, []string{"/Applications/Rancher Desktop.app"}),
			wantNames: []string{"Rancher Desktop"},
		},
		{
			name:      "darwin colima cli",
			probe:     fakeProbe("darwin", []string{"colima"}, nil),
			wantNames: []string{"Colima"},
		},
		{
			name:      "darwin podman cli",
			probe:     fakeProbe("darwin", []string{"podman"}, nil),
			wantNames: []string{"Podman"},
		},
		{
			name:      "darwin apple containers cli",
			probe:     fakeProbe("darwin", []string{"container"}, nil),
			wantNames: []string{"Apple Containers"},
		},
		{
			// A bare docker CLI on macOS is just a client (brew installs it
			// alongside colima) — it is not evidence of a startable runtime.
			name:      "darwin bare docker cli is not a runtime",
			probe:     fakeProbe("darwin", []string{"docker"}, nil),
			wantNames: []string{},
		},
		{
			name: "darwin multiple installed lists all",
			probe: fakeProbe("darwin", []string{"colima"},
				[]string{"/Applications/Docker.app", "/Applications/OrbStack.app"}),
			wantNames: []string{"Docker Desktop", "OrbStack", "Colima"},
		},
		{
			name:      "linux docker engine",
			probe:     fakeProbe("linux", []string{"docker"}, nil),
			wantNames: []string{"Docker Engine"},
		},
		{
			name:      "linux podman",
			probe:     fakeProbe("linux", []string{"podman"}, nil),
			wantNames: []string{"Podman"},
		},
		{
			// `container` is Apple-only tooling; on Linux a binary with that
			// name is something else entirely and must not be reported.
			name:      "linux container binary is not apple containers",
			probe:     fakeProbe("linux", []string{"container"}, nil),
			wantNames: []string{},
		},
		{
			name:      "windows docker desktop install dir",
			probe:     fakeProbe("windows", nil, []string{`C:\Program Files\Docker\Docker`}),
			wantNames: []string{"Docker Desktop"},
		},
		{
			name:      "windows docker cli on path",
			probe:     fakeProbe("windows", []string{"docker"}, nil),
			wantNames: []string{"Docker Desktop"},
		},
		{
			name:      "windows podman",
			probe:     fakeProbe("windows", []string{"podman"}, nil),
			wantNames: []string{"Podman"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectInstalled(tt.probe)
			gotNames := names(got)
			if len(gotNames) != len(tt.wantNames) {
				t.Fatalf("detectInstalled() = %v, want names %v", gotNames, tt.wantNames)
			}
			for i := range tt.wantNames {
				if gotNames[i] != tt.wantNames[i] {
					t.Errorf("detectInstalled()[%d] = %q, want %q", i, gotNames[i], tt.wantNames[i])
				}
			}
			for _, rt := range got {
				if strings.TrimSpace(rt.StartHint) == "" {
					t.Errorf("runtime %q has empty StartHint", rt.Name)
				}
			}
		})
	}
}

func TestDetectInstalledStartHints(t *testing.T) {
	tests := []struct {
		name     string
		probe    hostProbe
		wantHint string
	}{
		{
			name:     "darwin docker desktop opens the app",
			probe:    fakeProbe("darwin", nil, []string{"/Applications/Docker.app"}),
			wantHint: "open -a Docker",
		},
		{
			name:     "darwin colima start",
			probe:    fakeProbe("darwin", []string{"colima"}, nil),
			wantHint: "colima start",
		},
		{
			name:     "darwin podman machine start",
			probe:    fakeProbe("darwin", []string{"podman"}, nil),
			wantHint: "podman machine start",
		},
		{
			name:     "darwin apple containers system start",
			probe:    fakeProbe("darwin", []string{"container"}, nil),
			wantHint: "container system start",
		},
		{
			name:     "linux docker engine via systemctl",
			probe:    fakeProbe("linux", []string{"docker"}, nil),
			wantHint: "sudo systemctl start docker",
		},
		{
			name:     "linux podman socket activation",
			probe:    fakeProbe("linux", []string{"podman"}, nil),
			wantHint: "systemctl --user enable --now podman.socket",
		},
		{
			name:     "windows podman machine start",
			probe:    fakeProbe("windows", []string{"podman"}, nil),
			wantHint: "podman machine start",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectInstalled(tt.probe)
			if len(got) != 1 {
				t.Fatalf("expected exactly one runtime, got %v", names(got))
			}
			if !strings.Contains(got[0].StartHint, tt.wantHint) {
				t.Errorf("StartHint = %q, want it to contain %q", got[0].StartHint, tt.wantHint)
			}
		})
	}
}

func TestGuidanceInstalledNotRunning(t *testing.T) {
	r := Result{
		Status: RuntimeInstalledNotRunning,
		Installed: []InstalledRuntime{
			{Name: "Docker Desktop", StartHint: "open -a Docker"},
			{Name: "Colima", StartHint: "colima start"},
		},
	}
	out := Guidance("darwin", r)

	for _, want := range []string{
		"installed but not running",
		"Docker Desktop",
		"open -a Docker",
		"Colima",
		"colima start",
		"crewship start --no-docker",
		"crewship doctor",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Guidance() missing %q in:\n%s", want, out)
		}
	}
	// Must NOT tell the user to install anything — they already have a runtime.
	for _, reject := range []string{"Install", "install one"} {
		if strings.Contains(out, reject) {
			t.Errorf("Guidance() for installed-not-running must not say %q:\n%s", reject, out)
		}
	}
}

func TestGuidanceMissingPerOS(t *testing.T) {
	tests := []struct {
		goos  string
		wants []string
	}{
		{
			goos: "darwin",
			wants: []string{
				"macOS",
				"Docker Desktop",
				"https://docs.docker.com",
				"OrbStack",
				"Colima",
				"crewship start --no-docker",
				"crewship doctor",
			},
		},
		{
			goos: "linux",
			wants: []string{
				"Linux",
				"get.docker.com",
				"systemctl enable --now docker",
				"usermod -aG docker",
				"Podman",
				"crewship start --no-docker",
				"crewship doctor",
			},
		},
		{
			goos: "windows",
			wants: []string{
				"Docker Desktop",
				"WSL 2",
				"https://docs.docker.com",
				"crewship start --no-docker",
				"crewship doctor",
			},
		},
		{
			// Unknown OS still gets generic, actionable guidance.
			goos: "freebsd",
			wants: []string{
				"https://docs.docker.com",
				"crewship start --no-docker",
				"crewship doctor",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.goos, func(t *testing.T) {
			out := Guidance(tt.goos, Result{Status: RuntimeMissing})
			for _, want := range tt.wants {
				if !strings.Contains(out, want) {
					t.Errorf("Guidance(%q, missing) missing %q in:\n%s", tt.goos, want, out)
				}
			}
		})
	}
}

func TestGuidanceRunningIsEmpty(t *testing.T) {
	if out := Guidance("darwin", Result{Status: RuntimeRunning}); out != "" {
		t.Errorf("Guidance() for a running runtime should be empty, got:\n%s", out)
	}
}
