package docker

import (
	"strings"
	"testing"
)

// The capture-failure fallback must reach mise-installed tools: a crew built
// through BuildKit has no captured login PATH, and its adapter CLI may live
// only under the mise shims dir.
func TestApplyAgentLoginPath_FallbackIncludesMiseShims(t *testing.T) {
	env := applyAgentLoginPath([]string{"HOME=/home/agent"}, "", map[string]string{"PATH": "/usr/local/bin:/usr/bin:/bin"})
	var path string
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			path = strings.TrimPrefix(kv, "PATH=")
		}
	}
	if path == "" {
		t.Fatalf("no PATH in %v", env)
	}
	if !strings.Contains(path, "/opt/mise/data/shims") {
		t.Errorf("fallback PATH lacks the mise shims dir: %s", path)
	}
	if !strings.HasSuffix(path, "/usr/local/bin:/usr/bin:/bin") {
		t.Errorf("image PATH must be kept at the end: %s", path)
	}
	// A captured login PATH that lacks the mise shims dir (the case on a
	// BuildKit-built crew, where the capture sees the bare image PATH) still
	// gets it: the captured value is the base, not the whole answer.
	env = applyAgentLoginPath(nil, "/usr/local/bin:/usr/bin:/bin:/usr/local/games:/usr/games", nil)
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			if !strings.Contains(kv, "/opt/mise/data/shims") || !strings.HasSuffix(kv, "/usr/local/games:/usr/games") {
				t.Errorf("captured PATH must be kept and the mise shims dir added: %s", kv)
			}
		}
	}
}
