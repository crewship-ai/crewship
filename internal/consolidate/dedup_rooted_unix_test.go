//go:build unix

package consolidate

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDedupTrustedStorageRootRefusesReplacedOutputParent(t *testing.T) {
	root := t.TempDir()
	topics := filepath.Join(root, "topics")
	if err := os.Mkdir(topics, 0755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	name := "learned-" + now.Format("2006-01-02") + ".md"
	pattern := "private outside pattern must not suppress output"
	if err := os.WriteFile(filepath.Join(topics, name), []byte("- **Pattern:** "+pattern+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	rules := []LearnedRule{{Pattern: pattern}}
	if got := dedupAgainstPriorRooted(rules, root, topics, now, 7*24*time.Hour); len(got) != 0 {
		t.Fatal("healthy rooted dedup did not read authorized prior")
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, name), []byte("- **Pattern:** "+pattern+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(topics, topics+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, topics); err != nil {
		t.Fatal(err)
	}
	if got := dedupAgainstPriorRooted(rules, root, topics, now, 7*24*time.Hour); len(got) != 1 {
		t.Fatal("outside prior affected dedup after parent swap")
	}
	if got := dedupAgainstPriorRooted(rules, "", topics, now, 7*24*time.Hour); len(got) != 1 {
		t.Fatal("empty independent root fell back to OutputDir")
	}
}
