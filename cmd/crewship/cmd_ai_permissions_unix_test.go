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
	args := []string{"-test.run=^TestAIReconnectUnderRestrictiveUmask$", "-test.count=1", "-test.v"}
	if testing.Short() {
		args = append(args, "-test.short")
	}
	// This is the same test binary, so a parent built with -race already
	// carries race instrumentation into the child; there is no -test.race flag.
	child := exec.Command(os.Args[0], args...)
	child.Env = append(os.Environ(), "AI_RECONNECT_UMASK_CHILD=1")
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("umask 0077 reconnect matrix: %v\n%s", err, output)
	}
}
