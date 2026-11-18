//go:build !windows

package docker

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/devcontainer"
)

func TestApplyAgentLoginPathNativeOnlyKeepsImageFallback(t *testing.T) {
	image := "/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin"
	for _, tc := range []struct{ name, current, login string }{
		{"current native only", "/opt/mise/data/shims", ""},
		{"login native only", "", "/opt/mise/data/shims"},
		{"historical five shared directories", "/usr/local/py-utils/bin:/usr/local/share/npm-global/bin:/home/agent/.local/bin:/opt/crewship/bin:/opt/mise/data/shims", ""},
		{"all shared directories", strings.Join(devcontainer.AgentToolPathDirs, ":"), ""},
		{"crew tools only", "/opt/crew-tools/bin", ""},
		{"control-only captures", "\x01\x00", "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := envValue(applyAgentLoginPath([]string{"PATH=" + tc.current}, tc.login, map[string]string{"PATH": image}), "PATH")
			if !strings.HasSuffix(got, image) {
				t.Fatalf("lost image fallback and core utilities: %q; want suffix %q", got, image)
			}
		})
	}
}

func TestApplyAgentLoginPathNativeOnlyCanStartChildShell(t *testing.T) {
	path := envValue(applyAgentLoginPath([]string{"PATH=/opt/mise/data/shims"}, "", map[string]string{"PATH": "/usr/bin:/bin"}), "PATH")
	cmd := exec.Command("/bin/sh", "-c", "sh -c 'printf image-fallback-child-ok'")
	cmd.Env = []string{"PATH=" + path}
	if out, err := cmd.CombinedOutput(); err != nil || string(out) != "image-fallback-child-ok" {
		t.Fatalf("bare child shell lost the image's system PATH: %v %q", err, out)
	}
}

func TestApplyAgentLoginPathNativeOnlyWithoutImageUsesSystemFallback(t *testing.T) {
	got := envValue(applyAgentLoginPath([]string{"PATH=/opt/mise/data/shims"}, "", nil), "PATH")
	if !strings.HasSuffix(got, defaultAgentPath) {
		t.Fatalf("native-only source with unavailable image lost system PATH: %s", got)
	}
}

func TestApplyAgentLoginPathCustomSourcesDoNotAcquireImagePath(t *testing.T) {
	for _, tc := range []struct{ current, login string }{
		{"/custom/bin:/opt/mise/data/shims", ""},
		{"", "/custom/bin:/opt/mise/data/shims"},
	} {
		got := envValue(applyAgentLoginPath([]string{"PATH=" + tc.current}, tc.login, map[string]string{"PATH": "/image-only/bin:/usr/bin:/bin"}), "PATH")
		if strings.Contains(got, "/image-only/bin") {
			t.Fatalf("explicit custom PATH acquired fallback image directories: %s", got)
		}
		if strings.Index(got, "/custom/bin") > strings.Index(got, "/opt/mise/data/shims") {
			t.Fatalf("operator order changed: %s", got)
		}
	}
}
