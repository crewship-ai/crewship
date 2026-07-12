package cli

import (
	"runtime"
	"testing"
)

// TestShellCommand locks the platform dispatch for --with-cmd: the Windows
// crewship-cli build (#945) has no `sh` on PATH, so the historical
// unconditional `sh -c` fails there — cmd.exe's /c is the equivalent.
func TestShellCommand(t *testing.T) {
	name, args := shellCommand("echo hi")
	if runtime.GOOS == "windows" {
		if name != "cmd" || len(args) != 2 || args[0] != "/c" || args[1] != "echo hi" {
			t.Errorf("shellCommand on windows = %q %v, want cmd [/c \"echo hi\"]", name, args)
		}
		return
	}
	if name != "sh" || len(args) != 2 || args[0] != "-c" || args[1] != "echo hi" {
		t.Errorf("shellCommand = %q %v, want sh [-c \"echo hi\"]", name, args)
	}
}
