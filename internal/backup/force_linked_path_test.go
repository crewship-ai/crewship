//go:build linux

package backup_test

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/database"
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

// Links that no bundle path goes through are kept as links, and an absolute
// link naming the data directory still resolves after publication: the
// published directory has the same path as the original.
func TestForceRecoveryKeepsUnrelatedLinks(t *testing.T) {
	f := newInstanceFixture(t)
	bundle := f.create(t, true)
	parent := t.TempDir()
	data := filepath.Join(parent, "data")
	opts := backup.RecoverOptions{BundlePath: bundle.Path, Identities: []age.Identity{f.identity}, DataDir: data}
	if _, err := backup.RecoverInstance(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "disk2")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{
		"offsite":         outside,                          // another disk
		"alias-of-output": filepath.Join(data, "output"),    // absolute, back into this directory
		"relative-skills": "skills",                         // relative, inside
		"dangling":        filepath.Join(parent, "nowhere"), // nowhere
	}
	for name, to := range links {
		if err := os.Symlink(to, filepath.Join(data, name)); err != nil {
			t.Fatal(err)
		}
	}
	before := treeState(t, outside)
	opts.Force = true
	if _, err := backup.RecoverInstance(t.Context(), opts); err != nil {
		t.Fatalf("unrelated links must not block recovery: %v", err)
	}
	for name, to := range links {
		got, err := os.Readlink(filepath.Join(data, name))
		if err != nil || got != to {
			t.Errorf("%s: link = %q, %v; want %q", name, got, err, to)
		}
	}
	if resolved, err := filepath.EvalSymlinks(filepath.Join(data, "alias-of-output")); err != nil || resolved != filepath.Join(data, "output") {
		t.Errorf("absolute link resolves to %q, %v", resolved, err)
	}
	if got := treeState(t, outside); strings.Join(got, "\n") != strings.Join(before, "\n") {
		t.Errorf("directory behind a link changed: %v -> %v", before, got)
	}
}

// After a forced recovery the published directory opens the way the server
// opens it (the database writer, with its lease) and still holds the data.
func TestForceRecoveredInstanceOpensAsServerDatabase(t *testing.T) {
	f := newInstanceFixture(t)
	bundle := f.create(t, true)
	parent := t.TempDir()
	data := filepath.Join(parent, "data")
	opts := backup.RecoverOptions{BundlePath: bundle.Path, Identities: []age.Identity{f.identity}, DataDir: data}
	for i := 0; i < 2; i++ {
		if _, err := backup.RecoverInstance(t.Context(), opts); err != nil {
			t.Fatalf("recovery %d: %v", i, err)
		}
		opts.Force = true
		db, err := database.Open("file:"+filepath.Join(data, "crewship.db"), database.WithExclusiveWriter())
		if err != nil {
			t.Fatalf("server-style open after recovery %d: %v", i, err)
		}
		var n int
		if err := db.DB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM workspaces`).Scan(&n); err != nil || n != 2 {
			db.Close()
			t.Fatalf("workspaces = %d, %v", n, err)
		}
		var ok string
		if err := db.DB.QueryRowContext(t.Context(), `PRAGMA integrity_check`).Scan(&ok); err != nil || ok != "ok" {
			db.Close()
			t.Fatalf("integrity = %q, %v", ok, err)
		}
		db.Close()
	}
}
