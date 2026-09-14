package main

import (
	"os"
	"strings"
	"testing"
)

// The sweeper is wired into the daemon, not only tested in isolation. A
// helper that deletes expired receipts is not retention until something in
// the production boot path runs it; this pins that `crewship start` does.
func TestStart_WiresTheRoutineReceiptRetentionSweeper(t *testing.T) {
	src, err := os.ReadFile("cmd_start.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "go pipeline.StartRoutineReceiptRetentionSweeper(ctx, deps.DB, logger,") {
		t.Fatal("cmd_start.go no longer starts pipeline.StartRoutineReceiptRetentionSweeper; expired routine " +
			"receipts would then persist forever and the documented dedup window would be a lie")
	}
}
