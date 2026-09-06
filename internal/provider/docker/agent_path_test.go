package docker

import (
	"strings"
	"testing"
)

// TestApplyAgentLoginPath_UsesCapturedPath: when a login PATH was captured at
// provision, it is the base of the result; the well-known dirs it lacks are
// prepended (a login shell does not see mise's shims either).
func TestApplyAgentLoginPath_UsesCapturedPath(t *testing.T) {
	login := "/home/agent/.local/bin:/usr/local/py-utils/bin:/usr/local/bin:/usr/bin:/bin"
	env := []string{"CREWSHIP_CREW_ID=c1"}

	got := applyAgentLoginPath(env, login, map[string]string{"PATH": "/usr/local/bin:/usr/bin:/bin"})

	// The captured PATH is the base, kept intact at the end; the well-known
	// dirs it lacks (npm-global, mise shims) are put in front, the ones it
	// already has (~/.local/bin, py-utils) are not duplicated.
	// Every captured dir is present, each dir exactly once, the well-known
	// dirs first.
	v := envValue(got, "PATH")
	for _, d := range strings.Split(login, ":") {
		if !strings.Contains(":"+v+":", ":"+d+":") {
			t.Fatalf("PATH = %q lacks captured dir %q", v, d)
		}
	}
	seen := map[string]int{}
	for _, d := range strings.Split(v, ":") {
		seen[d]++
		if seen[d] > 1 {
			t.Errorf("PATH = %q repeats %q", v, d)
		}
	}
	if !strings.HasPrefix(v, "/usr/local/py-utils/bin:/usr/local/share/npm-global/bin:/home/agent/.local/bin:/opt/crewship/bin:/opt/mise/data/shims:") {
		t.Errorf("PATH = %q: well-known dirs must lead", v)
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

// TestApplyAgentLoginPath_MergesExistingPath: the containerEnv PATH already in
// env (the build's aggregated PATH: tool dirs, feature dirs, the image's) is
// kept, exactly once, ahead of the captured login PATH's extra dirs.
func TestApplyAgentLoginPath_MergesExistingPath(t *testing.T) {
	env := []string{"PATH=/opt/feature/bin:/usr/bin", "FOO=bar"}
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
	want := "/usr/local/py-utils/bin:/usr/local/share/npm-global/bin:/home/agent/.local/bin:/opt/crewship/bin:/opt/mise/data/shims:/opt/feature/bin:/usr/bin:/bin"
	if v := envValue(got, "PATH"); v != want {
		t.Errorf("PATH = %q\n   want %q", v, want)
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
