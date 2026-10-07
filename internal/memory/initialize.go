package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
)

// InitializeFile durably creates a missing memory file without replacing any
// existing entry. Parent descriptors remain pinned under storageRoot; symlink
// parents and leaves are refused even when they point inside that root.
func InitializeFile(ctx context.Context, storageRoot, path string, content []byte) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if storageRoot == "" {
		return false, fmt.Errorf("memory initialization requires a storage root")
	}
	f, err := openRootedMutationFile(storageRoot, path, true)
	if err != nil {
		return false, err
	}
	defer f.close()
	if info, err := f.root.Lstat(f.name); err == nil {
		if !info.Mode().IsRegular() {
			return false, os.ErrPermission
		}
		return false, syncInitializationDirectory(f.root)
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return false, err
	}
	tmp := f.name + ".tmp." + hex.EncodeToString(nonce[:])
	file, err := f.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o664)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.root.Remove(tmp) }()
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return false, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return false, err
	}
	if err := file.Close(); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	// Hard-link publication atomically fails if any competing writer created
	// the destination. Rename would replace that writer's content.
	if err := f.root.Link(tmp, f.name); err != nil {
		if errors.Is(err, os.ErrExist) {
			info, statErr := f.root.Lstat(f.name)
			if statErr != nil {
				return false, statErr
			}
			if !info.Mode().IsRegular() {
				return false, os.ErrPermission
			}
			return false, syncInitializationDirectory(f.root)
		}
		return false, err
	}
	if err := f.root.Remove(tmp); err != nil {
		return false, err
	}
	if err := syncInitializationDirectory(f.root); err != nil {
		return false, err
	}
	return true, nil
}

func syncInitializationDirectory(root *os.Root) error {
	parent, err := root.Open(".")
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Sync()
}
