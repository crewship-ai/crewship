package backup

import (
	"errors"
	"strings"
	"testing"
)

func TestUnsafeDevcontainerSettings(t *testing.T) {
	cases := []struct {
		name string
		json string
		want []string
	}{
		{name: "plain crew", json: `{"image":"crewship/agent-runtime"}`, want: nil},
		{name: "docker socket mount", json: `{"mounts":["source=/var/run/docker.sock,target=/var/run/docker.sock,type=bind"]}`, want: []string{"Docker socket mount on ops"}},
		{name: "privileged flag", json: `{"privileged": true}`, want: []string{"privileged mode on ops"}},
		{name: "privileged run arg and host network", json: `{"runArgs":["--privileged","--network=host"]}`, want: []string{"privileged mode on ops", "host-level runtime option --network=host on ops"}},
		{name: "host path bind object", json: `{"mounts":[{"source":"/srv/data","target":"/data","type":"bind"}]}`, want: []string{"host path mount on ops"}},
		{name: "named volume is fine", json: `{"mounts":["source=cache,target=/cache,type=volume"]}`, want: nil},
		{name: "comments allowed", json: "{\n// a note\n\"privileged\": true}", want: []string{"privileged mode on ops"}},
		{name: "a // inside a string is not a comment", json: `{"image":"http://x/y","privileged":false}`, want: nil},
		{name: "garbage", json: `{`, want: []string{"unreadable devcontainer configuration on ops"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := UnsafeDevcontainerSettings("ops", []byte(tc.json))
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEvaluateRestoreChecks(t *testing.T) {
	ws := &Manifest{FormatVersion: FormatVersion, Scope: ScopeWorkspace,
		Contents:       Contents{Workspace: &WorkspaceSummary{Slug: "lab"}, Crews: []CrewSummary{{Slug: "ops"}}},
		SourceInstance: Instance{Platform: "linux/arm64"}}
	inst := &Manifest{FormatVersion: FormatVersion, Scope: ScopeInstance,
		Contents: Contents{Instance: &InstanceContents{DatabaseBytes: 100, Stores: map[string]StoreSummary{StoreOutput: {Bytes: 50}}}}}
	old := &Manifest{FormatVersion: 1, Scope: ScopeWorkspace, Contents: Contents{Workspace: &WorkspaceSummary{Slug: "lab"}}}
	exists := func(s string) bool { return s == "lab" }
	cases := []struct {
		name  string
		in    RestoreCheckInput
		check func(t *testing.T, got RestoreChecks)
	}{
		{name: "instance need is exact, no room", in: RestoreCheckInput{Manifest: inst, BundleSize: 10, Target: TargetEmptyServer, FreeBytes: 100},
			check: func(t *testing.T, got RestoreChecks) {
				if got.Space.OK || got.Space.NeedBytes != 160 {
					t.Fatalf("space = %+v", got.Space)
				}
				if !got.Conflicts.OK {
					t.Fatalf("conflicts = %+v", got.Conflicts)
				}
			}},
		{name: "instance into a workspace target is refused", in: RestoreCheckInput{Manifest: inst, Target: TargetReplace},
			check: func(t *testing.T, got RestoreChecks) {
				if got.Conflicts.OK {
					t.Fatal("instance bundle accepted a workspace target")
				}
			}},
		{name: "arch mismatch warns and says rebuilt", in: RestoreCheckInput{Manifest: ws, Target: TargetReplace, FreeBytes: 1 << 30, HostPlatform: "linux/amd64", UnsafeChecked: true, WorkspaceExists: exists},
			check: func(t *testing.T, got RestoreChecks) {
				if !got.Runtime.OK || len(got.Runtime.Warnings) != 1 || !strings.Contains(got.Runtime.Warnings[0], "rebuilt from the crew image") {
					t.Fatalf("runtime = %+v", got.Runtime)
				}
				if !strings.Contains(got.Conflicts.Detail, "replaces workspace lab") {
					t.Fatalf("conflicts = %+v", got.Conflicts)
				}
			}},
		{name: "no docker with crews is not ok", in: RestoreCheckInput{Manifest: ws, Target: TargetNewWorkspace, DockerErr: errors.New("no daemon"), HostPlatform: "linux/arm64"},
			check: func(t *testing.T, got RestoreChecks) {
				if got.Runtime.OK {
					t.Fatal("runtime ok without docker")
				}
				if len(got.Runtime.Warnings) != 1 || !strings.Contains(got.Runtime.Warnings[0], "not checked") {
					t.Fatalf("warnings = %v", got.Runtime.Warnings)
				}
			}},
		{name: "old format goes through a converter", in: RestoreCheckInput{Manifest: old, Target: TargetNewWorkspace},
			check: func(t *testing.T, got RestoreChecks) {
				if !got.Format.OK || got.Format.Version != 1 || got.Format.Converter != !IsCompatible(1) {
					t.Fatalf("format = %+v", got.Format)
				}
			}},
		{name: "unsafe list is passed through", in: RestoreCheckInput{Manifest: ws, Target: TargetCrew, Unsafe: []string{"Docker socket mount on ops"}, UnsafeChecked: true},
			check: func(t *testing.T, got RestoreChecks) {
				if len(got.Unsafe) != 1 {
					t.Fatalf("unsafe = %v", got.Unsafe)
				}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { tc.check(t, EvaluateRestoreChecks(tc.in)) })
	}
}
