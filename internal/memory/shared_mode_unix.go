//go:build unix

package memory

import (
	"fmt"
	"os"
)

// chmodDirNoFollow changes the mode of dir through a descriptor opened with
// O_NOFOLLOW|O_DIRECTORY, so a symlink swapped in after the mkdir cannot
// redirect the chmod onto another file.
func chmodDirNoFollow(dir string, mode os.FileMode) error {
	d, err := openConfinedDir(dir)
	if err != nil {
		return fmt.Errorf("open shared dir %s: %w", dir, err)
	}
	defer func() { _ = d.close() }()
	if err := d.file.Chmod(mode); err != nil {
		return fmt.Errorf("chmod shared dir %s: %w", dir, err)
	}
	return nil
}

// shareConfinedChild sets MemoryDirPerm on child, a directory just created
// beneath parent, when parent is in the shared tree.
func shareConfinedChild(parent, child *confinedDir) error {
	fi, err := parent.file.Stat()
	if err != nil || !isSharedDir(fi) {
		return err
	}
	return child.file.Chmod(MemoryDirPerm)
}
