//go:build unix

package skills

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestBulkImportRejectsOwnedFIFOWithoutWriter(t *testing.T) {
	root := t.TempDir()
	name := filepath.Join(root, "SKILL.md")
	if err := syscall.Mkfifo(name, 0600); err != nil {
		t.Fatal(err)
	}
	imp := newCovImporter(t)
	type outcome struct {
		result *BulkImportResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		r, e := imp.BulkImport(context.Background(), BulkImportRequest{LocalPath: root})
		done <- outcome{r, e}
	}()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.result.TotalImported != 0 || len(got.result.Skipped) != 1 {
			t.Fatalf("special file was not rejected: %+v", got.result)
		}
	case <-time.After(3 * time.Second):
		// Unblock an old regressed FIFO open using only the owned fixture, then fail.
		fd, err := syscall.Open(name, os.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			_ = syscall.Close(fd)
		}
		select {
		case <-done:
		case <-time.After(time.Second):
		}
		t.Fatal("FIFO open blocked before regular-file validation")
	}
}
