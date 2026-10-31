//go:build unix

package pagebuild

import (
	"os"
	"syscall"
)

// matchDirOwner gives the record to the owner of page_projects_path when
// `crewship page enable` runs as root (sudo), so the server account that owns
// the directory can read the 0600 file. Anyone else keeps their own
// ownership: they could only write here if they own the directory anyway.
func matchDirOwner(path, dir string) error {
	if os.Geteuid() != 0 {
		return nil
	}
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	return os.Chown(path, int(st.Uid), int(st.Gid))
}

// matchFileOwner is matchDirOwner for an open file: the ownership change goes
// through the descriptor that was opened, never through a path.
func matchFileOwner(f *os.File, dir string) error {
	if os.Geteuid() != 0 {
		return nil
	}
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	return f.Chown(int(st.Uid), int(st.Gid))
}
