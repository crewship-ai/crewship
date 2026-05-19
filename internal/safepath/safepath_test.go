package safepath

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestValidateComponent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in      string
		wantErr bool
	}{
		{"ok", false},
		{"abc123", false},
		{"with.dot", false},
		{"with-dash", false},
		{"with_underscore", false},
		{"", true},
		{".", true},
		{"..", true},
		{"a/b", true},
		{`a\b`, true},
		{"a\x00b", true},
		{"../etc", true},
		{"./local", true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			_, err := ValidateComponent(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateComponent(%q) err=%v wantErr=%v", tc.in, err, tc.wantErr)
			}
			if tc.wantErr && err != nil && !errors.Is(err, ErrUnsafe) {
				t.Fatalf("expected ErrUnsafe, got %v", err)
			}
		})
	}
}

func TestJoinUnder(t *testing.T) {
	t.Parallel()
	base := filepath.Join(string(filepath.Separator), "var", "lib", "crewship")

	got, err := JoinUnder(base, "workspaces", "abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(base, "workspaces", "abc123")
	if got != want {
		t.Fatalf("JoinUnder = %q, want %q", got, want)
	}

	if _, err := JoinUnder(base, "..", "etc"); err == nil {
		t.Fatal("expected error for traversal component")
	}
	if _, err := JoinUnder(base, "ok", "with/slash"); err == nil {
		t.Fatal("expected error for separator in component")
	}
}

func TestEnsureInside(t *testing.T) {
	t.Parallel()
	base := filepath.Join(string(filepath.Separator), "base")
	if err := EnsureInside(base, filepath.Join(base, "child")); err != nil {
		t.Fatalf("expected child to be inside: %v", err)
	}
	if err := EnsureInside(base, base); err != nil {
		t.Fatalf("expected base to be inside itself: %v", err)
	}
	if err := EnsureInside(base, filepath.Join(base, "..", "evil")); err == nil {
		t.Fatal("expected escape to fail")
	}
}

func TestCleanAbs(t *testing.T) {
	t.Parallel()
	base := filepath.Join(string(filepath.Separator), "base")
	abs := filepath.Join(string(filepath.Separator), "etc", "passwd")
	got, err := CleanAbs(base, abs)
	if err != nil {
		t.Fatalf("absolute path should pass: %v", err)
	}
	if got != abs {
		t.Fatalf("CleanAbs(abs) = %q, want %q", got, abs)
	}
	if _, err := CleanAbs(base, "../escape"); err == nil {
		t.Fatal("relative traversal should fail")
	}
	if _, err := CleanAbs(base, "with\x00nul"); err == nil {
		t.Fatal("NUL in path should fail")
	}
}
