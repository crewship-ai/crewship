package devcontainer

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"
)

// miseLockAuxReference finds the auxiliary directories a v4 mise.lock names,
// e.g. `aube = { path = "locks/opencode/2.3.0~009061c2", … }`.
var miseLockAuxReference = regexp.MustCompile(`path\s*=\s*"(locks/[A-Za-z0-9_.~+/-]+)"`)

// ReadMiseLockBundle packages only mise.lock and its auxiliary roots
// (.mise/locks for lockfile v3, locks for v4, #5545) from a local
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
	for _, parent := range []string{".mise", miseLockAuxV3, miseLockAuxV4} {
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
	// `.mise/locks` is mise's own directory. `locks` is a generic name a
	// project may use for something else, so only directories mise.lock
	// itself references are packed from it.
	referenced := map[string]bool{}
	for _, m := range miseLockAuxReference.FindAllStringSubmatch(b.Files["mise.lock"], -1) {
		referenced[m[1]] = true
	}
	for _, aux := range []string{miseLockAuxV3, miseLockAuxV4} {
		err = fs.WalkDir(root.FS(), aux, func(name string, d fs.DirEntry, err error) error {
			if errors.Is(err, fs.ErrNotExist) && name == aux {
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
			if aux == miseLockAuxV4 && !referenced[path.Dir(name)] {
				return nil
			}
			return read(name)
		})
		if err != nil {
			return nil, err
		}
	}
	if err := b.Validate(); err != nil {
		return nil, err
	}
	return b, nil
}
