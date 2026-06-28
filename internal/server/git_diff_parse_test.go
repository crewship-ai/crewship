package server

import (
	"strings"
	"testing"
)

const tN = "CRWDIFFtestnonce" // marker prefix used by the tests

// TestParseGitDiff_NotRepo — the NOTREPO sentinel maps to is_repo:false so
// the UI shows "not a git repository" rather than an empty diff.
func TestParseGitDiff_NotRepo(t *testing.T) {
	got := parseGitDiff(tN+"NOTREPO\n", tN)
	if got["is_repo"] != false {
		t.Fatalf("is_repo = %v, want false", got["is_repo"])
	}
}

// TestParseGitDiff_FilesAndPatch — status + numstat merge per path, and the
// unified patch is captured verbatim under the DIFF marker.
func TestParseGitDiff_FilesAndPatch(t *testing.T) {
	out := tN + "STATUS\n" +
		"M\tinternal/api/inbox.go\n" +
		"A\tdocs/new.mdx\n" +
		"D\told/gone.go\n" +
		tN + "NUMSTAT\n" +
		"8\t1\tinternal/api/inbox.go\n" +
		"40\t0\tdocs/new.mdx\n" +
		"0\t12\told/gone.go\n" +
		tN + "DIFF\n" +
		"diff --git a/internal/api/inbox.go b/internal/api/inbox.go\n" +
		"@@ -1 +1 @@\n-old\n+new\n"
	got := parseGitDiff(out, tN)
	if got["is_repo"] != true {
		t.Fatalf("is_repo = %v, want true", got["is_repo"])
	}
	files, ok := got["files"].([]gitChangedFile)
	if !ok {
		t.Fatalf("files has unexpected type %T", got["files"])
	}
	if len(files) != 3 {
		t.Fatalf("len(files) = %d, want 3", len(files))
	}
	if files[0].Path != "internal/api/inbox.go" || files[0].Status != "modified" ||
		files[0].Additions != 8 || files[0].Deletions != 1 {
		t.Fatalf("file[0] = %+v", files[0])
	}
	if files[1].Status != "added" || files[1].Additions != 40 {
		t.Fatalf("file[1] = %+v", files[1])
	}
	if files[2].Status != "deleted" || files[2].Deletions != 12 {
		t.Fatalf("file[2] = %+v", files[2])
	}
	diff, _ := got["diff"].(string)
	if diff == "" || got["truncated"] != false {
		t.Fatalf("diff/truncated wrong: diff=%q truncated=%v", diff, got["truncated"])
	}
}

// TestParseGitDiff_MarkerCollisionImmune — a diff body that literally
// contains the bare (un-prefixed) marker words and even a context line
// equal to a fake marker must NOT corrupt parsing, now that markers carry a
// random nonce and are matched exactly.
func TestParseGitDiff_MarkerCollisionImmune(t *testing.T) {
	out := tN + "STATUS\n" +
		"M\troutes_container.go\n" +
		tN + "NUMSTAT\n" +
		"5\t2\troutes_container.go\n" +
		tN + "DIFF\n" +
		"diff --git a/routes_container.go b/routes_container.go\n" +
		"@@ -1 +1 @@\n" +
		"+const x = \"__DIFF__ and __NOTREPO__ in source\"\n" + // bare words in content
		" __NUMSTAT__\n" // a context line (leading space) equal to an old marker
	got := parseGitDiff(out, tN)
	if got["is_repo"] != true {
		t.Fatalf("is_repo = %v, want true (collision corrupted parse)", got["is_repo"])
	}
	files, _ := got["files"].([]gitChangedFile)
	if len(files) != 1 {
		t.Fatalf("len(files) = %d, want 1 — markers in diff body leaked into the file list", len(files))
	}
	if diff, _ := got["diff"].(string); !strings.Contains(diff, "in source") {
		t.Fatalf("diff body truncated by a fake marker: %q", diff)
	}
}

// TestParseGitDiff_Truncates — a patch over the cap is clipped and flagged.
func TestParseGitDiff_Truncates(t *testing.T) {
	var diffContent string
	for len(diffContent) <= gitDiffMaxBytes+5000 {
		diffContent += "+some added line of diff content here\n"
	}
	got := parseGitDiff(tN+"STATUS\n"+tN+"NUMSTAT\n"+tN+"DIFF\n"+diffContent, tN)
	if got["truncated"] != true {
		t.Fatalf("truncated = %v, want true", got["truncated"])
	}
	if diff, _ := got["diff"].(string); len(diff) > gitDiffMaxBytes {
		t.Fatalf("diff len = %d, want <= %d", len(diff), gitDiffMaxBytes)
	}
}
