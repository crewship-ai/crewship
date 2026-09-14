package memory

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// mutationFile pins the canonical file's parent for the whole operation. The
// absolute path remains the ledger identity, never a fallback for rooted I/O.
type mutationFile struct {
	path    string
	root    *os.Root
	name    string
	missing bool
}

func openMutationFile(storageRoot, path string, create bool) (*mutationFile, error) {
	file := &mutationFile{path: path}
	if storageRoot == "" {
		// Trusted in-process/legacy callers retain their existing path contract.
		if create {
			if err := os.MkdirAll(filepath.Dir(path), 0o775); err != nil {
				return nil, fmt.Errorf("mkdir parent: %w", err)
			}
		}
		return file, nil
	}
	base, err := filepath.Abs(storageRoot)
	if err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(base, absolute)
	if err != nil || !filepath.IsLocal(rel) || rel == "." {
		return nil, os.ErrPermission
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	// The next open is relative to the previous descriptor, not an absolute
	// pathname. Reject symlinks even when they lead to a sibling inside storage.
	for _, part := range strings.Split(filepath.Dir(rel), string(filepath.Separator)) {
		if part == "." {
			continue
		}
		info, err := root.Lstat(part)
		if errors.Is(err, os.ErrNotExist) && create {
			err = root.Mkdir(part, 0o775)
			if err == nil || errors.Is(err, os.ErrExist) {
				info, err = root.Lstat(part)
			}
		}
		if err != nil {
			_ = root.Close()
			if errors.Is(err, os.ErrNotExist) && !create {
				file.missing = true
				return file, nil
			}
			return nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			_ = root.Close()
			return nil, os.ErrPermission
		}
		next, err := root.OpenRoot(part)
		_ = root.Close()
		if err != nil {
			return nil, err
		}
		opened, err := next.Stat(".")
		if err != nil || !os.SameFile(info, opened) {
			_ = next.Close()
			return nil, os.ErrPermission
		}
		root = next
	}
	file.root, file.name = root, filepath.Base(rel)
	return file, nil
}

func (f *mutationFile) close() {
	if f.root != nil {
		_ = f.root.Close()
	}
}

func (f *mutationFile) read() ([]byte, error) {
	if f.missing {
		return nil, os.ErrNotExist
	}
	if f.root == nil {
		return readRegularNoFollow(f.path)
	}
	file, err := openRootNoFollow(f.root, f.name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file: %s", f.name)
	}
	return io.ReadAll(file)
}

func (f *mutationFile) write(content []byte) error {
	if f.root == nil {
		return writeFileDurable(f.path, content, 0o644)
	}
	return WriteFileDurableRoot(f.root, f.name, content, 0o644)
}

func (f *mutationFile) lock() interface {
	Lock() error
	Unlock() error
} {
	if f.root == nil {
		return NewFileLock(f.path + ".lock")
	}
	return NewRootFileLock(f.root, f.name+".lock")
}
