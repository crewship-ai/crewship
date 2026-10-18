package backup

import (
	"strings"
	"testing"
)

// ODCH-ADM-08: the restore checks compare the images the archive's crews were
// pinned to with what this server resolves for the same image today.
func TestEvaluateRestoreChecks_ImageAge(t *testing.T) {
	m := &Manifest{FormatVersion: FormatVersion, Scope: ScopeWorkspace,
		Contents: Contents{Workspace: &WorkspaceSummary{Slug: "lab"}, Crews: []CrewSummary{
			{Slug: "old", RuntimeImage: "img:1", BaseImageDigest: "sha256:aaa"},
			{Slug: "same", RuntimeImage: "img:2", BaseImageDigest: "sha256:bbb"},
			{Slug: "nopin", RuntimeImage: "img:3"},
			{Slug: "gone", RuntimeImage: "img:4", BaseImageDigest: "sha256:ddd"},
		}}}
	current := map[string]string{"img:1": "sha256:zzz", "img:2": "sha256:bbb", "img:3": "sha256:ccc"}
	lookup := func(image string) (string, bool) { d, ok := current[image]; return d, ok }

	cases := []struct {
		name          string
		lookup        func(string) (string, bool)
		older, checks int
		warn          string
	}{
		{"older image is counted and named", lookup, 1, 2, "old: built on an older image"},
		{"no resolver means nothing is claimed", nil, 0, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluateRestoreChecks(RestoreCheckInput{Manifest: m, Target: TargetNewWorkspace, HostPlatform: "linux/arm64", CurrentImageDigest: tc.lookup})
			if got.Runtime.ImagesOlder != tc.older || got.Runtime.ImagesChecked != tc.checks {
				t.Fatalf("older/checked = %d/%d, want %d/%d", got.Runtime.ImagesOlder, got.Runtime.ImagesChecked, tc.older, tc.checks)
			}
			joined := strings.Join(got.Runtime.Warnings, "|")
			if tc.warn != "" && !strings.Contains(joined, tc.warn) {
				t.Fatalf("warnings %q lack %q", joined, tc.warn)
			}
			if tc.warn == "" && strings.Contains(joined, "older image") {
				t.Fatalf("unexpected image warning: %q", joined)
			}
		})
	}
}
