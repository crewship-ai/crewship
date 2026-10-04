package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRootSkillWriterPreservesExistingFilesAndRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, slug := range []string{"", "..", "../outside", "a/b", `a\b`} {
		if path, err := WriteUniqueSkillFileRoot(root, dir, slug, []byte("unsafe")); err == nil || path != "" {
			t.Fatalf("unsafe slug %q accepted: %q, %v", slug, path, err)
		}
	}
	for i, content := range []string{"first review", "second review"} {
		path, err := WriteUniqueSkillFileRoot(root, dir, "review", []byte(content))
		if err != nil {
			t.Fatal(err)
		}
		want := "skill-review.md"
		if i == 1 {
			want = "skill-review-2.md"
		}
		if filepath.Base(path) != want {
			t.Fatalf("collision path = %q, want %q", path, want)
		}
		body, err := os.ReadFile(path)
		if err != nil || string(body) != content {
			t.Fatalf("staged content = %q, %v", body, err)
		}
	}
	body, err := root.ReadFile("skill-review.md")
	if err != nil || string(body) != "first review" {
		t.Fatalf("collision overwrote first review: %q, %v", body, err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteUniqueSkillFileRoot(root, dir, "closed", nil); err == nil || !strings.Contains(err.Error(), "open") {
		t.Fatalf("closed root = %v", err)
	}
	if _, err := WriteUniqueSkillFileRoot(nil, dir, "missing", nil); err == nil {
		t.Fatal("nil root accepted")
	}
}

func TestSkillWritersReportExhaustedNamesWithoutOverwriting(t *testing.T) {
	dir := t.TempDir()
	for i := 1; i < 100; i++ {
		name := "skill-review.md"
		if i > 1 {
			name = fmt.Sprintf("skill-review-%d.md", i)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte("retained review"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, write := range []func() (string, error){
		func() (string, error) { return WriteUniqueSkillFile(dir, "review", []byte("replacement")) },
		func() (string, error) { return WriteUniqueSkillFileRoot(root, dir, "review", []byte("replacement")) },
	} {
		if path, err := write(); path != "" || err == nil || !strings.Contains(err.Error(), "ran out of suffixes") {
			t.Fatalf("exhausted writer = %q, %v", path, err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 99 {
		t.Fatalf("existing reviews changed: %d, %v", len(entries), err)
	}
	for _, entry := range entries {
		body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil || string(body) != "retained review" {
			t.Fatalf("review overwritten: %s, %q, %v", entry.Name(), body, err)
		}
	}
}

func TestStageAuthoredSkillCannotPublishIntoAnUnavailableDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "occupied")
	if err := os.WriteFile(file, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := StageAuthoredSkill(file, validAuthoredSkill); err == nil {
		t.Fatal("regular file accepted as staging directory")
	}
	if _, err := WriteUniqueSkillFile(filepath.Join(file, "child"), "review", nil); err == nil {
		t.Fatal("writer accepted unavailable staging directory")
	}
}
