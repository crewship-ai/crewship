package main

import "testing"

func TestValidatePromptName(t *testing.T) {
	good := []string{"review", "review-go", "v1.2-deploy", "deploy_step_2", "abc123"}
	for _, n := range good {
		if err := validatePromptName(n); err != nil {
			t.Errorf("expected %q to be valid, got: %v", n, err)
		}
	}
	bad := map[string]string{
		"":              "empty",
		"../etc/passwd": "path traversal slashes",
		"foo/bar":       "slash",
		"foo bar":       "space",
		".hidden":       "leading dot",
		".":             "single dot",
		"..":            "double dot",
		"name with$":    "shell metachar",
		"name\nnewline": "newline",
	}
	for n, why := range bad {
		if err := validatePromptName(n); err == nil {
			t.Errorf("expected %q (%s) to fail validation", n, why)
		}
	}
}

func TestValidatePromptName_LengthBound(t *testing.T) {
	long := make([]byte, 65)
	for i := range long {
		long[i] = 'a'
	}
	if err := validatePromptName(string(long)); err == nil {
		t.Error("expected 65-char name to fail")
	}
}

func TestPromptPath_StaysInDir(t *testing.T) {
	// promptPath builds via validatePromptName then filepath.Join. Even
	// if a validator regression let "../" through, we want a sanity test
	// that exercises the assembly path.
	p, err := promptPath("review")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// We can't easily assert a literal path (depends on $HOME), but the
	// filename component must be exactly "review.md".
	if got := lastPathSegment(p); got != "review.md" {
		t.Errorf("filename: got %q want %q", got, "review.md")
	}
}

func lastPathSegment(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[i+1:]
		}
	}
	return p
}
