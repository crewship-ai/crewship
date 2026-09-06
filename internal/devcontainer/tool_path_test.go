package devcontainer

import (
	"strings"
	"testing"
)

func TestEnsureAgentToolPath(t *testing.T) {
	tests := []struct {
		name string
		in   map[string]string
		want string
	}{
		{"nil env refers to the image's own PATH behind the tool dirs", nil,
			"/home/agent/.local/bin:/opt/crewship/bin:/opt/mise/data/shims:${containerEnv:PATH}"},
		{"empty PATH same as absent", map[string]string{"PATH": "  "},
			"/home/agent/.local/bin:/opt/crewship/bin:/opt/mise/data/shims:${containerEnv:PATH}"},
		{"operator PATH is kept, tool dirs go first", map[string]string{"PATH": "/opt/x/bin:/usr/bin"},
			"/home/agent/.local/bin:/opt/crewship/bin:/opt/mise/data/shims:/opt/x/bin:/usr/bin"},
		{"already complete is untouched", map[string]string{"PATH": "/home/agent/.local/bin:/opt/crewship/bin:/opt/mise/data/shims:/usr/bin"},
			"/home/agent/.local/bin:/opt/crewship/bin:/opt/mise/data/shims:/usr/bin"},
		{"partially present adds only the missing dirs", map[string]string{"PATH": "/opt/mise/data/shims:/usr/bin"},
			"/home/agent/.local/bin:/opt/crewship/bin:/opt/mise/data/shims:/usr/bin"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ensureAgentToolPath(tc.in)
			if got["PATH"] != tc.want {
				t.Errorf("PATH = %q\n   want %q", got["PATH"], tc.want)
			}
		})
	}
	// Other keys survive, the mise variables are added, an operator's own
	// value for one of them is kept.
	env := ensureAgentToolPath(map[string]string{"FOO": "bar", "MISE_CACHE_DIR": "/tmp/mise-cache"})
	if env["FOO"] != "bar" {
		t.Error("FOO dropped")
	}
	if env["MISE_DATA_DIR"] != "/opt/mise/data" || env["MISE_GLOBAL_CONFIG_FILE"] != "/opt/mise/config/config.toml" {
		t.Errorf("mise runtime env not set: %v", env)
	}
	// Writable at runtime (read-only rootfs): state under the home volume.
	if env["MISE_STATE_DIR"] != "/home/agent/.local/state/mise" {
		t.Errorf("runtime state dir must be under the home volume: %v", env)
	}
	if env["MISE_CACHE_DIR"] != "/tmp/mise-cache" {
		t.Errorf("operator value overridden: %v", env)
	}
}

// The aggregated env reaches the image ENV: PATH with the tool dirs first and
// the MISE_* variables, so `docker run <cached image>` resolves mise tools
// without the runtime's help.
func TestGenerateDockerfile_CarriesAgentToolEnv(t *testing.T) {
	env := ensureAgentToolPath(map[string]string{"TZ": "UTC"})
	df, err := GenerateDockerfile(DockerfileBuild{BaseImage: "debian:bookworm", RootEnv: env})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ENV PATH=/home/agent/.local/bin:/opt/crewship/bin:/opt/mise/data/shims:",
		"ENV MISE_DATA_DIR=/opt/mise/data",
		"ENV MISE_GLOBAL_CONFIG_FILE=/opt/mise/config/config.toml",
		"ENV TZ=UTC",
	} {
		if !strings.Contains(df, want) {
			t.Errorf("Dockerfile lacks %q:\n%s", want, df)
		}
	}
}

func TestImageEnvChanges(t *testing.T) {
	imageEnv := map[string]string{"PATH": "/usr/local/bin:/usr/bin", "LANG": "C.UTF-8"}
	got := imageEnvChanges(map[string]string{
		"PATH":       "/opt/mise/data/shims:${containerEnv:PATH}",
		"TZ":         "Europe/Prague",
		"WITH SPACE": "x",                                   // invalid key: skipped
		"lower":      "x",                                   // not an env name by the Dockerfile rule: skipped
		"NOTE":       "has \"q\"",                           // quoted safely
		"BAD":        "a\nb",                                // control char: skipped
		"CONDA":      "${containerEnv:NOPE}:/opt/conda/bin", // unknown ref dropped, separators collapsed
	}, imageEnv)
	want := []string{
		`ENV CONDA="/opt/conda/bin"`,
		`ENV NOTE="has \"q\""`,
		`ENV PATH="/opt/mise/data/shims:/usr/local/bin:/usr/bin"`,
		`ENV TZ="Europe/Prague"`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("changes =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if imageEnvChanges(nil, imageEnv) != nil {
		t.Error("nil env must render no changes")
	}
	// No image env at all: the reference goes, the tool dirs stay.
	got = imageEnvChanges(map[string]string{"PATH": "/opt/mise/data/shims:${containerEnv:PATH}"}, nil)
	if len(got) != 1 || got[0] != `ENV PATH="/opt/mise/data/shims"` {
		t.Errorf("unresolvable reference not dropped cleanly: %v", got)
	}
}
