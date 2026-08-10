//go:build unix

package consolidate

import (
	"os"
	"path/filepath"
	"strings"
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
		t.Skipf("symlink unsupported: %v", err)
	}

	_, err := snapshotPins(Config{OutputDir: dir}, []journal.Entry{{
		ID: "pin-1", Type: "test", Priority: journal.PriorityPin, Summary: "must stay confined",
	}})
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("snapshotPins symlink error = %v, want explicit refusal", err)
	}
	got, readErr := os.ReadFile(victim)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != original {
		t.Fatalf("outside target changed to %q", got)
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
		t.Skipf("symlink unsupported: %v", err)
	}

	c := &Consolidator{}
	_, _, err := c.appendRules(dir, now, []LearnedRule{{Pattern: "p", Action: "a"}})
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("appendRules symlink error = %v, want explicit refusal", err)
	}
	got, readErr := os.ReadFile(victim)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != original {
		t.Fatalf("outside target changed to %q", got)
	}
}
