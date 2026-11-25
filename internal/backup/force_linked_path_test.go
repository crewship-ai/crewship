package backup_test

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
)

// treeState records every entry's kind, mode, size, times and content (a
// link by its own text) so a test can prove a tree is byte-for-byte and
// time-for-time unchanged.
func treeState(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		line := fmt.Sprintf("%s|%v|%d|%d", rel, info.Mode(), info.Size(), info.ModTime().UnixNano())
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			to, _ := os.Readlink(path)
			line += "|->" + to
		case info.Mode().IsRegular():
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			line += fmt.Sprintf("|%x", sha256.Sum256(b))
		}
		out = append(out, line)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// A link inside the existing data directory must never let forced recovery
// write outside its private stage, whatever the link points at.
func TestForceRecoveryRefusesBundlePathsThroughLinks(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("force publication requires Linux")
	}
	cases := []struct {
		name string
		// plant replaces the directory at link with a symlink and returns
		// the directory that must stay untouched besides the target.
		link   string
		target func(parent, data string) string
	}{
		{"parent is an absolute link out", "output/crews", func(parent, data string) string { return filepath.Join(parent, "outside") }},
		{"parent links back into the original directory", "output/crews", func(parent, data string) string { return filepath.Join(data, "memory") }},
		{"parent is a relative link out", "output/crews", func(parent, data string) string { return "../../outside" }},
		{"parent links inside the stage", "output/crews", func(parent, data string) string { return "../skills" }},
		{"last folder is a link", "output/crews/c_lab", func(parent, data string) string { return filepath.Join(parent, "outside") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newInstanceFixture(t)
			bundle := f.create(t, true)
			parent := t.TempDir()
			data := filepath.Join(parent, "data")
			opts := backup.RecoverOptions{BundlePath: bundle.Path, Identities: []age.Identity{f.identity}, DataDir: data}
			if _, err := backup.RecoverInstance(t.Context(), opts); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(parent, "outside")
			if err := os.Mkdir(outside, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(outside, "keep.txt"), []byte("operator data"), 0o600); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(data, filepath.FromSlash(tc.link))
			if err := os.RemoveAll(link); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(tc.target(parent, data), link); err != nil {
				t.Fatal(err)
			}
			beforeOutside, beforeData := treeState(t, outside), treeState(t, data)

			opts.Force = true
			_, err := backup.RecoverInstance(t.Context(), opts)
			if err == nil {
				t.Fatal("forced recovery went through a link")
			}
			if !strings.Contains(err.Error(), "link") {
				t.Errorf("error does not name the link: %v", err)
			}
			if got := treeState(t, outside); strings.Join(got, "\n") != strings.Join(beforeOutside, "\n") {
				t.Errorf("directory outside the installation changed:\nbefore %v\nafter  %v", beforeOutside, got)
			}
			if got := treeState(t, data); strings.Join(got, "\n") != strings.Join(beforeData, "\n") {
				t.Errorf("original data directory changed:\nbefore %v\nafter  %v", beforeData, got)
			}
			if names, _ := filepath.Glob(filepath.Join(parent, ".crewship-recovery-*")); len(names) != 0 {
				t.Errorf("staging left behind: %v", names)
			}
		})
	}
}
