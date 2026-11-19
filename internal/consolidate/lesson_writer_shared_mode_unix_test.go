//go:build unix

package consolidate

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestWriteCrewLesson_SharedTreeModes (#4777, #1623): the crew lesson is
// written host-side into the agent's setgid `.memory` tree. Its lockfile and
// lessons.md must be group-writable, and a missing tree is created 2775, or
// the next writer under another uid is refused until the container restarts.
func TestWriteCrewLesson_SharedTreeModes(t *testing.T) {
	old := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(old) })

	shared := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(shared, 0o775); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shared, os.ModeSetgid|0o775); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(shared); err != nil || fi.Mode()&os.ModeSetgid == 0 {
		t.Skip("filesystem does not keep the setgid bit on directories")
	}
	mem := filepath.Join(shared, ".memory")
	entry := LessonEntry{
		ID: "mission_outcome_m1", Kind: LessonKindNegative, Source: LessonSourceMissionOutcome,
		Rule: "ENG-1 failed", CapturedAt: time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC),
	}
	if err := WriteCrewLesson(context.Background(), mem, entry); err != nil {
		t.Fatalf("WriteCrewLesson: %v", err)
	}
	for _, tc := range []struct {
		rel  string
		want os.FileMode
	}{
		{".memory", os.ModeSetgid | 0o775},
		{".memory/lessons.md", 0o664},
		{".memory/lessons.md.lock", 0o660},
	} {
		fi, err := os.Lstat(filepath.Join(shared, tc.rel))
		if err != nil {
			t.Fatalf("%s: %v", tc.rel, err)
		}
		if got := fi.Mode() & (os.ModePerm | os.ModeSetgid); got != tc.want {
			t.Errorf("%s: mode %v, want %v", tc.rel, got, tc.want)
		}
	}
}
