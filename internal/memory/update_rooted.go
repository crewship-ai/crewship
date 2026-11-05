package memory

import (
	"errors"
	"fmt"
	"io/fs"
)

// UpdateFileRooted runs a locked read-modify-write on path with its parent
// pinned beneath storageRoot. The lock, the read and the publish all use one
// root handle, so a parent swapped while waiting for the lock cannot redirect
// any of them. A symlink at any component is refused. update receives the
// prior bytes and whether the file existed, and returns the new content.
func UpdateFileRooted(storageRoot, path string, update func(prior []byte, exists bool) ([]byte, error)) error {
	file, err := openRootedMutationFile(storageRoot, path, true)
	if err != nil {
		return fmt.Errorf("pin parent: %w", err)
	}
	defer file.close()

	lock := NewRootFileLock(file.root, file.name+".lock")
	if err := lock.Lock(); err != nil {
		return fmt.Errorf("lock: %w", err)
	}
	defer func() { _ = lock.Unlock() }()

	prior, readErr := file.read()
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		return fmt.Errorf("read: %w", readErr)
	}
	content, err := update(prior, readErr == nil)
	if err != nil {
		return err
	}
	if err := WriteFileDurableRoot(file.root, file.name, content, 0o644); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}
