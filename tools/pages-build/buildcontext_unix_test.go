//go:build unix

package pageprofile

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// The image's Dockerfile COPYs the build context and then switches to an
// unprivileged USER; COPY keeps each file's mode, so a context file that is
// not world-readable becomes unreadable for the compiler at run time
// (EACCES on /opt/pages/build.mjs). Hosts and service units with umask 077
// are common, and os.WriteFile's mode is filtered by the umask, so the
// written context must not depend on it. Found by the real-Docker acceptance
// of `crewship page enable` on a umask-077 host (#4497).
func TestWriteBuildContextIsWorldReadableUnderRestrictiveUmask(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)

	dir := t.TempDir()
	if err := WriteBuildContext(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range buildContextFiles {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o644 {
			t.Errorf("%s has mode %04o under umask 077, want 0644 (the image's unprivileged USER must read it)", name, got)
		}
	}
}
