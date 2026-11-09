package cli

import (
	"strings"
	"testing"
)

// A shell that exports CREWSHIP_SERVER for one slot, run from a directory that
// no profile is bound to, dialled the persisted default profile instead and got
// that server's answers (for example "503 Docs are not enabled on this server"
// from a slot without Docs). The CLI keeps the profile precedence but says so.
func TestIgnoredServerEnvNotice(t *testing.T) {
	cfg := &CLIConfig{
		Current: "dev3",
		Servers: map[string]*ServerProfile{
			"dev2": {Server: "http://127.0.0.1:8082"},
			"dev3": {Server: "http://127.0.0.1:8083"},
		},
		DirectoryProfiles: map[string]string{"/w/crewship_2": "dev2"},
	}
	cases := []struct {
		name, env, flagServer, flagProfile, envProfile, cwd string
		cfg                                                 *CLIConfig
		want                                                []string // substrings; nil means no notice
	}{
		{name: "persisted default wins over env for another server", env: "http://localhost:8082", cwd: "/tmp", cfg: cfg,
			want: []string{"CREWSHIP_SERVER=http://localhost:8082 is ignored", `profile "dev3"`, "http://127.0.0.1:8083", "--profile"}},
		{name: "directory profile on the same server (loopback alias)", env: "http://localhost:8082", cwd: "/w/crewship_2/sub", cfg: cfg},
		{name: "directory profile on another server", env: "http://localhost:8083", cwd: "/w/crewship_2", cfg: cfg,
			want: []string{"is ignored", `profile "dev2"`, "directory"}},
		{name: "same server as the persisted default", env: "http://127.0.0.1:8083/", cwd: "/tmp", cfg: cfg},
		{name: "no env", cwd: "/tmp", cfg: cfg},
		{name: "--server wins anyway", env: "http://localhost:8082", flagServer: "http://localhost:8082", cwd: "/tmp", cfg: cfg},
		{name: "explicit --profile is a deliberate choice", env: "http://localhost:8082", flagProfile: "dev3", cwd: "/tmp", cfg: cfg},
		{name: "explicit CREWSHIP_PROFILE is a deliberate choice", env: "http://localhost:8082", envProfile: "dev3", cwd: "/tmp", cfg: cfg},
		{name: "legacy mode honours the env", env: "http://localhost:8082", cwd: "/tmp", cfg: &CLIConfig{Server: "http://127.0.0.1:8083"}},
		{name: "no config", env: "http://localhost:8082", cwd: "/tmp"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CREWSHIP_SERVER", tc.env)
			t.Setenv("CREWSHIP_PROFILE", tc.envProfile)
			SetWorkingDir(tc.cwd)
			t.Cleanup(func() { SetWorkingDir("") })
			got := IgnoredServerEnvNotice(tc.flagServer, tc.flagProfile, tc.cfg)
			if tc.want == nil {
				if got != "" {
					t.Fatalf("unexpected notice %q", got)
				}
				return
			}
			for _, part := range tc.want {
				if !strings.Contains(got, part) {
					t.Fatalf("notice %q lacks %q", got, part)
				}
			}
		})
	}
}
