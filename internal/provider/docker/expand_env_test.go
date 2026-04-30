package docker

import "testing"

func TestExpandVarRefs_PathChain(t *testing.T) {
	vars := map[string]string{
		"PATH": "/usr/bin:/bin",
		"HOME": "/home/agent",
	}
	got := expandVarRefs("/usr/local/cargo/bin:${PATH}", vars)
	want := "/usr/local/cargo/bin:/usr/bin:/bin"
	if got != want {
		t.Fatalf("expand: got %q, want %q", got, want)
	}
}

func TestExpandVarRefs_ContainerEnvPrefix(t *testing.T) {
	vars := map[string]string{"PATH": "/usr/bin"}
	got := expandVarRefs("${containerEnv:PATH}/extra", vars)
	want := "/usr/bin/extra"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestExpandVarRefs_UnknownVarPreserved(t *testing.T) {
	got := expandVarRefs("${UNDEFINED}/x", map[string]string{})
	want := "${UNDEFINED}/x"
	if got != want {
		t.Fatalf("got %q want %q (unknown vars must round-trip)", got, want)
	}
}

func TestExpandVarRefs_NoBraces_NoChange(t *testing.T) {
	in := "/usr/bin:/bin"
	if got := expandVarRefs(in, map[string]string{"PATH": "X"}); got != in {
		t.Fatalf("plain string changed: %q", got)
	}
}

func TestExpandVarRefs_BareDollarNotExpanded(t *testing.T) {
	// $PATH (no braces) is intentionally NOT expanded — avoid stomping
	// regexes / shell scripts that legitimately contain literal $X.
	in := "$PATH/extra"
	if got := expandVarRefs(in, map[string]string{"PATH": "X"}); got != in {
		t.Fatalf("bare $VAR was expanded: %q", got)
	}
}

func TestExpandVarRefs_UnterminatedTokenRoundTrips(t *testing.T) {
	in := "abc${PATH" // no closing brace
	if got := expandVarRefs(in, map[string]string{"PATH": "X"}); got != in {
		t.Fatalf("unterminated token mangled: %q", got)
	}
}

func TestExpandVarRefs_MultipleTokens(t *testing.T) {
	vars := map[string]string{"HOME": "/home/agent", "PATH": "/usr/bin"}
	got := expandVarRefs("${HOME}/.local/bin:${PATH}", vars)
	want := "/home/agent/.local/bin:/usr/bin"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestExpandContainerEnv_SmokeTest(t *testing.T) {
	imageEnv := map[string]string{"PATH": "/usr/bin:/bin"}
	in := []string{
		"CREWSHIP_CREW_ID=abc",              // no expansion needed
		"PATH=/usr/local/cargo/bin:${PATH}", // primary fix scenario
		"NODE_VERSION=22.22.2",              // unrelated
		"BAD=${UNDEFINED}",                  // unknown — preserved
	}
	out := expandContainerEnv(in, imageEnv)
	wantPath := "PATH=/usr/local/cargo/bin:/usr/bin:/bin"
	if out[1] != wantPath {
		t.Errorf("PATH not expanded: got %q want %q", out[1], wantPath)
	}
	if out[3] != "BAD=${UNDEFINED}" {
		t.Errorf("unknown var should preserve, got %q", out[3])
	}
	if out[0] != in[0] || out[2] != in[2] {
		t.Errorf("unrelated vars mutated")
	}
}

func TestExpandContainerEnv_NoImageEnvIsNoop(t *testing.T) {
	in := []string{"PATH=/x:${PATH}"}
	out := expandContainerEnv(in, nil)
	if out[0] != in[0] {
		t.Fatalf("with empty imageEnv expansion should be a no-op, got %q", out[0])
	}
}

func TestExpandContainerEnv_MalformedEntryPasses(t *testing.T) {
	in := []string{"NOEQUALSSIGN", "GOOD=${PATH}"}
	out := expandContainerEnv(in, map[string]string{"PATH": "X"})
	if out[0] != "NOEQUALSSIGN" {
		t.Fatalf("malformed entry was rewritten: %q", out[0])
	}
	if out[1] != "GOOD=X" {
		t.Fatalf("good entry not expanded: %q", out[1])
	}
}
