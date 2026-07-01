package docker

import (
	"strings"
	"testing"
)

// TestApplyAgentLoginPath_UsesCapturedPath: when a login PATH was captured at
// provision, it is used verbatim — it already contains the feature bin dirs.
func TestApplyAgentLoginPath_UsesCapturedPath(t *testing.T) {
	login := "/home/agent/.local/bin:/usr/local/py-utils/bin:/usr/local/bin:/usr/bin:/bin"
	env := []string{"CREWSHIP_CREW_ID=c1"}

	got := applyAgentLoginPath(env, login, map[string]string{"PATH": "/usr/local/bin:/usr/bin:/bin"})

	if v := envValue(got, "PATH"); v != login {
		t.Fatalf("PATH = %q, want captured login path %q", v, login)
	}
	if !strings.Contains(envValue(got, "PATH"), "/usr/local/py-utils/bin") {
		t.Error("resulting PATH must include /usr/local/py-utils/bin")
	}
}

// TestApplyAgentLoginPath_FallbackPrependsWellKnownDirs: capture failure (empty
// login PATH) falls back to prepending the well-known feature dirs onto the
// image PATH, without breaking — and without duplicating dirs already present.
func TestApplyAgentLoginPath_FallbackPrependsWellKnownDirs(t *testing.T) {
	imageEnv := map[string]string{"PATH": "/usr/local/bin:/usr/bin:/bin"}
	env := []string{"CREWSHIP_CREW_ID=c1"}

	got := applyAgentLoginPath(env, "", imageEnv)
	path := envValue(got, "PATH")

	for _, dir := range wellKnownDevcontainerBinDirs {
		if !strings.Contains(path, dir) {
			t.Errorf("fallback PATH %q missing well-known dir %q", path, dir)
		}
	}
	// Well-known dirs lead; the image PATH is preserved as the tail.
	if !strings.HasSuffix(path, "/usr/local/bin:/usr/bin:/bin") {
		t.Errorf("fallback PATH must preserve image PATH tail, got %q", path)
	}
	if !strings.HasPrefix(path, "/usr/local/py-utils/bin:") {
		t.Errorf("well-known dirs must be prepended, got %q", path)
	}
}

// TestApplyAgentLoginPath_FallbackNilImageEnv: a nil image env (inspect failed)
// must not panic and still yields a usable PATH with the feature dirs.
func TestApplyAgentLoginPath_FallbackNilImageEnv(t *testing.T) {
	got := applyAgentLoginPath([]string{"CREWSHIP_CREW_ID=c1"}, "", nil)
	path := envValue(got, "PATH")
	if !strings.Contains(path, "/usr/local/py-utils/bin") {
		t.Errorf("PATH must include feature dir even with nil image env, got %q", path)
	}
	if !strings.HasSuffix(path, defaultAgentPath) {
		t.Errorf("PATH must fall back to defaultAgentPath tail, got %q", path)
	}
}

// TestApplyAgentLoginPath_ReplacesExistingPath: an existing containerEnv PATH
// entry is replaced (not duplicated) by the captured login PATH.
func TestApplyAgentLoginPath_ReplacesExistingPath(t *testing.T) {
	env := []string{"PATH=/old/bin", "FOO=bar"}
	login := "/usr/local/py-utils/bin:/usr/bin:/bin"

	got := applyAgentLoginPath(env, login, nil)

	n := 0
	for _, e := range got {
		if strings.HasPrefix(e, "PATH=") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("expected exactly one PATH entry, got %d: %v", n, got)
	}
	if envValue(got, "PATH") != login {
		t.Errorf("PATH = %q, want %q", envValue(got, "PATH"), login)
	}
	if envValue(got, "FOO") != "bar" {
		t.Error("unrelated env entries must be preserved")
	}
}

// TestFallbackAgentPath_DedupesPresentDir: a well-known dir already on the base
// PATH is not prepended again.
func TestFallbackAgentPath_DedupesPresentDir(t *testing.T) {
	base := "/home/agent/.local/bin:/usr/local/bin:/usr/bin"
	got := fallbackAgentPath(base, "")
	if strings.Count(got, "/home/agent/.local/bin") != 1 {
		t.Errorf("/home/agent/.local/bin must appear once, got %q", got)
	}
}
