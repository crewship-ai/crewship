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

// TestJoinRel_Safe is ported from internal/pathsafe (TestJoin_Safe), which
// JoinRel replaces. The cases are the memory-tree shapes the sidecar and the
// in-process dispatcher actually pass: a bare file, a nested file, and inputs
// carrying "./" noise that Clean removes.
func TestJoinRel_Safe(t *testing.T) {
	t.Parallel()
	base := filepath.FromSlash("/srv/agent/.memory")
	cases := []struct {
		rel  string
		want string
	}{
		{"AGENT.md", filepath.FromSlash("/srv/agent/.memory/AGENT.md")},
		{"daily/2026-07-09.md", filepath.FromSlash("/srv/agent/.memory/daily/2026-07-09.md")},
		{"peers/eva.md", filepath.FromSlash("/srv/agent/.memory/peers/eva.md")},
		{"./AGENT.md", filepath.FromSlash("/srv/agent/.memory/AGENT.md")},
		{"daily/./x.md", filepath.FromSlash("/srv/agent/.memory/daily/x.md")},
	}
	for _, c := range cases {
		got, err := JoinRel(base, c.rel)
		if err != nil {
			t.Fatalf("JoinRel(%q,%q) unexpected error: %v", base, c.rel, err)
		}
		if got != c.want {
			t.Errorf("JoinRel(%q,%q) = %q, want %q", base, c.rel, got, c.want)
		}
	}
}

// TestJoinRel_RejectsTraversal is ported from internal/pathsafe
// (TestJoin_RejectsTraversal). Every case must be refused: escaping the base,
// absolute paths, NUL smuggling, and traversal disguised inside a subdir.
func TestJoinRel_RejectsTraversal(t *testing.T) {
	t.Parallel()
	base := filepath.FromSlash("/srv/agent/.memory")
	bad := []string{
		"",
		"..",
		"../",
		"../../etc/passwd",
		"daily/../../etc/passwd",
		"daily/../../../root/.ssh/authorized_keys",
		"peers/../../secret",
		filepath.FromSlash("/etc/passwd"),
		filepath.FromSlash("/srv/agent/.memory/../.memory-evil/x"),
		"AGENT.md\x00.png",
		"daily/2026\x00.md",
	}
	for _, rel := range bad {
		got, err := JoinRel(base, rel)
		if err == nil {
			t.Errorf("JoinRel(%q,%q) = %q, want ErrUnsafe", base, rel, got)
			continue
		}
		if !errors.Is(err, ErrUnsafe) {
			t.Errorf("JoinRel(%q,%q) err = %v, want ErrUnsafe", base, rel, err)
		}
	}
}

// TestJoinRel_EmptyBaseRejected is ported from internal/pathsafe
// (TestJoin_EmptyRootRejected): a caller that forgot to configure its root
// must not get a relative path back that lands in the process CWD.
func TestJoinRel_EmptyBaseRejected(t *testing.T) {
	t.Parallel()
	if _, err := JoinRel("", "AGENT.md"); err == nil {
		t.Error("JoinRel with empty base should be rejected")
	}
}

// TestJoinRel_DotReturnsBase pins the one input where JoinRel returns the base
// itself. pathsafe.Join behaved this way and EnsureInside/JoinUnder both treat
// base as inside base, so JoinRel stays consistent rather than tightening.
// Callers that need a *file* must reject this themselves — Engine.ReindexPath
// does, explicitly.
func TestJoinRel_DotReturnsBase(t *testing.T) {
	t.Parallel()
	base := filepath.FromSlash("/srv/agent/.memory")
	got, err := JoinRel(base, ".")
	if err != nil {
		t.Fatalf("JoinRel(base, \".\") unexpected error: %v", err)
	}
	if got != base {
		t.Fatalf("JoinRel(base, \".\") = %q, want %q", got, base)
	}
}

// TestJoinRel_RejectsBackslashSegment is a deliberate behaviour delta against
// the pathsafe.Join it replaces: on Linux a backslash is an ordinary filename
// byte, and pathsafe.Join let "a\\b.md" through as one weird component.
// JoinRel runs every segment through ValidateComponent, which refuses it for
// the reason ValidateComponent documents — Windows shares and uploaded
// archives reach Linux containers, and a name that is one component here is
// two on the other side of that boundary.
func TestJoinRel_RejectsBackslashSegment(t *testing.T) {
	t.Parallel()
	base := filepath.FromSlash("/srv/agent/.memory")
	for _, rel := range []string{`a\b.md`, `daily\..\..\etc\passwd`, `daily/x\y.md`} {
		if got, err := JoinRel(base, rel); err == nil {
			t.Errorf("JoinRel(%q,%q) = %q, want ErrUnsafe", base, rel, got)
		}
	}
}

// TestJoinRel_SegmentsMatchValidateComponent is the invariant that lets this
// package have a single notion of "safe component": for any segment that
// survives filepath.Clean, JoinRel accepts it exactly when ValidateComponent
// does. Segments are placed FIRST in the input because Clean collapses a
// non-leading ".." together with the segment before it — "dir/.." is "." and
// legitimately resolves to base (see TestJoinRel_DotReturnsBase), so only the
// leading position exercises the segment check itself.
func TestJoinRel_SegmentsMatchValidateComponent(t *testing.T) {
	t.Parallel()
	base := filepath.FromSlash("/base")
	segs := []string{"ok", "abc123", "with.dot", "with-dash", "with_underscore", "..", `a\b`, "a\x00b"}
	for _, s := range segs {
		_, compErr := ValidateComponent(s)
		_, joinErr := JoinRel(base, s+"/leaf.md")
		if (compErr == nil) != (joinErr == nil) {
			t.Errorf("segment %q: ValidateComponent err=%v but JoinRel err=%v", s, compErr, joinErr)
		}
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
