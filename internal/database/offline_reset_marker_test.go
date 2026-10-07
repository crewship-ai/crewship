package database

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOfflineResetMarkerFailClosedAndRetry(t *testing.T) {
	root := t.TempDir()
	if err := CheckResetPending(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "run"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := BeginOfflineReset(root, "own"); err != nil {
		t.Fatal(err)
	}
	if err := CheckResetPending(root); err == nil {
		t.Fatal("partial reset allows startup")
	}
	if err := BeginOfflineReset(root, "own"); err != nil {
		t.Fatal("same identity cannot retry", err)
	}
	if err := BeginOfflineReset(root, "foreign"); err == nil {
		t.Fatal("foreign identity can adopt reset")
	}
	if err := FinishOfflineReset(root); err != nil {
		t.Fatal(err)
	}
	if err := CheckResetPending(root); err != nil {
		t.Fatal(err)
	}
}
