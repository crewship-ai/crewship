package main

import (
	"os"
	"strings"
	"testing"
)

// The inbox, chats and Keeper decision windows are only enforced by the
// sweeper cmd_start.go launches. Without it an administrator could set a
// window in Admin › Data retention and nothing would ever be deleted.
func TestStart_WiresTheRetentionSweeper(t *testing.T) {
	src, err := os.ReadFile("cmd_start.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "go retention.StartSweeper(ctx, deps.DB, logger,") {
		t.Fatal("cmd_start.go no longer starts retention.StartSweeper; inbox, chats and Keeper decision " +
			"windows would be saved and never enforced")
	}
}
