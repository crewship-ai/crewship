package devcontainer

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
)

// ReadMiseLockBundle packages only mise.lock and .mise/locks from a local
// directory. OpenRoot confines reads even if the directory changes while
// packing. Symlinks and special files are rejected; unrelated config files,
// including environment values, are never included.
func ReadMiseLockBundle(dir string) (*MiseLockBundle, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	b := &MiseLockBundle{SchemaVersion: 1, Files: map[string]string{}}
	total := 0
	read := func(name string) error {
		if !validMiseLockPath(name) || len(b.Files) >= maxMiseLockFiles {
			return fmt.Errorf("mise lock: invalid path or too many files")
		}
		stat, err := root.Lstat(name)
		if err != nil {
			return err
		}
		if !stat.Mode().IsRegular() {
			return fmt.Errorf("mise lock: only regular files are supported")
		}
		f, err := root.Open(name)
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(io.LimitReader(f, maxMiseLockFileBytes+1))
		closeErr := f.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		total += len(data)
		if len(data) > maxMiseLockFileBytes || total > maxMiseLockBytes {
			return fmt.Errorf("mise lock: bundle size limit exceeded")
		}
		b.Files[name] = string(data)
		return nil
	}
	if err := read("mise.lock"); err != nil {
		return nil, err
	}
	for _, parent := range []string{".mise", ".mise/locks"} {
		stat, err := root.Lstat(parent)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !stat.IsDir() {
			return nil, fmt.Errorf("mise lock: auxiliary directory must not be a symlink or file")
		}
	}
	err = fs.WalkDir(root.FS(), ".mise/locks", func(name string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) && name == ".mise/locks" {
			return nil
		}
		if err != nil {
			return err
		}
		if len(name) > 256 {
			return fmt.Errorf("mise lock: path too long")
		}
		if d.IsDir() {
			return nil
		}
		return read(name)
	})
	if err != nil {
		return nil, err
	}
	if err := b.Validate(); err != nil {
		return nil, err
	}
	return b, nil
}
