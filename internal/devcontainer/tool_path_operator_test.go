package devcontainer

import (
	"strings"
	"testing"
)

func TestEnsureAgentToolPathPreservesOperatorPrecedence(t *testing.T) {
	input := "/opt/mise/data/shims:/home/agent/.local/bin:/usr/local/share/npm-global/bin:/usr/bin:/bin"
	got := ensureAgentToolPath(map[string]string{"PATH": input})["PATH"]
	if !strings.HasSuffix(got, input) {
		t.Fatalf("operator precedence changed: %q; expected existing sequence %q", got, input)
	}
	if strings.Index(got, MiseShimsDir) > strings.Index(got, "/home/agent/.local/bin") {
		t.Fatalf("persistent home unexpectedly shadows operator's mise selection: %s", got)
	}
	if again := ensureAgentToolPath(map[string]string{"PATH": got})["PATH"]; again != got {
		t.Fatalf("normalization is not idempotent: %s != %s", again, got)
	}
}

func TestNativeToolPathPreservesProvisioningReferences(t *testing.T) {
	input := "${containerEnv:PATH}:${containerEnv:EXTRA_PATH}"
	got := NativeToolPath(input)
	if !strings.HasSuffix(got, input) {
		t.Fatalf("PATH reference changed before image expansion: %s", got)
	}
	resolved := expandEnvRefs(got, map[string]string{"PATH": "/usr/bin:/bin", "EXTRA_PATH": "/opt/operator/bin"})
	if !strings.HasSuffix(resolved, "/usr/bin:/bin:/opt/operator/bin") {
		t.Fatalf("lost operator paths during provisioning: %s", resolved)
	}
}
