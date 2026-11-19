package devcontainer

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"
	"strings"
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
	// `.mise/locks` is mise's own directory and is packed whole. `locks` is a
	// generic name a project may use for something else, so from it only the
	// directories mise.lock itself references are walked; the rest of that
	// tree is never opened. A reference that is not a real directory, or that
	// yields no file, fails here rather than as an incomplete bundle at
	// `mise install --locked`.
	roots := []string{}
	for _, parent := range []string{".mise", miseLockAuxV3} {
		ok, err := lockDirPresent(root, parent)
		if err != nil {
			return nil, err
		}
		if ok && parent == miseLockAuxV3 {
			roots = append(roots, parent)
		}
	}
	seen := map[string]bool{}
	for _, m := range miseLockAuxReference.FindAllStringSubmatch(b.Files["mise.lock"], -1) {
		ref := m[1]
		if seen[ref] {
			continue
		}
		seen[ref] = true
		if path.Clean(ref) != ref || len(strings.Split(ref, "/")) < 2 {
			return nil, fmt.Errorf("mise lock: invalid auxiliary reference")
		}
		// Every component, including `locks` itself, must be a real directory.
		for dir := ref; dir != "."; dir = path.Dir(dir) {
			ok, err := lockDirPresent(root, dir)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, fmt.Errorf("mise lock: referenced auxiliary directory is missing")
			}
		}
		roots = append(roots, ref)
	}
	for _, aux := range roots {
		before := len(b.Files)
		err = fs.WalkDir(root.FS(), aux, func(name string, d fs.DirEntry, err error) error {
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
		if aux != miseLockAuxV3 && len(b.Files) == before {
			return nil, fmt.Errorf("mise lock: referenced auxiliary directory is empty")
		}
	}
	if err := b.Validate(); err != nil {
		return nil, err
	}
	return b, nil
}

// lockDirPresent reports whether name exists as a real directory. A symlink
// or file in its place is an error, never followed.
func lockDirPresent(root *os.Root, name string) (bool, error) {
	stat, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !stat.IsDir() {
		return false, fmt.Errorf("mise lock: auxiliary directory must not be a symlink or file")
	}
	return true, nil
}
