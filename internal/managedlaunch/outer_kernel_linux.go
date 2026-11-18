//go:build linux

package managedlaunch

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

// The launcher checks its own kernel state after immutable artifact validation,
// under the existing run PID/starttime and stop ownership, immediately before
// execveat. No shell, environment variable or image utility supplies this proof.
func assertOuterKernelControls() error {
	raw, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return errors.New("Codex outer isolation: kernel status unavailable")
	}
	return validateOuterKernelControls(os.Getuid(), os.Geteuid(), os.Getgid(), os.Getegid(), string(raw))
}
func validateOuterKernelControls(uid, euid, gid, egid int, raw string) error {
	denied := errors.New("Codex outer isolation: effective UID, no-new-privileges, seccomp or capability controls are unqualified")
	if uid != 1001 || euid != 1001 || gid != 1001 || egid != 1001 || len(raw) > 64<<10 {
		return denied
	}
	required := map[string]bool{"NoNewPrivs": false, "Seccomp": false, "CapInh": false, "CapPrm": false, "CapEff": false, "CapBnd": false, "CapAmb": false}
	for _, line := range strings.Split(raw, "\n") {
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		seen, needed := required[name]
		if !needed {
			continue
		}
		if seen {
			return denied
		}
		fields := strings.Fields(value)
		if len(fields) != 1 {
			return denied
		}
		switch name {
		case "NoNewPrivs":
			if fields[0] != "1" {
				return denied
			}
		case "Seccomp":
			if fields[0] != "2" {
				return denied
			}
		default:
			v, err := strconv.ParseUint(fields[0], 16, 64)
			if err != nil || v != 0 {
				return denied
			}
		}
		required[name] = true
	}
	for _, seen := range required {
		if !seen {
			return denied
		}
	}
	return nil
}
