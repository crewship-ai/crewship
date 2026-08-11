//go:build unix

package consolidate

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

func TestSnapshotPinsRefusesSymlinkedTarget(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim")
	const original = "outside\n"
	if err := os.WriteFile(victim, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(dir, "pins.md")); err != nil {
		t.Fatalf("plant pins symlink: %v", err)
	}

	_, err := snapshotPins(Config{OutputDir: dir}, []journal.Entry{{
		ID: "pin-1", Type: "test", Priority: journal.PriorityPin, Summary: "must stay confined",
	}})
	// Check the victim before the error: without either defense snapshotPins
	// can return nil after following the link, and the write-through is the
	// security invariant this test must report.
	got, readErr := os.ReadFile(victim)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != original {
		t.Fatalf("outside target changed to %q", got)
	}
	if err == nil {
		t.Fatal("snapshotPins accepted a symlinked target")
	}
}

func TestSnapshotPinsRefusesSymlinkedOutputDir(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	outputDir := filepath.Join(root, "topics")
	if err := os.Symlink(outside, outputDir); err != nil {
		t.Fatalf("plant output-directory symlink: %v", err)
	}

	_, err := snapshotPins(Config{OutputRoot: root, OutputDir: outputDir}, []journal.Entry{{
		ID: "pin-1", Type: "test", Priority: journal.PriorityPin, Summary: "must stay confined",
	}})
	if err == nil {
		t.Fatal("snapshotPins accepted a symlinked output directory")
	}
	if _, statErr := os.Lstat(filepath.Join(outside, "pins.md")); !os.IsNotExist(statErr) {
		t.Fatalf("outside directory received pins.md or stat failed unexpectedly: %v", statErr)
	}
}

func TestAppendRulesRefusesSymlinkedTarget(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 10, 15, 0, 0, 0, time.UTC)
	victim := filepath.Join(t.TempDir(), "victim")
	const original = "outside\n"
	if err := os.WriteFile(victim, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	name := "learned-" + now.Format("2006-01-02") + ".md"
	if err := os.Symlink(victim, filepath.Join(dir, name)); err != nil {
		t.Fatalf("plant learned-file symlink: %v", err)
	}

	c := &Consolidator{}
	_, _, err := c.appendRules(dir, now, []LearnedRule{{Pattern: "p", Action: "a"}})
	// An unrooted write can change the victim before the rooted readback
	// returns an error, so error wording must not hide the write-through.
	got, readErr := os.ReadFile(victim)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != original {
		t.Fatalf("outside target changed to %q", got)
	}
	if err == nil {
		t.Fatal("appendRules accepted a symlinked target")
	}
}
