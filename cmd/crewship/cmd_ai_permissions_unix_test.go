//go:build !windows

package main

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
)

// Confine the process-wide umask to a child so parallel tests cannot inherit it.
// The existing native-client matrix already checks success, rollback before and
// after rename, races, symlinks, ownership, retained settings and file modes.
func TestAIReconnectUnderRestrictiveUmask(t *testing.T) {
	if os.Getenv("AI_RECONNECT_UMASK_CHILD") == "1" {
		previous := syscall.Umask(0077)
		defer syscall.Umask(previous)
		TestAIReconnectFailurePreservesRegistration(t)
		return
	}
	child := exec.Command(os.Args[0], "-test.run=^TestAIReconnectUnderRestrictiveUmask$", "-test.count=1")
	child.Env = append(os.Environ(), "AI_RECONNECT_UMASK_CHILD=1")
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("umask 0077 reconnect matrix: %v\n%s", err, output)
	}
}
