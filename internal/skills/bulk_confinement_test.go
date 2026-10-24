package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// All targets belong to this test; no host file or live repository is read.
func TestBulkImportConfinementOwnedFixtures(t *testing.T) {
	for _, kind := range []string{"outside-relative", "outside-absolute", "inside-relative"} {
		t.Run(kind, func(t *testing.T) {
			parent := t.TempDir()
			root := filepath.Join(parent, "clone")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(parent, "owned-target.md")
			name := "outside-owned-fixture"
			want := 0
			if kind == "inside-relative" {
				target = filepath.Join(root, "owned-target.md")
				name = "inside-owned-fixture"
				want = 1
			}
			if err := os.WriteFile(target, []byte(skillMD(name, "MIT")), 0600); err != nil {
				t.Fatal(err)
			}
			link := target
			if kind != "outside-absolute" {
				var err error
				link, err = filepath.Rel(root, target)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(link, filepath.Join(root, "SKILL.md")); err != nil {
				t.Fatal(err)
			}
			imp := newCovImporter(t)
			result, err := imp.BulkImport(context.Background(), BulkImportRequest{LocalPath: root})
			if err != nil {
				t.Fatal(err)
			}
			if result.TotalImported != want {
				t.Errorf("imported=%d want=%d; SKILL.md must remain in the opened root", result.TotalImported, want)
			}
			var count int
			if err := imp.db.QueryRow("SELECT COUNT(*) FROM skills WHERE name = ?", name).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != want {
				t.Errorf("persisted owned target count=%d want=%d", count, want)
			}
		})
	}
}

func TestBulkImportConfinementSizeBoundary(t *testing.T) {
	for _, extra := range []int{0, 1} {
		t.Run(string(rune('0'+extra)), func(t *testing.T) {
			root := t.TempDir()
			body := skillMD("owned-sized-fixture", "MIT")
			n := int(maxSkillFileBytes) + extra - len(body)
			body += strings.Repeat("x\n", n/2) + strings.Repeat("x", n%2)
			if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			result, err := newCovImporter(t).BulkImport(context.Background(), BulkImportRequest{LocalPath: root})
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if extra > 0 {
				want = 0
			}
			if result.TotalImported != want {
				t.Fatalf("size=%d imported=%d want=%d", len(body), result.TotalImported, want)
			}
		})
	}
}
