package pathsafe

import (
	"path/filepath"
	"testing"
)

func TestJoin_Safe(t *testing.T) {
	root := filepath.FromSlash("/srv/agent/.memory")
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
		got, err := Join(root, c.rel)
		if err != nil {
			t.Fatalf("Join(%q,%q) unexpected error: %v", root, c.rel, err)
		}
		if got != c.want {
			t.Errorf("Join(%q,%q) = %q, want %q", root, c.rel, got, c.want)
		}
	}
}

func TestJoin_RejectsTraversal(t *testing.T) {
	root := filepath.FromSlash("/srv/agent/.memory")
	// Every one of these must be refused: escaping the root, absolute
	// paths, NUL smuggling, and traversal disguised inside a subdir.
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
		if got, err := Join(root, rel); err == nil {
			t.Errorf("Join(%q,%q) = %q, want ErrUnsafePath", root, rel, got)
		}
	}
}

func TestJoin_EmptyRootRejected(t *testing.T) {
	if _, err := Join("", "AGENT.md"); err == nil {
		t.Error("Join with empty root should be rejected")
	}
}

func TestJoin_NoSiblingPrefixEscape(t *testing.T) {
	// root="/a/b" must not admit a sibling like "/a/bevil" — the
	// separator-anchored prefix check is what prevents this. Since Join
	// only accepts relative rel, exercise the boundary via Under, which
	// shares the rule.
	root := filepath.FromSlash("/a/b")
	if Under(root, filepath.FromSlash("/a/bevil/x")) {
		t.Error("Under admitted a sibling directory sharing a name prefix")
	}
	if !Under(root, filepath.FromSlash("/a/b/x")) {
		t.Error("Under rejected a legitimate descendant")
	}
	if !Under(root, root) {
		t.Error("Under rejected the root itself")
	}
}
