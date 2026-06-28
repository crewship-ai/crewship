package server

import (
	"testing"
)

// TestParseGitDiff_NotRepo — the __NOTREPO__ sentinel maps to is_repo:false
// so the UI shows "not a git repository" rather than an empty diff.
func TestParseGitDiff_NotRepo(t *testing.T) {
	got := parseGitDiff("__NOTREPO__\n")
	if got["is_repo"] != false {
		t.Fatalf("is_repo = %v, want false", got["is_repo"])
	}
}

// TestParseGitDiff_FilesAndPatch — status + numstat merge per path, and the
// unified patch is captured verbatim under the __DIFF__ marker.
func TestParseGitDiff_FilesAndPatch(t *testing.T) {
	out := `__STATUS__
M	internal/api/inbox.go
A	docs/new.mdx
D	old/gone.go
__NUMSTAT__
8	1	internal/api/inbox.go
40	0	docs/new.mdx
0	12	old/gone.go
__DIFF__
diff --git a/internal/api/inbox.go b/internal/api/inbox.go
@@ -1 +1 @@
-old
+new
`
	got := parseGitDiff(out)
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
	// First file: modified, 8/1.
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

// TestParseGitDiff_Truncates — a patch over the cap is clipped and flagged.
func TestParseGitDiff_Truncates(t *testing.T) {
	var diffContent string
	for len(diffContent) <= gitDiffMaxBytes+5000 {
		diffContent += "+some added line of diff content here\n"
	}
	got := parseGitDiff("__STATUS__\n__NUMSTAT__\n__DIFF__\n" + diffContent)
	if got["truncated"] != true {
		t.Fatalf("truncated = %v, want true", got["truncated"])
	}
	if diff, _ := got["diff"].(string); len(diff) > gitDiffMaxBytes {
		t.Fatalf("diff len = %d, want <= %d", len(diff), gitDiffMaxBytes)
	}
}
