package safepath

import (
	"errors"
	"path/filepath"
	"strings"
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

// legacyPathsafeJoin is internal/pathsafe.Join, copied verbatim from the
// revision before this package absorbed it. It is kept ONLY as a test oracle.
//
// A package collapse has exactly one way to be dangerous: the survivor accepts
// something the deleted one refused. Two call sites — the sidecar's memory HTTP
// write surface and Engine.ReindexPath — swapped from that function to JoinRel
// on trust, and reading both and concluding "JoinRel is stricter" is an
// argument, not a test. This makes it a test.
func legacyPathsafeJoin(root, rel string) (string, error) {
	if root == "" || rel == "" {
		return "", errLegacyUnsafe
	}
	if strings.ContainsRune(rel, 0) {
		return "", errLegacyUnsafe
	}
	if filepath.IsAbs(rel) {
		return "", errLegacyUnsafe
	}
	cleanRel := filepath.Clean(rel)
	sep := string(filepath.Separator)
	if cleanRel == ".." || strings.HasPrefix(cleanRel, ".."+sep) || strings.Contains(cleanRel, sep+".."+sep) {
		return "", errLegacyUnsafe
	}
	cleanRoot := filepath.Clean(root)
	joined := filepath.Clean(filepath.Join(cleanRoot, cleanRel))
	if joined != cleanRoot && !strings.HasPrefix(joined, cleanRoot+sep) {
		return "", errLegacyUnsafe
	}
	return joined, nil
}

var errLegacyUnsafe = errors.New("pathsafe: unsafe path")

// TestJoinRel_NeverLooserThanDeletedPathsafeJoin is the invariant that makes
// the collapse safe, checked over a cross-product of the segment shapes that
// break path guards rather than over a hand-picked list.
//
// Two things are asserted, and only these two, because JoinRel is deliberately
// allowed to be STRICTER (it refuses backslash components, which pathsafe.Join
// treated as an ordinary Linux filename byte):
//
//   - JoinRel never accepts an input pathsafe.Join rejected;
//   - where both accept, they return the identical path, so no call site that
//     swapped over silently started writing somewhere else.
func TestJoinRel_NeverLooserThanDeletedPathsafeJoin(t *testing.T) {
	t.Parallel()
	base := filepath.FromSlash("/srv/agent/.memory")

	segs := []string{
		"", ".", "..", "...", "....", " ", "a", "AGENT.md", "daily",
		`a\b`, `..\..`, "a\x00b", "\x00", "/", "//", "/etc", "etc/",
		"-", "~", "$HOME", ".hidden", "a b", "a..b", "..a", "a..",
	}
	var rels []string
	for _, a := range segs {
		rels = append(rels, a)
		for _, b := range segs {
			rels = append(rels, a+"/"+b)
			rels = append(rels, a+"/"+b+"/leaf.md")
		}
	}
	rels = append(rels,
		"../../etc/passwd", "daily/../../etc/passwd", "./AGENT.md",
		"daily/./x.md", "daily//x.md", "a/b/../../../escape",
		filepath.FromSlash("/srv/agent/.memory/../.memory-evil/x"),
	)

	var accepted int
	for _, rel := range rels {
		legacy, legacyErr := legacyPathsafeJoin(base, rel)
		got, err := JoinRel(base, rel)
		if err != nil {
			continue // stricter is allowed, and is the point of the collapse
		}
		accepted++
		if legacyErr != nil {
			t.Errorf("JoinRel(%q, %q) = %q, but the deleted pathsafe.Join refused it — "+
				"the collapse LOOSENED the guard", base, rel, got)
			continue
		}
		if got != legacy {
			t.Errorf("JoinRel(%q, %q) = %q, pathsafe.Join returned %q — result drifted",
				base, rel, got, legacy)
		}
	}
	// Guard the guard: if every input were rejected the loop above would be
	// vacuously green and would keep passing through any future tightening.
	if accepted < 20 {
		t.Fatalf("only %d of %d inputs were accepted; the corpus stopped exercising the happy path", accepted, len(rels))
	}
}
