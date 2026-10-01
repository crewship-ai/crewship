//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestArtifactConfinementAndBounds(t *testing.T) {
	t.Run("scratch only", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "result.txt"), []byte("own"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(root, ".codex"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".codex", "auth.json"), []byte("private"), 0600); err != nil {
			t.Fatal(err)
		}
		files, err := collectArtifacts(root)
		if err != nil || len(files) != 1 || files[0].Name != "result.txt" || string(files[0].Content) != "own" {
			t.Fatalf("files=%v err=%v", files, err)
		}
	})
	t.Run("symlink denied", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Symlink("/etc/passwd", filepath.Join(root, "foreign")); err != nil {
			t.Fatal(err)
		}
		if _, err := collectArtifacts(root); err == nil {
			t.Fatal("symlink admitted")
		}
	})
	t.Run("oversized denied", func(t *testing.T) {
		root := t.TempDir()
		file, err := os.Create(filepath.Join(root, "large"))
		if err != nil {
			t.Fatal(err)
		}
		if err = file.Truncate((1 << 20) + 1); err != nil {
			t.Fatal(err)
		}
		file.Close()
		if _, err := collectArtifacts(root); err == nil {
			t.Fatal("oversized admitted")
		}
	})
	t.Run("count denied", func(t *testing.T) {
		root := t.TempDir()
		for i := 0; i < 33; i++ {
			if err := os.WriteFile(filepath.Join(root, string(rune('A'+i))), nil, 0600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := collectArtifacts(root); err == nil {
			t.Fatal("count exceeded")
		}
	})
}
